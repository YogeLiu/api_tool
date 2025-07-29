package extractor

import (
	"go/ast"
	"go/types"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/models"
	"github.com/YogeLiu/api-tool/pkg/parser"

	"golang.org/x/tools/go/packages"
)

// IrisExtractor 实现了针对Iris框架的API提取逻辑
type IrisExtractor struct {
	project *parser.Project
}

// GetFrameworkName 返回框架名称
func (i *IrisExtractor) GetFrameworkName() string {
	return "iris"
}

// FindRootRouters 查找iris.Application类型的根路由器
func (i *IrisExtractor) FindRootRouters(pkgs []*packages.Package) []types.Object {
	var routers []types.Object

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			// 遍历所有声明
			for _, decl := range file.Decls {
				// 查找变量声明
				if genDecl, ok := decl.(*ast.GenDecl); ok {
					for _, spec := range genDecl.Specs {
						if valueSpec, ok := spec.(*ast.ValueSpec); ok {
							for _, name := range valueSpec.Names {
								if obj := pkg.TypesInfo.ObjectOf(name); obj != nil {
									if i.isIrisApplication(obj.Type()) {
										routers = append(routers, obj)
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return routers
}

// isIrisApplication 检查类型是否为iris.Application
func (i *IrisExtractor) isIrisApplication(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			return obj.Pkg().Path() == "github.com/kataras/iris/v12" && obj.Name() == "Application"
		}
	}

	return false
}

// IsRouteGroupCall 检查是否为路由分组调用
func (i *IrisExtractor) IsRouteGroupCall(callExpr *ast.CallExpr, typeInfo *types.Info) (bool, string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if selExpr.Sel.Name == "Party" {
			if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
				if i.isIrisParty(typ) {
					if len(callExpr.Args) > 0 {
						if pathArg, ok := callExpr.Args[0].(*ast.BasicLit); ok {
							path := strings.Trim(pathArg.Value, "\"")
							return true, path
						}
					}
				}
			}
		}
	}
	return false, ""
}

// isIrisParty 检查类型是否为iris相关的路由器类型
func (i *IrisExtractor) isIrisParty(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			pkgPath := obj.Pkg().Path()
			typeName := obj.Name()
			return pkgPath == "github.com/kataras/iris/v12" &&
				(typeName == "Application" || typeName == "Party")
		}
	}

	return false
}

// IsHTTPMethodCall 检查是否为HTTP方法调用
func (i *IrisExtractor) IsHTTPMethodCall(callExpr *ast.CallExpr, typeInfo *types.Info) (bool, string, string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		methodName := selExpr.Sel.Name
		httpMethods := []string{"Get", "Post", "Put", "Delete", "Patch", "Head", "Options"}

		for _, method := range httpMethods {
			if methodName == method {
				if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
					if i.isIrisParty(typ) {
						if len(callExpr.Args) > 0 {
							if pathArg, ok := callExpr.Args[0].(*ast.BasicLit); ok {
								path := strings.Trim(pathArg.Value, "\"")
								return true, strings.ToUpper(method), path
							}
						}
					}
				}
			}
		}
	}
	return false, "", ""
}

// ExtractRequest 提取请求信息
func (i *IrisExtractor) ExtractRequest(handlerDecl *ast.FuncDecl, typeInfo *types.Info, resolver TypeResolver) models.RequestInfo {
	request := models.RequestInfo{}

	if handlerDecl.Body == nil {
		return request
	}

	// 遍历函数体，查找iris相关的请求操作
	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
				methodName := selExpr.Sel.Name

				if i.isIrisContextCall(selExpr.X, typeInfo) {
					switch methodName {
					case "ReadJSON":
						if len(callExpr.Args) > 0 {
							if typ := typeInfo.TypeOf(callExpr.Args[0]); typ != nil {
								request.Body = resolver(typ)
							}
						}
					case "URLParam":
						if len(callExpr.Args) > 0 {
							if keyArg, ok := callExpr.Args[0].(*ast.BasicLit); ok {
								key := strings.Trim(keyArg.Value, "\"")
								request.Query = append(request.Query, models.FieldInfo{
									Name: key,
									Type: "string",
								})
							}
						}
					}
				}
			}
		}
		return true
	})

	return request
}

// ExtractResponse 提取响应信息
func (i *IrisExtractor) ExtractResponse(handlerDecl *ast.FuncDecl, typeInfo *types.Info, resolver TypeResolver) models.ResponseInfo {
	response := models.ResponseInfo{}

	if handlerDecl.Body == nil {
		return response
	}

	// 遍历函数体，查找iris相关的响应操作
	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
				methodName := selExpr.Sel.Name

				if i.isIrisContextCall(selExpr.X, typeInfo) {
					switch methodName {
					case "JSON":
						if len(callExpr.Args) > 0 {
							if typ := typeInfo.TypeOf(callExpr.Args[0]); typ != nil {
								response.Body = resolver(typ)
							}
						}
					case "WriteString", "HTML", "XML", "YAML":
						response.Body = &models.FieldInfo{
							Type: "string",
						}
					}
				}
			}
		}
		return true
	})

	return response
}

// isIrisContextCall 检查是否为iris.Context的方法调用
func (i *IrisExtractor) isIrisContextCall(expr ast.Expr, typeInfo *types.Info) bool {
	if typ := typeInfo.TypeOf(expr); typ != nil {
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = ptr.Elem()
		}

		if named, ok := typ.(*types.Named); ok {
			obj := named.Obj()
			if obj != nil && obj.Pkg() != nil {
				return obj.Pkg().Path() == "github.com/kataras/iris/v12/context" && obj.Name() == "Context"
			}
		}
	}
	return false
}
