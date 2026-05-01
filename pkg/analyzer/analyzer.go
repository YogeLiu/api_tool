// Package analyzer 把 extractor 找到的路由 + 各包 AST 转化为带请求/响应
// 结构信息的 models.APIInfo。
package analyzer

import (
	"go/ast"
	"go/types"
	"sort"

	"github.com/YogeLiu/api-tool/pkg/extractor"
	"github.com/YogeLiu/api-tool/pkg/models"
	"golang.org/x/tools/go/packages"
)

// Analyze 入口：给定已加载的包，返回完整的 APIInfo。
func Analyze(pkgs []*packages.Package) (*models.APIInfo, error) {
	rawRoutes := extractor.FindRoutes(pkgs)
	wrappers := buildWrapperIndex(pkgs)

	out := &models.APIInfo{}
	for _, r := range rawRoutes {
		route := buildRoute(r, pkgs, wrappers)
		if route == nil {
			continue
		}
		out.Routes = append(out.Routes, *route)
	}

	sort.Slice(out.Routes, func(i, j int) bool {
		if out.Routes[i].Path != out.Routes[j].Path {
			return out.Routes[i].Path < out.Routes[j].Path
		}
		return out.Routes[i].Method < out.Routes[j].Method
	})
	out.APINumber = len(out.Routes)
	return out, nil
}

// buildRoute 解析单条路由的 handler，填充请求/响应字段。
func buildRoute(r extractor.Route, pkgs []*packages.Package, wrappers wrapperIndex) *models.RouteInfo {
	handlerDecl, handlerPkg := resolveHandler(r.HandlerArg, r.Pkg, pkgs)
	route := &models.RouteInfo{
		Method: r.Method,
		Path:   r.Path,
	}
	if handlerDecl == nil || handlerPkg == nil {
		// 仍然返回基础路由信息，便于上游观察未解析项
		route.Handler = handlerName(r.HandlerArg)
		return route
	}

	route.Handler = handlerDecl.Name.Name
	route.PackageName = handlerPkg.Name
	route.PackagePath = handlerPkg.PkgPath
	if handlerPkg.Fset != nil {
		route.HandlerStartLine = handlerPkg.Fset.Position(handlerDecl.Pos()).Line
		route.HandlerEndLine = handlerPkg.Fset.Position(handlerDecl.End()).Line
	}
	route.RequestParams = extractRequestParams(handlerDecl, handlerPkg)
	route.ResponseSchema = analyzeResponse(handlerDecl, handlerPkg, wrappers)
	return route
}

// resolveHandler 根据 .GET("/x", h) 中的 h 表达式找到 FuncDecl。
// 优先用 TypesInfo.ObjectOf 走类型系统；找不到则按包路径在已加载包内回退查找。
func resolveHandler(arg ast.Expr, callerPkg *packages.Package, pkgs []*packages.Package) (*ast.FuncDecl, *packages.Package) {
	switch e := arg.(type) {
	case *ast.Ident:
		obj := callerPkg.TypesInfo.ObjectOf(e)
		return findFuncDecl(obj, pkgs)
	case *ast.SelectorExpr:
		obj := callerPkg.TypesInfo.ObjectOf(e.Sel)
		return findFuncDecl(obj, pkgs)
	case *ast.FuncLit:
		fn := &ast.FuncDecl{
			Name: &ast.Ident{Name: "anonymous"},
			Type: e.Type,
			Body: e.Body,
		}
		return fn, callerPkg
	}
	return nil, nil
}

func findFuncDecl(obj types.Object, pkgs []*packages.Package) (*ast.FuncDecl, *packages.Package) {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return nil, nil
	}
	for _, pkg := range pkgs {
		if pkg.PkgPath != fn.Pkg().Path() {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				if pkg.TypesInfo.ObjectOf(fd.Name) == fn {
					return fd, pkg
				}
			}
		}
	}
	return nil, nil
}

// handlerName 在 handler 解析失败时给出可读名字。
func handlerName(arg ast.Expr) string {
	switch e := arg.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.FuncLit:
		return "anonymous"
	}
	return "unknown"
}
