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

// IrisExtractor 实现了针对Iris框架的API提取逻辑
type IrisExtractor struct {
	project *parser.Project
}

// GetFrameworkName 返回框架名称
func (i *IrisExtractor) GetFrameworkName() string {
	return "iris"
}

// InitializeAnalysis 初始化分析器（Iris提取器暂未实现预扫描）
func (i *IrisExtractor) InitializeAnalysis() error {
	// Iris提取器暂未实现响应函数预扫描功能
	return nil
}

// FindRootRouters 查找iris.Application类型的根路由器
func (i *IrisExtractor) FindRootRouters(pkgs []*packages.Package) []types.Object {
	var routers []types.Object

	fmt.Printf("[DEBUG] IrisExtractor.FindRootRouters: 开始查找，共有 %d 个包\n", len(pkgs))

	for _, pkg := range pkgs {
		fmt.Printf("[DEBUG] 处理包: %s (包含 %d 个语法文件)\n", pkg.PkgPath, len(pkg.Syntax))
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
									if i.IsIrisApplication(obj.Type()) {
										fmt.Printf("[DEBUG] 找到Iris Application变量: %s\n", name.Name)
										routers = append(routers, obj)
									}
								}
							}
						}
					}
				}

				// 查找函数参数中的路由器
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Name != nil {
						fmt.Printf("[DEBUG] 检查函数: %s\n", funcDecl.Name.Name)
					}

					// 检查函数参数
					if funcDecl.Type.Params != nil {
						for _, param := range funcDecl.Type.Params.List {
							for _, name := range param.Names {
								if obj := pkg.TypesInfo.ObjectOf(name); obj != nil {
									fmt.Printf("[DEBUG] 检查函数参数 %s, 类型: %s\n", name.Name, obj.Type().String())
									if i.IsIrisApplication(obj.Type()) {
										fmt.Printf("[DEBUG] 找到Iris路由器参数: %s (函数: %s)\n", name.Name, funcDecl.Name.Name)
										routers = append(routers, obj)
									}
								}
							}
						}
					}

					// 查找函数中的变量赋值和iris.New()调用
					ast.Inspect(funcDecl, func(node ast.Node) bool {
						switch n := node.(type) {
						case *ast.AssignStmt:
							// 查找赋值语句
							for _, rhs := range n.Rhs {
								if callExpr, ok := rhs.(*ast.CallExpr); ok {
									if i.isIrisNewCall(callExpr, pkg.TypesInfo) {
										fmt.Printf("[DEBUG] 确认为iris.New()调用\n")
										// 这是iris.New()调用
										for _, lhs := range n.Lhs {
											if ident, ok := lhs.(*ast.Ident); ok {
												if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
													fmt.Printf("[DEBUG] 找到iris.New()调用结果变量: %s\n", ident.Name)
													routers = append(routers, obj)
												}
											}
										}
									}
								}
							}
						case *ast.ValueSpec:
							// 查找变量声明中的iris.New()调用
							for idx, value := range n.Values {
								if callExpr, ok := value.(*ast.CallExpr); ok {
									if i.isIrisNewCall(callExpr, pkg.TypesInfo) {
										fmt.Printf("[DEBUG] 在变量声明中找到iris.New()调用\n")
										if idx < len(n.Names) {
											if obj := pkg.TypesInfo.ObjectOf(n.Names[idx]); obj != nil {
												fmt.Printf("[DEBUG] 找到iris.New()声明变量: %s\n", n.Names[idx].Name)
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

// IsIrisApplication 检查类型是否为iris.Application或相关类型
func (i *IrisExtractor) IsIrisApplication(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			pkgPath := obj.Pkg().Path()
			typeName := obj.Name()

			// 支持多种iris包路径和相关类型
			// 1. 主包中的类型
			if (pkgPath == "github.com/kataras/iris" || pkgPath == "github.com/kataras/iris/v12") &&
				(typeName == "Application" || typeName == "APIBuilder") {
				return true
			}

			// 2. core/router包中的类型
			if (pkgPath == "github.com/kataras/iris/core/router" || pkgPath == "github.com/kataras/iris/v12/core/router") &&
				(typeName == "Party" || typeName == "APIBuilder") {
				return true
			}
		}
	}

	return false
}

// isIrisNewCall 检查是否为iris.New()调用
func (i *IrisExtractor) isIrisNewCall(callExpr *ast.CallExpr, typeInfo *types.Info) bool {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			// 检查包名是否为iris
			if ident.Name == "iris" {
				// 检查方法名
				methodName := selExpr.Sel.Name
				if methodName == "New" {
					return true
				}
			}
		}
	}
	return false
}

// IsRouteGroupCall 检查是否为路由分组调用
func (i *IrisExtractor) IsRouteGroupCall(callExpr *ast.CallExpr, typeInfo *types.Info) (bool, string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if selExpr.Sel.Name == "Party" {
			if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
				if i.IsIrisParty(typ) {
					if len(callExpr.Args) > 0 {
						path := i.extractPathFromExpression(callExpr.Args[0], typeInfo)
						return true, path
					}
				}
			}
		}
	}
	return false, ""
}

// IsIrisParty 检查类型是否为iris相关的路由器类型
func (i *IrisExtractor) IsIrisParty(typ types.Type) bool {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			pkgPath := obj.Pkg().Path()
			typeName := obj.Name()

			// 支持多种iris包路径和相关类型
			// 1. 主包中的类型
			if (pkgPath == "github.com/kataras/iris" || pkgPath == "github.com/kataras/iris/v12") &&
				(typeName == "Application" || typeName == "Party" || typeName == "APIBuilder") {
				return true
			}

			// 2. core/router包中的类型
			if (pkgPath == "github.com/kataras/iris/core/router" || pkgPath == "github.com/kataras/iris/v12/core/router") &&
				(typeName == "Party" || typeName == "APIBuilder") {
				return true
			}

			fmt.Printf("[DEBUG] isIrisParty: 检查类型 %s.%s\n", pkgPath, typeName)
		}
	}

	return false
}

