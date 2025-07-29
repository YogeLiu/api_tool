// 文件位置: pkg/extractor/gin_extractor.go
package extractor

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/models"
	"github.com/YogeLiu/api-tool/pkg/parser"

	"golang.org/x/tools/go/packages"
)

// GinExtractor 实现了针对Gin框架的API提取逻辑
type GinExtractor struct {
	project *parser.Project
}

// GetFrameworkName 返回框架名称
func (g *GinExtractor) GetFrameworkName() string {
	return "gin"
}

// FindRootRouters 查找gin.Engine类型的根路由器
func (g *GinExtractor) FindRootRouters(pkgs []*packages.Package) []types.Object {
	var routers []types.Object

	fmt.Printf("[DEBUG] GinExtractor.FindRootRouters: 开始查找，共有 %d 个包\n", len(pkgs))

	for i, pkg := range pkgs {
		fmt.Printf("[DEBUG] 处理包 %d: %s (包含 %d 个语法文件)\n", i, pkg.PkgPath, len(pkg.Syntax))

		for _, file := range pkg.Syntax {
			// 遍历所有声明
			for _, decl := range file.Decls {
				// 查找变量声明
				if genDecl, ok := decl.(*ast.GenDecl); ok {
					for _, spec := range genDecl.Specs {
						if valueSpec, ok := spec.(*ast.ValueSpec); ok {
							for _, name := range valueSpec.Names {
								if obj := pkg.TypesInfo.ObjectOf(name); obj != nil {
									fmt.Printf("[DEBUG] 检查变量 %s, 类型: %s\n", name.Name, obj.Type().String())
									if g.isGinEngine(obj.Type()) {
										fmt.Printf("[DEBUG] 找到gin.Engine变量: %s\n", name.Name)
										routers = append(routers, obj)
									}
								}
							}
						}
					}
				}

				// 查找函数中的变量赋值和gin.New()调用
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Name != nil {
						fmt.Printf("[DEBUG] 检查函数: %s\n", funcDecl.Name.Name)
					}
					ast.Inspect(funcDecl, func(node ast.Node) bool {
						switch n := node.(type) {
						case *ast.AssignStmt:
							// 查找赋值语句
							for _, rhs := range n.Rhs {
								if callExpr, ok := rhs.(*ast.CallExpr); ok {
									fmt.Printf("[DEBUG] 找到赋值语句中的调用表达式\n")
									if g.isGinNewCall(callExpr) {
										fmt.Printf("[DEBUG] 确认为gin.New()或gin.Default()调用\n")
										// 这是gin.New()或gin.Default()调用
										for _, lhs := range n.Lhs {
											if ident, ok := lhs.(*ast.Ident); ok {
												if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
													fmt.Printf("[DEBUG] 找到gin.New()调用结果变量: %s\n", ident.Name)
													routers = append(routers, obj)
												}
											}
										}
									}
								}
							}
						case *ast.ValueSpec:
							// 查找变量声明中的gin.New()调用
							for i, value := range n.Values {
								if callExpr, ok := value.(*ast.CallExpr); ok {
									if g.isGinNewCall(callExpr) {
										fmt.Printf("[DEBUG] 在变量声明中找到gin.New()调用\n")
										if i < len(n.Names) {
											if obj := pkg.TypesInfo.ObjectOf(n.Names[i]); obj != nil {
												fmt.Printf("[DEBUG] 找到gin.New()声明变量: %s\n", n.Names[i].Name)
												routers = append(routers, obj)
											}
										}
									}
								}
							}
						}
						return true
					})
				}
			}
		}
	}

	fmt.Printf("[DEBUG] FindRootRouters完成，找到 %d 个根路由器\n", len(routers))
	return routers
}

// isGinEngine 检查类型是否为gin.Engine或*gin.Engine
func (g *GinExtractor) isGinEngine(typ types.Type) bool {
	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			return obj.Pkg().Path() == "github.com/gin-gonic/gin" && obj.Name() == "Engine"
		}
	}

	return false
}

// isGinNewCall 检查是否为gin.New()或gin.Default()调用
func (g *GinExtractor) isGinNewCall(callExpr *ast.CallExpr) bool {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			fmt.Printf("[DEBUG] isGinNewCall: 检查调用 %s.%s\n", ident.Name, selExpr.Sel.Name)
			// 检查包名是否为gin
			if ident.Name == "gin" {
				// 检查方法名
				methodName := selExpr.Sel.Name
				if methodName == "New" || methodName == "Default" {
					fmt.Printf("[DEBUG] isGinNewCall: 确认为gin.%s()调用\n", methodName)
					return true
				}
			}
		}
	}
	return false
}

