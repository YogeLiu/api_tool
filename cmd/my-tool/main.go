// 文件位置: cmd/my-tool/main.go
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"

	"github.com/YogeLiu/api-tool/pkg/analyzer"
	"github.com/YogeLiu/api-tool/pkg/extractor"
	"github.com/YogeLiu/api-tool/pkg/parser"
)

func main() {
	projectPath := flag.String("path", ".", "要分析的 Go 项目的根路径。")
	framework := flag.String("framework", "gin", "目标框架 (gin 或 iris)。")
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

	log.Println("4. 生成 JSON 输出...")
	output, err := json.MarshalIndent(apiInfo, "", "  ")
	if err != nil {
		log.Fatalf("JSON序列化失败: %v", err)
	}

	os.Stdout.Write(output)
	log.Println("\n分析完成。")
}