// IsHTTPMethodCall 检查是否为HTTP方法调用
func (i *IrisExtractor) IsHTTPMethodCall(callExpr *ast.CallExpr, typeInfo *types.Info) (bool, string, string) {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		methodName := selExpr.Sel.Name

		// 检查调用者类型是否为iris相关类型
		if typ := typeInfo.TypeOf(selExpr.X); typ != nil {
			if i.IsIrisParty(typ) {
				// Iris HTTP方法名：Get, Post, Put, Delete, Patch, Options, Head, Any
				var httpMethod string
				switch methodName {
				case "Get":
					httpMethod = "GET"
				case "Post":
					httpMethod = "POST"
				case "Put":
					httpMethod = "PUT"
				case "Delete":
					httpMethod = "DELETE"
				case "Patch":
					httpMethod = "PATCH"
				case "Options":
					httpMethod = "OPTIONS"
				case "Head":
					httpMethod = "HEAD"
				case "Any":
					httpMethod = "ANY"
				default:
					return false, "", ""
				}

				// 提取路径参数
				var path string
				if len(callExpr.Args) > 0 {
					path = i.extractPathFromExpression(callExpr.Args[0], typeInfo)
				}

				fmt.Printf("[DEBUG] IsHTTPMethodCall (Iris): 找到HTTP方法调用 %s %s\n", httpMethod, path)
				return true, httpMethod, path
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
				return obj.Pkg().Path() == "github.com/kataras/iris" && obj.Name() == "Context"
			}
		}
	}
	return false
}

// ExtractPathFromCall 从调用表达式中提取路径
func (i *IrisExtractor) ExtractPathFromCall(callExpr *ast.CallExpr) string {
	if len(callExpr.Args) > 0 {
		if pathArg, ok := callExpr.Args[0].(*ast.BasicLit); ok {
			// 去除引号
			path := strings.Trim(pathArg.Value, "\"")
			return path
		}
	}
	return ""
}

