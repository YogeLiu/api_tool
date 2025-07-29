// 文件位置: pkg/parser/parser.go
package parser

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"

	"github.com/YogeLiu/api-tool/pkg/models"

	"golang.org/x/tools/go/packages"
)

// ParseProject 解析指定路径的Go项目
func ParseProject(projectPath string) (*Project, error) {
	// 配置包加载选项
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedImports |
			packages.NeedTypes |
			packages.NeedTypesSizes |
			packages.NeedSyntax |
			packages.NeedTypesInfo,
		Dir: projectPath,
		Env: append(os.Environ(), "GOFLAGS=-mod=vendor"),
	}

	// 加载项目中的所有包
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, &models.ParseError{
			Path:   projectPath,
			Reason: fmt.Sprintf("加载包失败: %v", err),
		}
	}

	// 检查是否有解析错误
	var parseErrors []string
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			for _, pkgErr := range pkg.Errors {
				parseErrors = append(parseErrors, fmt.Sprintf("包 %s: %v", pkg.PkgPath, pkgErr))
			}
		}
	}

	if len(parseErrors) > 0 {
		return nil, &models.ParseError{
			Path:   projectPath,
			Reason: fmt.Sprintf("包解析错误: %v", parseErrors),
		}
	}

	// 过滤掉空包或无效包
	var validPkgs []*packages.Package
	for _, pkg := range pkgs {
		if pkg.PkgPath != "" && len(pkg.Syntax) > 0 {
			validPkgs = append(validPkgs, pkg)
		}
	}

	if len(validPkgs) == 0 {
		return nil, &models.ParseError{
			Path:   projectPath,
			Reason: "没有找到有效的Go包",
		}
	}

	// 创建并返回Project实例
	project := NewProject(validPkgs)
	return project, nil
}

// GetFilePosition 获取AST节点在源文件中的位置信息
func GetFilePosition(pkg *packages.Package, pos token.Pos) (string, int, error) {
	if !pos.IsValid() {
		return "", 0, fmt.Errorf("无效的位置信息")
	}

	position := pkg.Fset.Position(pos)
	return position.Filename, position.Line, nil
}

// FindFunctionByName 在包中查找指定名称的函数
func FindFunctionByName(pkg *packages.Package, funcName string) (*ast.FuncDecl, error) {
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				if funcDecl.Name.Name == funcName {
					return funcDecl, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("找不到函数: %s", funcName)
}
