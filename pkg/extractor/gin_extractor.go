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

// GinExtractor 实现了 Extractor 接口，仅关注路由解析逻辑
type GinExtractor struct {
	project *parser.Project
}

// GetFrameworkName 返回框架名称
func (g *GinExtractor) GetFrameworkName() string {
	return "gin"
}

// InitializeAnalysis 初始化分析器
func (g *GinExtractor) InitializeAnalysis() error {
	// 由于只关注路由解析，不需要复杂的初始化
	return nil
}

// FindRootRouters 查找gin.Engine类型的根路由器
func (g *GinExtractor) FindRootRouters(pkgs []*packages.Package) []types.Object {
	var routers []types.Object

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				if assign, ok := node.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
					if lhs, ok := assign.Lhs[0].(*ast.Ident); ok {
						if callExpr, ok := assign.Rhs[0].(*ast.CallExpr); ok {
							if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
								if ident, ok := selExpr.X.(*ast.Ident); ok && ident.Name == "gin" {
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
				return true
			})
		}
	}

	return routers
}

// IsGinEngine 检查类型是否为gin.Engine
func (g *GinExtractor) IsGinEngine(typ types.Type) bool {
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

// IsGinRouterGroup 检查类型是否为gin.RouterGroup
func (g *GinExtractor) IsGinRouterGroup(typ types.Type) bool {
	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			return obj.Pkg().Path() == "github.com/gin-gonic/gin" && obj.Name() == "RouterGroup"
		}
	}
	return false
}

// IsRouterParameter 检查函数参数是否为路由器类型
func (g *GinExtractor) IsRouterParameter(param *ast.Field, typeInfo *types.Info) bool {
	if param.Type == nil {
		return false
	}

	typ := typeInfo.TypeOf(param.Type)
	if typ == nil {
		return false
	}

	// 检查是否为 *gin.Engine 或 *gin.RouterGroup
	return g.IsGinEngine(typ) || g.IsGinRouterGroup(typ)
}

// FindRouterGroupFunctions 查找所有接受路由器参数的函数（路由分组函数）
func (g *GinExtractor) FindRouterGroupFunctions(pkgs []*packages.Package) map[string]*models.RouterGroupFunction {
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
func (g *GinExtractor) IsRouteGroupCall(callExpr *ast.CallExpr, typeInfo *types.Info) (isGroup bool, pathSegment string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if selExpr.Sel.Name == "Group" {
			// 检查调用者是否为gin相关类型
			if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
				if g.IsGinEngine(typ) || g.IsGinRouterGroup(typ) {
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
func (g *GinExtractor) IsHTTPMethodCall(callExpr *ast.CallExpr, typeInfo *types.Info) (isHTTP bool, httpMethod, pathSegment string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		httpMethods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}
		for _, method := range httpMethods {
			if selExpr.Sel.Name == method {
				// 检查调用者是否为gin相关类型
				if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
					if g.IsGinEngine(typ) || g.IsGinRouterGroup(typ) {
						// 提取路径参数
						if len(callExpr.Args) > 0 {
							if lit, ok := callExpr.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
								pathSegment = strings.Trim(lit.Value, `"`)
								return true, method, pathSegment
							}
						}
					}
				}
			}
		}
	}
	return false, "", ""
}