// FindRouterGroupFunctions 查找所有接受Iris路由器参数的函数（路由分组函数）
func (i *IrisExtractor) FindRouterGroupFunctions(pkgs []*packages.Package) map[string]*models.RouterGroupFunction {
	routerGroupFunctions := make(map[string]*models.RouterGroupFunction)

	fmt.Printf("[DEBUG] IrisExtractor.FindRouterGroupFunctions: 开始查找路由分组函数，共有 %d 个包\n", len(pkgs))

	for _, pkg := range pkgs {
		fmt.Printf("[DEBUG] 检查包: %s\n", pkg.PkgPath)
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Type.Params != nil {
						// 检查每个参数是否为路由器类型
						for idx, param := range funcDecl.Type.Params.List {
							if i.IsRouterParameter(param, pkg.TypesInfo) {
								uniqueKey := pkg.PkgPath + "+" + funcDecl.Name.Name
								fmt.Printf("[DEBUG] 找到路由分组函数: %s (参数索引: %d)\n", uniqueKey, idx)

								routerGroupFunctions[uniqueKey] = &models.RouterGroupFunction{
									PackagePath:    pkg.PkgPath,
									FunctionName:   funcDecl.Name.Name,
									FuncDecl:       funcDecl,
									Package:        pkg,
									RouterParamIdx: idx,
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

// IsRouterParameter 检查函数参数是否为Iris路由器类型
func (i *IrisExtractor) IsRouterParameter(param *ast.Field, typeInfo *types.Info) bool {
	if param.Type != nil {
		// 获取参数类型
		if typ := typeInfo.TypeOf(param.Type); typ != nil {
			// 检查是否为Iris路由器相关类型
			return i.IsIrisParty(typ)
		}
	}
	return false
}

// extractPathFromExpression 从表达式中提取路径，支持多种表达式类型
func (i *IrisExtractor) extractPathFromExpression(expr ast.Expr, typeInfo *types.Info) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		// 字符串字面量: "/user"
		return strings.Trim(e.Value, "\"")

	case *ast.CallExpr:
		// 函数调用: fmt.Sprintf("/%s", enum.AvoidInsuranceFlag)
		return i.extractPathFromFunctionCall(e, typeInfo)

	case *ast.Ident:
		// 变量引用: pathVar
		return i.extractPathFromIdentifier(e, typeInfo)

	case *ast.SelectorExpr:
		// 字段访问: config.BasePath
		return i.extractPathFromSelector(e, typeInfo)

	case *ast.BinaryExpr:
		// 二元表达式: "/api" + "/v1"
		return i.extractPathFromBinaryExpr(e, typeInfo)

	default:
		// 其他未处理的表达式类型，返回占位符
		fmt.Printf("[DEBUG] extractPathFromExpression: 未处理的表达式类型 %T\n", expr)
		return "/dynamic_path"
	}
}

// extractPathFromFunctionCall 从函数调用中提取路径
func (i *IrisExtractor) extractPathFromFunctionCall(callExpr *ast.CallExpr, typeInfo *types.Info) string {
	// 检查是否为 fmt.Sprintf 调用
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			if ident.Name == "fmt" && selExpr.Sel.Name == "Sprintf" {
				// 处理 fmt.Sprintf 调用
				return i.extractPathFromSprintfCall(callExpr, typeInfo)
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
func (i *IrisExtractor) extractPathFromSprintfCall(callExpr *ast.CallExpr, typeInfo *types.Info) string {
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
func (i *IrisExtractor) extractPathFromIdentifier(ident *ast.Ident, typeInfo *types.Info) string {
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
func (i *IrisExtractor) extractPathFromSelector(selExpr *ast.SelectorExpr, typeInfo *types.Info) string {
	if ident, ok := selExpr.X.(*ast.Ident); ok {
		// 例如: config.BasePath -> "{config.BasePath}"
		return fmt.Sprintf("/{%s.%s}", ident.Name, selExpr.Sel.Name)
	}

	return "/selector_path"
}

// extractPathFromBinaryExpr 从二元表达式中提取路径
func (i *IrisExtractor) extractPathFromBinaryExpr(binExpr *ast.BinaryExpr, typeInfo *types.Info) string {
	if binExpr.Op.String() == "+" {
		// 字符串连接
		left := i.extractPathFromExpression(binExpr.X, typeInfo)
		right := i.extractPathFromExpression(binExpr.Y, typeInfo)

		// 如果两边都是简单字符串，直接连接
		if !strings.Contains(left, "{") && !strings.Contains(right, "{") {
			return left + right
		}

		return fmt.Sprintf("%s%s", left, right)
	}

	return "/binary_expr"
}
