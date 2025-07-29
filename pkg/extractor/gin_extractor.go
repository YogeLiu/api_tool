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
									if g.IsGinEngine(obj.Type()) {
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
				if g.IsGinRouterGroup(typ) {
					fmt.Printf("[DEBUG] IsRouteGroupCall: 确认为Gin路由分组调用\n")
					// 提取路径参数
					if len(callExpr.Args) > 0 {
						path := g.extractPathFromExpression(callExpr.Args[0], typeInfo)
						fmt.Printf("[DEBUG] IsRouteGroupCall: 路径段 %s\n", path)
						return true, path
					}
				}
			}
		}
	}
	return false, ""
}

// IsGinRouterGroup 检查类型是否为gin相关的路由器类型
func (g *GinExtractor) IsGinRouterGroup(typ types.Type) bool {
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
					if g.IsGinRouterGroup(typ) {
						fmt.Printf("[DEBUG] IsHTTPMethodCall: 确认为Gin HTTP方法调用\n")
						// 提取路径参数
						if len(callExpr.Args) > 0 {
							path := g.extractPathFromExpression(callExpr.Args[0], typeInfo)
							fmt.Printf("[DEBUG] IsHTTPMethodCall: 方法 %s, 路径 %s\n", method, path)
							return true, method, path
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

// FindRouterGroupFunctions 查找所有接受Gin路由器参数的函数（路由分组函数）
func (g *GinExtractor) FindRouterGroupFunctions(pkgs []*packages.Package) map[string]*models.RouterGroupFunction {
	routerGroupFunctions := make(map[string]*models.RouterGroupFunction)

	fmt.Printf("[DEBUG] GinExtractor.FindRouterGroupFunctions: 开始查找路由分组函数，共有 %d 个包\n", len(pkgs))

	for _, pkg := range pkgs {
		fmt.Printf("[DEBUG] 检查包: %s\n", pkg.PkgPath)
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Type.Params != nil {
						// 检查每个参数是否为路由器类型
						for i, param := range funcDecl.Type.Params.List {
							if g.IsRouterParameter(param, pkg.TypesInfo) {
								uniqueKey := pkg.PkgPath + "+" + funcDecl.Name.Name
								fmt.Printf("[DEBUG] 找到路由分组函数: %s (参数索引: %d)\n", uniqueKey, i)

								routerGroupFunctions[uniqueKey] = &models.RouterGroupFunction{
									PackagePath:    pkg.PkgPath,
									FunctionName:   funcDecl.Name.Name,
									FuncDecl:       funcDecl,
									Package:        pkg,
									RouterParamIdx: i,
									UniqueKey:      uniqueKey,
								}
								break // 找到一个路由器参数就足够了
							}
						}
					}
				}
			}
		}
	}

	fmt.Printf("[DEBUG] FindRouterGroupFunctions完成，找到 %d 个路由分组函数\n", len(routerGroupFunctions))
	return routerGroupFunctions
}

// IsRouterParameter 检查函数参数是否为Gin路由器类型
func (g *GinExtractor) IsRouterParameter(param *ast.Field, typeInfo *types.Info) bool {
	if param.Type != nil {
		// 获取参数类型
		if typ := typeInfo.TypeOf(param.Type); typ != nil {
			// 检查是否为Gin路由器相关类型
			return g.IsGinRouterGroup(typ)
		}
	}
	return false
}

// extractPathFromExpression 从表达式中提取路径，支持多种表达式类型
func (g *GinExtractor) extractPathFromExpression(expr ast.Expr, typeInfo *types.Info) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		// 字符串字面量: "/user"
		return strings.Trim(e.Value, "\"")

	case *ast.CallExpr:
		// 函数调用: fmt.Sprintf("/%s", enum.AvoidInsuranceFlag)
		return g.extractPathFromFunctionCall(e, typeInfo)

	case *ast.Ident:
		// 变量引用: pathVar
		return g.extractPathFromIdentifier(e, typeInfo)

	case *ast.SelectorExpr:
		// 字段访问: config.BasePath
		return g.extractPathFromSelector(e, typeInfo)

	case *ast.BinaryExpr:
		// 二元表达式: "/api" + "/v1"
		return g.extractPathFromBinaryExpr(e, typeInfo)

	default:
		// 其他未处理的表达式类型，返回占位符
		fmt.Printf("[DEBUG] extractPathFromExpression: 未处理的表达式类型 %T\n", expr)
		return "/dynamic_path"
	}
}

