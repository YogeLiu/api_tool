package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/analyzer"
	"github.com/YogeLiu/api-tool/pkg/exporter"
	"github.com/YogeLiu/api-tool/pkg/models"
	"github.com/YogeLiu/api-tool/pkg/parser"
)

func main() {
	projectPath := flag.String("path", ".", "Go 项目根路径")
	outputFormat := flag.String("format", "json", "输出格式: json 或 swagger")
	outputFile := flag.String("output", "", "输出文件路径，为空则打印到 stdout")
	projectName := flag.String("project", "", "项目名称（仅 swagger 使用，默认取目录名）")
	pathFilter := flag.String("filter", "", "仅输出 path 包含该子串的路由")
	flag.Parse()

	if args := flag.Args(); len(args) > 0 {
		*projectPath = args[0]
	}

	pkgs, err := parser.Load(*projectPath)
	if err != nil {
		log.Fatalf("加载项目失败: %v", err)
	}

	apiInfo, err := analyzer.Analyze(pkgs)
	if err != nil {
		log.Fatalf("分析失败: %v", err)
	}

	if *pathFilter != "" {
		apiInfo = filterByPath(apiInfo, *pathFilter)
	}

	switch *outputFormat {
	case "swagger":
		if err := exportSwagger(apiInfo, *projectPath, *projectName, *outputFile); err != nil {
			log.Fatalf("Swagger 导出失败: %v", err)
		}
	default:
		if err := writeJSON(apiInfo, *outputFile); err != nil {
			log.Fatalf("JSON 输出失败: %v", err)
		}
	}
}

func writeJSON(info *models.APIInfo, outputFile string) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if outputFile == "" {
		_, err := os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(outputFile, data, 0644)
}

func filterByPath(info *models.APIInfo, sub string) *models.APIInfo {
	var routes []models.RouteInfo
	for _, r := range info.Routes {
		if strings.Contains(r.Path, sub) {
			routes = append(routes, r)
		}
	}
	return &models.APIInfo{Routes: routes, APINumber: len(routes)}
}

func exportSwagger(info *models.APIInfo, projectPath, projectName, outputFile string) error {
	if projectName == "" {
		projectName = filepath.Base(projectPath)
	}
	outputDir := "./swagger_exports"
	if outputFile != "" {
		outputDir = filepath.Dir(outputFile)
	}
	exp := exporter.NewSwaggerExporter(projectName, "1.0.0", "http://localhost:8080", outputDir, true)
	if err := exp.Export(info); err != nil {
		return err
	}
	fmt.Printf("Swagger 已导出到 %s\n", outputDir)
	return nil
}
