package parser

import (
	"fmt"
	"os"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/models"
	"golang.org/x/tools/go/packages"
)

// Load 加载指定目录下所有 Go 包，返回去除空包后的列表。
func Load(projectPath string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedDeps |
			packages.NeedImports,
		Tests: false,
		Dir:   projectPath,
		Env:   append(os.Environ(), "GOFLAGS=-mod=mod"),
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, &models.ParseError{Path: projectPath, Reason: err.Error()}
	}

	var errs []string
	var valid []*packages.Package
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			errs = append(errs, fmt.Sprintf("%s: %v", pkg.PkgPath, e))
		}
		if pkg.PkgPath != "" && len(pkg.Syntax) > 0 {
			valid = append(valid, pkg)
		}
	}
	if len(valid) == 0 {
		return nil, &models.ParseError{
			Path:   projectPath,
			Reason: "未找到有效的 Go 包: " + strings.Join(errs, "; "),
		}
	}
	return valid, nil
}
