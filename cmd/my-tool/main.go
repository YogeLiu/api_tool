// 文件位置: cmd/my-tool/main.go
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/analyzer"
	"github.com/YogeLiu/api-tool/pkg/exporter"
	"github.com/YogeLiu/api-tool/pkg/extractor"
	"github.com/YogeLiu/api-tool/pkg/models"
	"github.com/YogeLiu/api-tool/pkg/parser"
)

func main() {
	projectPath := flag.String("path", ".", "要分析的 Go 项目的根路径。")
	framework := flag.String("framework", "gin", "目标框架 (gin 或 iris)。")
	outputFormat := flag.String("format", "json", "输出格式 (json, yapi 或 swagger)。")
	outputFile := flag.String("output", "", "输出文件路径 (可选)。")
	projectName := flag.String("project", "", "项目名称 (YAPI格式时使用)。")
	pathFilter := flag.String("filter", "", "路径过滤器，只显示包含指定路径的路由 (可选)。")
	flag.Parse()

	// 检查是否有位置参数，如果有则使用位置参数作为项目路径
	args := flag.Args()
	if len(args) > 0 {
		*projectPath = args[0]
	}

	log.Printf("项目路径: %s", *projectPath)

	log.Println("1. 解析项目代码...")
	proj, err := parser.ParseProject(*projectPath)
	if err != nil {
		log.Fatalf("项目解析失败: %v", err)
	}

	log.Println("2. 选择框架提取器:", *framework)
	var ext extractor.Extractor
	switch *framework {
	case "gin":
		ext = extractor.NewGinExtractor(proj)
	case "iris":
		ext = extractor.NewIrisExtractor(proj)
	default:
		log.Fatalf("不支持的框架: %s", *framework)
	}

	log.Println("3. 运行核心分析器...")
	coreAnalyzer := analyzer.NewAnalyzer(*projectPath, proj, ext)
	apiInfo, err := coreAnalyzer.Analyze()
	if err != nil {
		log.Fatalf("核心分析失败: %v", err)
	}

	// 如果指定了路径过滤器，过滤路由
	if *pathFilter != "" {
		apiInfo = filterRoutesByPath(apiInfo, *pathFilter)
		log.Printf("路径过滤器 '%s' 应用后，剩余路由数: %d", *pathFilter, len(apiInfo.Routes))
	}

	log.Printf("4. 生成 %s 格式输出...", *outputFormat)

	switch *outputFormat {
	case "yapi":
		// YAPI格式导出
		if err := exportToYAPI(apiInfo, *projectPath, *projectName, *outputFile); err != nil {
			log.Fatalf("YAPI导出失败: %v", err)
		}
	case "swagger":
		// Swagger格式导出
		if err := exportToSwagger(apiInfo, *projectPath, *projectName, *outputFile); err != nil {
			log.Fatalf("Swagger导出失败: %v", err)
		}
	default:
		// 默认JSON格式输出
		output, err := json.MarshalIndent(apiInfo, "", "  ")
		if err != nil {
			log.Fatalf("JSON序列化失败: %v", err)
		}

		if *outputFile != "" {
			// 保存到文件
			if err := os.WriteFile(*outputFile, output, 0644); err != nil {
				log.Fatalf("保存文件失败: %v", err)
			}
			log.Printf("✅ JSON输出已保存到: %s", *outputFile)
		} else {
			// 输出到控制台
			printRoutesToTerminal(apiInfo)
		}
	}

	log.Println("\n分析完成。")
}

// exportToYAPI 导出为YAPI格式
func exportToYAPI(apiInfo *models.APIInfo, projectPath, projectName, outputFile string) error {
	// 如果没有指定项目名称，使用项目路径的最后一部分
	if projectName == "" {
		projectName = filepath.Base(projectPath)
	}

	// 确定输出目录
	outputDir := "./yapi_exports"
	if outputFile != "" {
		outputDir = filepath.Dir(outputFile)
	}

	// 创建YAPI导出器
	yapiExporter := exporter.NewYAPIExporter(projectName, "", outputDir)

	// 执行导出
	return yapiExporter.Export(apiInfo)
}

// exportToSwagger 导出为Swagger格式
func exportToSwagger(apiInfo *models.APIInfo, projectPath, projectName, outputFile string) error {
	// 如果没有指定项目名称，使用项目路径的最后一部分
	if projectName == "" {
		projectName = filepath.Base(projectPath)
	}

	// 确定输出目录
	outputDir := "./swagger_exports"
	if outputFile != "" {
		outputDir = filepath.Dir(outputFile)
	}

	// 创建Swagger导出器
	swaggerExporter := exporter.NewSwaggerExporter(projectName, "1.0.0", "http://localhost:8080", outputDir, true)

	// 执行导出
	return swaggerExporter.Export(apiInfo)
}

// filterRoutesByPath 根据路径过滤器过滤路由
func filterRoutesByPath(apiInfo *models.APIInfo, pathFilter string) *models.APIInfo {
	var filteredRoutes []models.RouteInfo

	for _, route := range apiInfo.Routes {
		if strings.Contains(route.Path, pathFilter) {
			filteredRoutes = append(filteredRoutes, route)
		}
	}

	return &models.APIInfo{
		Routes: filteredRoutes,
	}
}

// printRoutesToTerminal 以JSON格式打印路由到终端
func printRoutesToTerminal(apiInfo *models.APIInfo) {
	output, err := json.MarshalIndent(apiInfo, "", "  ")
	if err != nil {
		log.Fatalf("JSON序列化失败: %v", err)
	}

	os.Stdout.Write(output)
}
