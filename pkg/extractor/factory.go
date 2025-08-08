// 文件位置: pkg/extractor/factory.go
package extractor

import (
	"fmt"
	"log"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/parser"
)

// DetectFramework 自动检测项目使用的Web框架
func DetectFramework(project *parser.Project) (string, error) {
	ginFound := false
	irisFound := false

	// 检查项目的导入
	for _, pkg := range project.Packages {
		for _, file := range pkg.Syntax {
			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, "\"")

				if strings.Contains(importPath, "github.com/gin-gonic/gin") {
					ginFound = true
				}
				if strings.Contains(importPath, "github.com/kataras/iris") {
					irisFound = true
				}
			}
		}
	}

	if ginFound && irisFound {
		return "", fmt.Errorf("检测到多个框架，请手动指定")
	}

	if ginFound {
		return "gin", nil
	}

	if irisFound {
		return "iris", nil
	}

	return "", fmt.Errorf("未检测到支持的Web框架")
}

// NewGinExtractor 创建Gin框架提取器
func NewGinExtractor(project *parser.Project) Extractor {
	log.Printf("[DEBUG] 🔥 NewGinExtractor 被调用，创建GinExtractor实例 🔥\n")
	return &GinExtractor{
		project: project,
	}
}

// NewIrisExtractor 创建Iris框架提取器
func NewIrisExtractor(project *parser.Project) Extractor {
	return &IrisExtractor{
		project: project,
	}
}

// CreateExtractor 根据框架名称创建对应的提取器
func CreateExtractor(framework string, project *parser.Project) (Extractor, error) {
	switch strings.ToLower(framework) {
	case "gin":
		return NewGinExtractor(project), nil
	case "iris":
		return NewIrisExtractor(project), nil
	default:
		return nil, fmt.Errorf("不支持的框架: %s", framework)
	}
}
