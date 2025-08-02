package main

import (
	"fmt"
	"log"
	"os"

	"github.com/YogeLiu/api-tool/helper"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: go run main.go <项目目录>")
		fmt.Println("示例: go run main.go ./my-gin-project")
		os.Exit(1)
	}

	projectDir := os.Args[1]
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		fmt.Printf("❌ 目录不存在: %s\n", projectDir)
		os.Exit(1)
	}

	fmt.Printf("🔍 开始解析项目: %s\n", projectDir)

	analyzer, err := helper.NewGinHandlerAnalyzer(projectDir)
	if err != nil {
		log.Fatalf("❌ 初始化分析器失败: %v", err)
	}

	analyzer.Analyze()
	fmt.Println("\n✅ 解析完成")
}
