// 文件位置: pkg/extractor/gin_extractor.go
package extractor

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/models"
	"github.com/YogeLiu/api-tool/pkg/parser"
	"golang.org/x/tools/go/packages"
)

// IrisExtractor 实现了 Extractor 接口，仅关注路由解析逻辑
type IrisExtractor struct {
	project *parser.Project
}

// GetFrameworkName 返回框架名称
func (g *IrisExtractor) GetFrameworkName() string {
	return "iris"
}

// InitializeAnalysis 初始化分析器
func (g *IrisExtractor) InitializeAnalysis() error {
	// 由于只关注路由解析，不需要复杂的初始化
	return nil
}

// FindRootRouters 查找router.APIBuilder和iris.Application类型的根路由器
func (g *IrisExtractor) FindRootRouters(pkgs []*packages.Package) []types.Object {
	var routers []types.Object

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				// 查找赋值语句
				if assign, ok := node.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
					if lhs, ok := assign.Lhs[0].(*ast.Ident); ok {
						if callExpr, ok := assign.Rhs[0].(*ast.CallExpr); ok {
							if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
								if ident, ok := selExpr.X.(*ast.Ident); ok && ident.Name == "iris" {
									// 查找 iris.Default() 或 iris.New()
									if selExpr.Sel.Name == "Default" || selExpr.Sel.Name == "New" {
										if obj := pkg.TypesInfo.ObjectOf(lhs); obj != nil {
											routers = append(routers, obj)
										}
									}
								}
							}
						}
					}
				}

				// 查找函数参数中的router.APIBuilder类型
				if funcDecl, ok := node.(*ast.FuncDecl); ok {
					if funcDecl.Type.Params != nil {
						for _, param := range funcDecl.Type.Params.List {
							if param.Type != nil {
								typ := pkg.TypesInfo.TypeOf(param.Type)
								if typ != nil && g.IsIrisEngine(typ) {
									// 为每个参数创建一个对象
									for _, name := range param.Names {
										if obj := pkg.TypesInfo.ObjectOf(name); obj != nil {
											routers = append(routers, obj)
										}
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

	return routers
}

// IsIrisEngine 检查类型为router.APIBuilder或iris.Application
func (g *IrisExtractor) IsIrisEngine(typ types.Type) bool {
	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			// 检查是否为 router.APIBuilder
			if obj.Pkg().Path() == "github.com/kataras/iris/core/router" && obj.Name() == "APIBuilder" {
				return true
			}
			// 检查是否为 iris.Application
			if obj.Pkg().Path() == "github.com/kataras/iris" && obj.Name() == "Application" {
				return true
			}
		}
	}
	return false
}

// IsIrisRouterGroup 检查类型是否为router.Party
func (g *IrisExtractor) IsIrisRouterGroup(typ types.Type) bool {
	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			// 检查是否为 router.Party
			if obj.Pkg().Path() == "github.com/kataras/iris/core/router" && obj.Name() == "Party" {
				return true
			}
			// 也支持直接的 iris.Party (向后兼容)
			if obj.Pkg().Path() == "github.com/kataras/iris" && obj.Name() == "Party" {
				return true
			}
		}
	}
	return false
}

// IsRouterParameter 检查函数参数是否为路由器类型
func (g *IrisExtractor) IsRouterParameter(param *ast.Field, typeInfo *types.Info) bool {
	if param.Type == nil {
		return false
	}

	typ := typeInfo.TypeOf(param.Type)
	if typ == nil {
		return false
	}

	// 检查是否为 *router.APIBuilder、*iris.Application 或 *router.Party
	return g.IsIrisEngine(typ) || g.IsIrisRouterGroup(typ)
}

// FindRouterGroupFunctions 查找所有接受路由器参数的函数（路由分组函数）
func (g *IrisExtractor) FindRouterGroupFunctions(pkgs []*packages.Package) map[string]*models.RouterGroupFunction {
	routerGroupFunctions := make(map[string]*models.RouterGroupFunction)

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Type.Params != nil {
						// 检查每个参数是否为路由器类型
						for _, param := range funcDecl.Type.Params.List {
							if g.IsRouterParameter(param, pkg.TypesInfo) {
								uniqueKey := pkg.PkgPath + "+" + funcDecl.Name.Name
								routerGroupFunctions[uniqueKey] = &models.RouterGroupFunction{
									PackagePath:  pkg.PkgPath,
									FunctionName: funcDecl.Name.Name,
									FuncDecl:     funcDecl,
									Package:      pkg,
								}
								break
							}
						}
					}
				}
			}
		}
	}

	return routerGroupFunctions
}

// IsRouteGroupCall 判断一个调用表达式是否为路由分组（如 .Group()）
func (g *IrisExtractor) IsRouteGroupCall(callExpr *ast.CallExpr, typeInfo *types.Info) (isGroup bool, pathSegment string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if selExpr.Sel.Name == "Party" {
			// 检查调用者是否为iris相关类型
			if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
				if g.IsIrisEngine(typ) || g.IsIrisRouterGroup(typ) {
					// 提取路径参数
					if len(callExpr.Args) > 0 {
						if lit, ok := callExpr.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							pathSegment = strings.Trim(lit.Value, `"`)
							return true, pathSegment
						}
					}
				}
			}
		}
	}
	return false, ""
}

// IsHTTPMethodCall 判断一个调用表达式是否为 HTTP 方法注册
func (g *IrisExtractor) IsHTTPMethodCall(callExpr *ast.CallExpr, typeInfo *types.Info) (isHTTP bool, httpMethod, pathSegment string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		// 检查调用者是否为iris相关类型
		if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
			if g.IsIrisEngine(typ) || g.IsIrisRouterGroup(typ) {
				// 处理Handle方法: Handle("GET", "/path", handler)
				if selExpr.Sel.Name == "Handle" && len(callExpr.Args) >= 2 {
					// 第一个参数是HTTP方法
					if methodLit, ok := callExpr.Args[0].(*ast.BasicLit); ok && methodLit.Kind == token.STRING {
						httpMethod = strings.Trim(methodLit.Value, `"`)
					}
					// 第二个参数是路径
					if pathLit, ok := callExpr.Args[1].(*ast.BasicLit); ok && pathLit.Kind == token.STRING {
						pathSegment = strings.Trim(pathLit.Value, `"`)
					}
					return true, httpMethod, pathSegment
				}

				// 处理HTTP方法的简写形式: Get(), Post(), Put(), Delete() 等
				httpMethods := []string{"Get", "Post", "Put", "Delete", "Patch", "Head", "Options", "Any"}
				for _, method := range httpMethods {
					if selExpr.Sel.Name == method {
						// 提取路径参数
						if len(callExpr.Args) > 0 {
							if lit, ok := callExpr.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								pathSegment = strings.Trim(lit.Value, `"`)
								// 将方法名转换为大写形式
								httpMethod = strings.ToUpper(method)
								if method == "Any" {
									httpMethod = "ANY"
								}
								return true, httpMethod, pathSegment
							}
						}
					}
				}
			}
		}
	}
	return false, "", ""
}