// extractPathFromFunctionCall 从函数调用中提取路径
func (g *GinExtractor) extractPathFromFunctionCall(callExpr *ast.CallExpr, typeInfo *types.Info) string {
	// 检查是否为 fmt.Sprintf 调用
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			if ident.Name == "fmt" && selExpr.Sel.Name == "Sprintf" {
				// 处理 fmt.Sprintf 调用
				return g.extractPathFromSprintfCall(callExpr, typeInfo)
			}
		}
	}

	// 其他函数调用，尝试从类型信息获取
	if typ := typeInfo.TypeOf(callExpr); typ != nil {
		if basic, ok := typ.(*types.Basic); ok && basic.Kind() == types.String {
			return "/dynamic_path"
		}
	}

	return "/function_call"
}

// extractPathFromSprintfCall 从 fmt.Sprintf 调用中提取路径模式
func (g *GinExtractor) extractPathFromSprintfCall(callExpr *ast.CallExpr, typeInfo *types.Info) string {
	if len(callExpr.Args) == 0 {
		return "/sprintf_empty"
	}

	// 获取格式字符串（第一个参数）
	if formatExpr, ok := callExpr.Args[0].(*ast.BasicLit); ok {
		formatStr := strings.Trim(formatExpr.Value, "\"")

		// 如果有更多参数，尝试进行简单的模式识别
		if len(callExpr.Args) > 1 {
			// 对于简单情况，我们可以尝试识别一些常见模式
			// 例如: fmt.Sprintf("/%s", enum.Value) -> "/{param}"
			result := formatStr
			argCount := len(callExpr.Args) - 1 // 减去格式字符串

			// 简单替换 %s, %d 等为占位符
			result = strings.ReplaceAll(result, "%s", "{param}")
			result = strings.ReplaceAll(result, "%d", "{id}")
			result = strings.ReplaceAll(result, "%v", "{value}")

			fmt.Printf("[DEBUG] extractPathFromSprintfCall: 格式='%s', 参数数量=%d, 结果='%s'\n",
				formatStr, argCount, result)

			return result
		}

		return formatStr
	}

	return "/sprintf_complex"
}

// extractPathFromIdentifier 从标识符中提取路径
func (g *GinExtractor) extractPathFromIdentifier(ident *ast.Ident, typeInfo *types.Info) string {
	// 尝试从类型信息获取值
	if obj := typeInfo.ObjectOf(ident); obj != nil {
		if konst, ok := obj.(*types.Const); ok {
			// 常量值
			if konst.Val() != nil {
				if val := konst.Val().String(); val != "" {
					return strings.Trim(val, "\"")
				}
			}
		}

		// 变量名作为路径标识
		return fmt.Sprintf("/{%s}", ident.Name)
	}

	return fmt.Sprintf("/{%s}", ident.Name)
}

// extractPathFromSelector 从选择器表达式中提取路径
func (g *GinExtractor) extractPathFromSelector(selExpr *ast.SelectorExpr, typeInfo *types.Info) string {
	if ident, ok := selExpr.X.(*ast.Ident); ok {
		// 例如: config.BasePath -> "{config.BasePath}"
		return fmt.Sprintf("/{%s.%s}", ident.Name, selExpr.Sel.Name)
	}

	return "/selector_path"
}

// extractPathFromBinaryExpr 从二元表达式中提取路径
func (g *GinExtractor) extractPathFromBinaryExpr(binExpr *ast.BinaryExpr, typeInfo *types.Info) string {
	if binExpr.Op.String() == "+" {
		// 字符串连接
		left := g.extractPathFromExpression(binExpr.X, typeInfo)
		right := g.extractPathFromExpression(binExpr.Y, typeInfo)

		// 如果两边都是简单字符串，直接连接
		if !strings.Contains(left, "{") && !strings.Contains(right, "{") {
			return left + right
		}

		return fmt.Sprintf("%s%s", left, right)
	}

	return "/binary_expr"
}