// IsRouteGroupCall 检查是否为路由分组调用
func (g *GinExtractor) IsRouteGroupCall(callExpr *ast.CallExpr, typeInfo *types.Info) (bool, string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		fmt.Printf("[DEBUG] IsRouteGroupCall: 检查方法 %s\n", selExpr.Sel.Name)
		if selExpr.Sel.Name == "Group" {
			// 检查调用者是否为gin相关类型
			if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
				fmt.Printf("[DEBUG] IsRouteGroupCall: 调用者类型 %s\n", typ.String())
				if g.isGinRouterGroup(typ) {
					fmt.Printf("[DEBUG] IsRouteGroupCall: 确认为Gin路由分组调用\n")
					// 提取路径参数
					if len(callExpr.Args) > 0 {
						if pathArg, ok := callExpr.Args[0].(*ast.BasicLit); ok {
							path := strings.Trim(pathArg.Value, "\"")
							fmt.Printf("[DEBUG] IsRouteGroupCall: 路径段 %s\n", path)
							return true, path
						}
					}
				}
			}
		}
	}
	return false, ""
}

// isGinRouterGroup 检查类型是否为gin相关的路由器类型
func (g *GinExtractor) isGinRouterGroup(typ types.Type) bool {
	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			pkgPath := obj.Pkg().Path()
			typeName := obj.Name()
			return pkgPath == "github.com/gin-gonic/gin" &&
				(typeName == "Engine" || typeName == "RouterGroup")
		}
	}

	return false
}

// IsHTTPMethodCall 检查是否为HTTP方法调用
func (g *GinExtractor) IsHTTPMethodCall(callExpr *ast.CallExpr, typeInfo *types.Info) (bool, string, string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		methodName := selExpr.Sel.Name
		fmt.Printf("[DEBUG] IsHTTPMethodCall: 检查方法 %s\n", methodName)
		httpMethods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}

		for _, method := range httpMethods {
			if methodName == method {
				// 检查调用者是否为gin相关类型
				if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
					fmt.Printf("[DEBUG] IsHTTPMethodCall: 调用者类型 %s\n", typ.String())
					if g.isGinRouterGroup(typ) {
						fmt.Printf("[DEBUG] IsHTTPMethodCall: 确认为Gin HTTP方法调用\n")
						// 提取路径参数
						if len(callExpr.Args) > 0 {
							if pathArg, ok := callExpr.Args[0].(*ast.BasicLit); ok {
								path := strings.Trim(pathArg.Value, "\"")
								fmt.Printf("[DEBUG] IsHTTPMethodCall: 方法 %s, 路径 %s\n", method, path)
								return true, method, path
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
func (g *GinExtractor) ExtractRequest(handlerDecl *ast.FuncDecl, typeInfo *types.Info, resolver TypeResolver) models.RequestInfo {
	request := models.RequestInfo{}

	if handlerDecl.Body == nil {
		return request
	}

	// 遍历函数体，查找gin相关的请求操作
	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
				methodName := selExpr.Sel.Name

				// 检查是否为gin的Context方法调用
				if g.isGinContextCall(selExpr.X, typeInfo) {
					switch methodName {
					case "Bind", "ShouldBind", "BindJSON", "ShouldBindJSON":
						// 提取请求体类型
						if len(callExpr.Args) > 0 {
							if typ := typeInfo.TypeOf(callExpr.Args[0]); typ != nil {
								request.Body = resolver(typ)
							}
						}
					case "Query":
						// 提取查询参数
						if len(callExpr.Args) > 0 {
							if keyArg, ok := callExpr.Args[0].(*ast.BasicLit); ok {
								key := strings.Trim(keyArg.Value, "\"")
								request.Query = append(request.Query, models.FieldInfo{
									Name: key,
									Type: "string",
								})
							}
						}
					case "Param":
						// 提取路径参数
						if len(callExpr.Args) > 0 {
							if keyArg, ok := callExpr.Args[0].(*ast.BasicLit); ok {
								key := strings.Trim(keyArg.Value, "\"")
								request.Params = append(request.Params, models.FieldInfo{
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
func (g *GinExtractor) ExtractResponse(handlerDecl *ast.FuncDecl, typeInfo *types.Info, resolver TypeResolver) models.ResponseInfo {
	response := models.ResponseInfo{}

	if handlerDecl.Body == nil {
		return response
	}

	// 遍历函数体，查找gin相关的响应操作
	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
				methodName := selExpr.Sel.Name

				// 检查是否为gin的Context方法调用
				if g.isGinContextCall(selExpr.X, typeInfo) {
					switch methodName {
					case "JSON":
						// 提取JSON响应类型
						if len(callExpr.Args) > 1 { // 第一个参数是状态码，第二个是数据
							if typ := typeInfo.TypeOf(callExpr.Args[1]); typ != nil {
								response.Body = resolver(typ)
							}
						}
					case "String", "HTML", "XML", "YAML":
						// 其他响应类型，默认为string
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

// isGinContextCall 检查是否为gin.Context的方法调用
func (g *GinExtractor) isGinContextCall(expr ast.Expr, typeInfo *types.Info) bool {
	if typ := typeInfo.TypeOf(expr); typ != nil {
		// 处理指针类型
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = ptr.Elem()
		}

		if named, ok := typ.(*types.Named); ok {
			obj := named.Obj()
			if obj != nil && obj.Pkg() != nil {
				return obj.Pkg().Path() == "github.com/gin-gonic/gin" && obj.Name() == "Context"
			}
		}
	}
	return false
}
