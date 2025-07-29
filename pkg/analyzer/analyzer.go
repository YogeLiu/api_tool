// 文件位置: pkg/analyzer/analyzer.go
package analyzer

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"strings"

	"go/types"

	"github.com/YogeLiu/api-tool/pkg/extractor"
	"github.com/YogeLiu/api-tool/pkg/models"
	"github.com/YogeLiu/api-tool/pkg/parser"
	"golang.org/x/tools/go/packages"
)

// Analyzer 核心分析器，执行与框架无关的业务逻辑分析
type Analyzer struct {
	project   *parser.Project
	extractor extractor.Extractor
}

// NewAnalyzer 创建新的分析器实例
func NewAnalyzer(proj *parser.Project, ext extractor.Extractor) *Analyzer {
	return &Analyzer{
		project:   proj,
		extractor: ext,
	}
}

// Analyze 执行主分析流程
func (a *Analyzer) Analyze() (*models.APIInfo, error) {
	// 1. 查找根路由器
	rootRouters := a.extractor.FindRootRouters(a.project.Packages)
	if len(rootRouters) == 0 {
		return nil, &models.AnalysisError{
			Context: "查找根路由器",
			Reason:  fmt.Sprintf("未找到 %s 框架的根路由器", a.extractor.GetFrameworkName()),
		}
	}

	// 2. 初始化任务队列
	var taskQueue []*AnalysisTask
	for _, router := range rootRouters {
		task := &AnalysisTask{
			RouterObject:    router,
			AccumulatedPath: "",
			Parent:          nil,
			TriggeringFunc:  nil,
			VisitedObjects:  make(map[types.Object]bool),
		}
		taskQueue = append(taskQueue, task)
	}

	// 3. 收集所有路由信息
	var routes []models.RouteInfo

	// 执行任务队列循环
	for len(taskQueue) > 0 {
		// 取出队首任务
		currentTask := taskQueue[0]
		taskQueue = taskQueue[1:]

		// 循环检测
		if currentTask.HasCycle(currentTask.RouterObject) {
			continue // 跳过循环的任务
		}
		currentTask.AddVisited(currentTask.RouterObject)

		// 在项目中查找所有对当前路由器对象的使用
		newRoutes, newTasks, err := a.analyzeRouterUsage(currentTask)
		if err != nil {
			return nil, err
		}

		// 添加新发现的路由
		routes = append(routes, newRoutes...)

		// 添加新任务到队列
		taskQueue = append(taskQueue, newTasks...)
	}

	return &models.APIInfo{
		Routes: routes,
	}, nil
}

// analyzeRouterUsage 分析路由器的使用情况
func (a *Analyzer) analyzeRouterUsage(task *AnalysisTask) ([]models.RouteInfo, []*AnalysisTask, error) {
	var routes []models.RouteInfo
	var newTasks []*AnalysisTask

	fmt.Printf("[DEBUG] analyzeRouterUsage: 开始分析路由器对象的使用\n")

	// 遍历所有包，查找对当前路由器对象的调用
	for i, pkg := range a.project.Packages {
		fmt.Printf("[DEBUG] 检查包 %d: %s\n", i, pkg.PkgPath)
		for fileIndex, file := range pkg.Syntax {
			fmt.Printf("[DEBUG] 检查文件 %d\n", fileIndex)
			callCount := 0
			ast.Inspect(file, func(node ast.Node) bool {
				if callExpr, ok := node.(*ast.CallExpr); ok {
					callCount++

					// 检查是否为将路由器作为参数传递的函数调用
					newRouters := a.checkFunctionCall(callExpr, task.RouterObject, pkg.TypesInfo)
					for _, newRouter := range newRouters {
						fmt.Printf("[DEBUG] 创建新任务来跟踪参数对象: %s\n", newRouter.Name())
						newTask := &AnalysisTask{
							RouterObject:    newRouter,
							AccumulatedPath: task.AccumulatedPath,
							Parent:          task,
							TriggeringFunc:  nil,
							VisitedObjects:  make(map[types.Object]bool),
						}
						newTasks = append(newTasks, newTask)
					}

					// 检查调用表达式是否涉及当前路由器对象
					if a.isCallOnObject(callExpr, task.RouterObject, pkg.TypesInfo) {
						fmt.Printf("[DEBUG] 找到对路由器对象的调用\n")
						// 检查是否为路由分组调用
						if isGroup, pathSegment := a.extractor.IsRouteGroupCall(callExpr, pkg.TypesInfo); isGroup {
							fmt.Printf("[DEBUG] 发现路由分组调用，路径段: %s\n", pathSegment)
							// 处理路由分组
							newTask := a.handleRouteGroup(callExpr, task, pathSegment)
							if newTask != nil {
								newTasks = append(newTasks, newTask)
							}
						} else if isHTTP, method, pathSegment := a.extractor.IsHTTPMethodCall(callExpr, pkg.TypesInfo); isHTTP {
							fmt.Printf("[DEBUG] 发现HTTP方法调用: %s %s\n", method, pathSegment)
							// 处理HTTP方法调用
							route := a.handleHTTPMethod(callExpr, task, method, pathSegment, pkg.TypesInfo)
							if route != nil {
								routes = append(routes, *route)
							}
						}
					}
				}
				return true
			})
			fmt.Printf("[DEBUG] 文件中总共检查了 %d 个调用表达式\n", callCount)
		}
	}

	fmt.Printf("[DEBUG] analyzeRouterUsage完成，找到 %d 个路由，%d 个新任务\n", len(routes), len(newTasks))
	return routes, newTasks, nil
}

// isCallOnObject 检查调用表达式是否在指定对象上调用
func (a *Analyzer) isCallOnObject(callExpr *ast.CallExpr, targetObj types.Object, typeInfo *types.Info) bool {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			if obj := typeInfo.ObjectOf(ident); obj != nil {
				fmt.Printf("[DEBUG] isCallOnObject: 检查调用 %s.%s, 目标对象: %s, 当前对象: %s\n",
					ident.Name, selExpr.Sel.Name, targetObj.Name(), obj.Name())
				return obj == targetObj
			} else {
				fmt.Printf("[DEBUG] isCallOnObject: 无法获取对象信息 %s.%s\n", ident.Name, selExpr.Sel.Name)
			}
		} else {
			fmt.Printf("[DEBUG] isCallOnObject: 调用表达式不是简单标识符调用\n")
		}
	} else {
		fmt.Printf("[DEBUG] isCallOnObject: 不是选择器表达式调用\n")
	}
	return false
}

// checkFunctionCall 检查是否为将路由器作为参数传递的函数调用
func (a *Analyzer) checkFunctionCall(callExpr *ast.CallExpr, targetObj types.Object, typeInfo *types.Info) []types.Object {
	var newRouters []types.Object

	// 检查函数调用的参数
	for _, arg := range callExpr.Args {
		if ident, ok := arg.(*ast.Ident); ok {
			if obj := typeInfo.ObjectOf(ident); obj != nil {
				if obj == targetObj {
					fmt.Printf("[DEBUG] 发现路由器作为参数传递给函数调用\n")

					if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
						// 跨包调用，如 health.SetupRouter(r)
						if ident, ok := selExpr.X.(*ast.Ident); ok {
							packageAlias := ident.Name
							functionName := selExpr.Sel.Name
							fmt.Printf("[DEBUG] 发现跨包函数调用: %s.%s\n", packageAlias, functionName)

							// 通用地处理跨包函数调用
							paramObj := a.findParameterObjectInCrossPackageFunction(packageAlias, functionName, 0)
							if paramObj != nil {
								fmt.Printf("[DEBUG] 找到跨包函数 %s.%s 的参数对象: %s\n", packageAlias, functionName, paramObj.Name())
								newRouters = append(newRouters, paramObj)
							}
						}
					} else if ident, ok := callExpr.Fun.(*ast.Ident); ok {
						// 同包内调用，如 InternalRouter(g)
						functionName := ident.Name
						fmt.Printf("[DEBUG] 发现同包内函数调用: %s\n", functionName)

						// 查找函数定义和参数对象
						paramObj := a.findParameterObjectInFunction(functionName, 0) // 假设路由器是第一个参数
						if paramObj != nil {
							fmt.Printf("[DEBUG] 找到函数 %s 的参数对象: %s\n", functionName, paramObj.Name())
							newRouters = append(newRouters, paramObj)
						}
					}
				}
			}
		}
	}

	return newRouters
}

// findParameterObjectInCrossPackageFunction 在跨包函数中查找参数对象
func (a *Analyzer) findParameterObjectInCrossPackageFunction(packageAlias, functionName string, paramIndex int) types.Object {
	fmt.Printf("[DEBUG] findParameterObjectInCrossPackageFunction: 查找 %s.%s 的第 %d 个参数\n", packageAlias, functionName, paramIndex)

	// 根据别名查找实际的包路径
	var targetPackagePath string

	// 遍历所有包来查找import关系
	for _, pkg := range a.project.Packages {
		for _, file := range pkg.Syntax {
			// 检查这个文件的import语句
			for _, imp := range file.Imports {
				if imp.Name != nil && imp.Name.Name == packageAlias {
					// 有显式别名
					targetPackagePath = strings.Trim(imp.Path.Value, "\"")
					fmt.Printf("[DEBUG] 通过别名 %s 找到包路径: %s\n", packageAlias, targetPackagePath)
					break
				} else if imp.Name == nil {
					// 没有别名，使用包名的最后部分
					importPath := strings.Trim(imp.Path.Value, "\"")
					parts := strings.Split(importPath, "/")
					if len(parts) > 0 && parts[len(parts)-1] == packageAlias {
						targetPackagePath = importPath
						fmt.Printf("[DEBUG] 通过包名 %s 找到包路径: %s\n", packageAlias, targetPackagePath)
						break
					}
				}
			}
			if targetPackagePath != "" {
				break
			}
		}
		if targetPackagePath != "" {
			break
		}
	}

	if targetPackagePath == "" {
		fmt.Printf("[DEBUG] 无法找到包 %s 的路径\n", packageAlias)
		return nil
	}

	// 在目标包中查找函数定义
	for _, pkg := range a.project.Packages {
		if pkg.PkgPath == targetPackagePath {
			fmt.Printf("[DEBUG] 找到目标包: %s\n", targetPackagePath)
			return a.findParameterObjectInPackage(pkg, functionName, paramIndex)
		}
	}

	fmt.Printf("[DEBUG] 未找到包: %s\n", targetPackagePath)
	return nil
}

// findParameterObjectInPackage 在指定包中查找函数的参数对象
func (a *Analyzer) findParameterObjectInPackage(pkg *packages.Package, functionName string, paramIndex int) types.Object {
	fmt.Printf("[DEBUG] findParameterObjectInPackage: 在包 %s 中查找函数 %s 的第 %d 个参数\n", pkg.PkgPath, functionName, paramIndex)

	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				if funcDecl.Name.Name == functionName {
					fmt.Printf("[DEBUG] 找到函数定义: %s\n", functionName)

					// 检查参数列表
					if funcDecl.Type.Params != nil && len(funcDecl.Type.Params.List) > paramIndex {
						paramField := funcDecl.Type.Params.List[paramIndex]
						if len(paramField.Names) > 0 {
							paramName := paramField.Names[0]
							if obj := pkg.TypesInfo.ObjectOf(paramName); obj != nil {
								fmt.Printf("[DEBUG] 找到参数对象: %s\n", paramName.Name)
								return obj
							}
						}
					}
				}
			}
		}
	}

	return nil
}

// // findLoadFunction 查找routers包中的Load函数
// func (a *Analyzer) findLoadFunction() *ast.FuncDecl {
// 	for _, pkg := range a.project.Packages {
// 		if strings.HasSuffix(pkg.PkgPath, "/routers") {
// 			for _, file := range pkg.Syntax {
// 				for _, decl := range file.Decls {
// 					if funcDecl, ok := decl.(*ast.FuncDecl); ok {
// 						if funcDecl.Name.Name == "Load" {
// 							return funcDecl
// 						}
// 					}
// 				}
// 			}
// 		}
// 	}
// 	return nil
// }

// findParameterObjectInRoutersPackage 在routers包中查找指定名称的参数对象
// func (a *Analyzer) findParameterObjectInRoutersPackage(paramName string) types.Object {
// 	for _, pkg := range a.project.Packages {
// 		if strings.HasSuffix(pkg.PkgPath, "/routers") {
// 			for _, file := range pkg.Syntax {
// 				for _, decl := range file.Decls {
// 					if funcDecl, ok := decl.(*ast.FuncDecl); ok {
// 						if funcDecl.Name.Name == "Load" && funcDecl.Type.Params != nil {
// 							for _, param := range funcDecl.Type.Params.List {
// 								for _, name := range param.Names {
// 									if name.Name == paramName {
// 										if obj := pkg.TypesInfo.ObjectOf(name); obj != nil {
// 											return obj
// 										}
// 									}
// 								}
// 							}
// 						}
// 					}
// 				}
// 			}
// 		}
// 	}
// 	return nil
// }

// findParameterObjectInFunction 在指定函数中查找指定索引的参数对象
func (a *Analyzer) findParameterObjectInFunction(functionName string, paramIndex int) types.Object {
	fmt.Printf("[DEBUG] findParameterObjectInFunction: 查找函数 %s 的第 %d 个参数\n", functionName, paramIndex)

	// 在所有包中查找函数定义
	for _, pkg := range a.project.Packages {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Name.Name == functionName {
						fmt.Printf("[DEBUG] 找到函数定义: %s 在包 %s\n", functionName, pkg.PkgPath)

						// 检查参数列表
						if funcDecl.Type.Params != nil && len(funcDecl.Type.Params.List) > paramIndex {
							paramField := funcDecl.Type.Params.List[paramIndex]
							if len(paramField.Names) > 0 {
								paramName := paramField.Names[0]
								if obj := pkg.TypesInfo.ObjectOf(paramName); obj != nil {
									fmt.Printf("[DEBUG] 找到参数对象: %s\n", paramName.Name)
									return obj
								}
							}
						}
					}
				}
			}
		}
	}

	fmt.Printf("[DEBUG] 未找到函数 %s 的参数对象\n", functionName)
	return nil
}

// handleRouteGroup 处理路由分组调用
func (a *Analyzer) handleRouteGroup(callExpr *ast.CallExpr, parentTask *AnalysisTask, pathSegment string) *AnalysisTask {
	fmt.Printf("[DEBUG] handleRouteGroup: 开始处理路由分组, 路径段: %s\n", pathSegment)

	// 创建新的分析任务
	newPath := a.combinePaths(parentTask.AccumulatedPath, pathSegment)
	fmt.Printf("[DEBUG] handleRouteGroup: 组合后的路径: %s\n", newPath)

	// 查找分组调用的结果对象
	// 这里我们需要找到接收Group()调用结果的变量
	groupObj := a.findGroupResultObject(callExpr)
	if groupObj == nil {
		fmt.Printf("[DEBUG] handleRouteGroup: 未找到分组结果对象\n")
		return nil
	}

	fmt.Printf("[DEBUG] handleRouteGroup: 找到分组结果对象: %s\n", groupObj.Name())

	newTask := &AnalysisTask{
		RouterObject:    groupObj,
		AccumulatedPath: newPath,
		Parent:          parentTask,
		TriggeringFunc:  nil,
		VisitedObjects:  make(map[types.Object]bool),
	}

	fmt.Printf("[DEBUG] handleRouteGroup: 创建新任务成功\n")
	return newTask
}

// handleHTTPMethod 处理HTTP方法调用
func (a *Analyzer) handleHTTPMethod(callExpr *ast.CallExpr, task *AnalysisTask, method, pathSegment string, typeInfo *types.Info) *models.RouteInfo {
	fmt.Printf("[DEBUG] handleHTTPMethod: 开始处理HTTP方法, 方法: %s, 路径段: %s\n", method, pathSegment)

	// 组合完整路径
	fullPath := a.combinePaths(task.AccumulatedPath, pathSegment)
	fmt.Printf("[DEBUG] handleHTTPMethod: 完整路径: %s\n", fullPath)

	// 提取处理函数
	handlerFunc := a.extractHandlerFunction(callExpr, typeInfo)
	if handlerFunc == nil {
		fmt.Printf("[DEBUG] handleHTTPMethod: 未找到处理函数\n")
		return nil
	}

	fmt.Printf("[DEBUG] handleHTTPMethod: 找到处理函数: %s\n", handlerFunc.Name.Name)

	// 提取请求和响应信息
	request := a.extractor.ExtractRequest(handlerFunc, typeInfo, a.resolveType)
	response := a.extractor.ExtractResponse(handlerFunc, typeInfo, a.resolveType)

	// 创建路由信息
	route := &models.RouteInfo{
		Method:   method,
		Path:     fullPath,
		Handler:  handlerFunc.Name.Name,
		Request:  request,
		Response: response,
	}

	fmt.Printf("[DEBUG] handleHTTPMethod: 创建路由成功: %s %s -> %s\n", method, fullPath, handlerFunc.Name.Name)
	return route
}

// combinePaths 组合路径段
func (a *Analyzer) combinePaths(basePath, segment string) string {
	if basePath == "" {
		return segment
	}
	if segment == "" {
		return basePath
	}

	// 确保路径以/开头
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if !strings.HasPrefix(segment, "/") {
		segment = "/" + segment
	}

	// 清理路径
	return filepath.Join(basePath, segment)
}

// extractHandlerFunction 提取处理函数
func (a *Analyzer) extractHandlerFunction(callExpr *ast.CallExpr, typeInfo *types.Info) *ast.FuncDecl {
	fmt.Printf("[DEBUG] extractHandlerFunction: 开始提取处理函数，参数数量: %d\n", len(callExpr.Args))

	// 查找处理函数参数（通常是最后一个参数）
	if len(callExpr.Args) == 0 {
		fmt.Printf("[DEBUG] extractHandlerFunction: 没有参数\n")
		return nil
	}

	// 获取最后一个参数（通常是处理函数）
	lastArg := callExpr.Args[len(callExpr.Args)-1]
	fmt.Printf("[DEBUG] extractHandlerFunction: 最后一个参数类型: %T\n", lastArg)

	// 如果是标识符，查找对应的函数声明
	if ident, ok := lastArg.(*ast.Ident); ok {
		fmt.Printf("[DEBUG] extractHandlerFunction: 参数是标识符: %s\n", ident.Name)
		if obj := typeInfo.ObjectOf(ident); obj != nil {
			fmt.Printf("[DEBUG] extractHandlerFunction: 找到对象: %s\n", obj.Name())
			// 在项目中查找函数声明
			funcDecl := a.findFunctionDeclaration(obj.Name())
			if funcDecl != nil {
				fmt.Printf("[DEBUG] extractHandlerFunction: 找到函数声明: %s\n", funcDecl.Name.Name)
			}
			return funcDecl
		}
	}

	// 如果是选择器表达式，如 api.Create
	if selExpr, ok := lastArg.(*ast.SelectorExpr); ok {
		fmt.Printf("[DEBUG] extractHandlerFunction: 参数是选择器表达式\n")
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			packageName := ident.Name
			functionName := selExpr.Sel.Name
			fmt.Printf("[DEBUG] extractHandlerFunction: 选择器表达式 %s.%s\n", packageName, functionName)

			// 在指定包中查找函数声明
			funcDecl := a.findFunctionInPackage(packageName, functionName)
			if funcDecl != nil {
				fmt.Printf("[DEBUG] extractHandlerFunction: 在包 %s 中找到函数: %s\n", packageName, functionName)
			} else {
				fmt.Printf("[DEBUG] extractHandlerFunction: 在包 %s 中未找到函数: %s\n", packageName, functionName)
			}
			return funcDecl
		}
	}

	// 如果是函数字面量
	if funcLit, ok := lastArg.(*ast.FuncLit); ok {
		fmt.Printf("[DEBUG] extractHandlerFunction: 参数是函数字面量\n")
		// 创建一个临时的函数声明
		funcDecl := &ast.FuncDecl{
			Name: &ast.Ident{Name: "anonymous"},
			Type: funcLit.Type,
			Body: funcLit.Body,
		}
		return funcDecl
	}

	fmt.Printf("[DEBUG] extractHandlerFunction: 无法识别的参数类型\n")
	return nil
}

// findGroupResultObject 查找分组调用的结果对象
func (a *Analyzer) findGroupResultObject(callExpr *ast.CallExpr) types.Object {
	fmt.Printf("[DEBUG] findGroupResultObject: 开始查找分组结果对象\n")

	// 查找包含这个调用表达式的赋值语句
	// 我们需要向上遍历AST来找到赋值的目标
	for _, pkg := range a.project.Packages {
		// 移除硬编码的路径限制，在所有包中查找
		for _, file := range pkg.Syntax {
			var foundObj types.Object
			// 查找所有赋值语句
			ast.Inspect(file, func(node ast.Node) bool {
				if foundObj != nil {
					return false // 已经找到，停止遍历
				}

				if assignStmt, ok := node.(*ast.AssignStmt); ok {
					// 检查右侧是否包含我们的调用表达式
					for i, rhs := range assignStmt.Rhs {
						if rhs == callExpr {
							// 找到了！获取左侧的变量
							if i < len(assignStmt.Lhs) {
								if ident, ok := assignStmt.Lhs[i].(*ast.Ident); ok {
									if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
										fmt.Printf("[DEBUG] findGroupResultObject: 找到赋值目标变量: %s\n", ident.Name)
										foundObj = obj
										return false
									}
								}
							}
						}
					}
				}

				// 查找变量声明
				if genDecl, ok := node.(*ast.GenDecl); ok {
					for _, spec := range genDecl.Specs {
						if valueSpec, ok := spec.(*ast.ValueSpec); ok {
							for i, value := range valueSpec.Values {
								if value == callExpr {
									if i < len(valueSpec.Names) {
										if obj := pkg.TypesInfo.ObjectOf(valueSpec.Names[i]); obj != nil {
											fmt.Printf("[DEBUG] findGroupResultObject: 找到声明变量: %s\n", valueSpec.Names[i].Name)
											foundObj = obj
											return false
										}
									}
								}
							}
						}
					}
				}
				return true
			})

			if foundObj != nil {
				return foundObj
			}
		}
	}

	fmt.Printf("[DEBUG] findGroupResultObject: 未找到分组结果对象\n")
	return nil
}

// findFunctionDeclaration 在项目中查找函数声明
func (a *Analyzer) findFunctionDeclaration(funcName string) *ast.FuncDecl {
	for _, pkg := range a.project.Packages {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Name.Name == funcName {
						return funcDecl
					}
				}
			}
		}
	}
	return nil
}

// findFunctionInPackage 在指定包中查找函数声明
func (a *Analyzer) findFunctionInPackage(packageName, functionName string) *ast.FuncDecl {
	for _, pkg := range a.project.Packages {
		// 查找匹配的包（可能是别名导入）
		for _, file := range pkg.Syntax {
			for _, imp := range file.Imports {
				// 检查是否有别名
				if imp.Name != nil && imp.Name.Name == packageName {
					// 找到别名匹配的包，在该包中查找函数
					return a.findFunctionInImportedPackage(imp.Path.Value, functionName)
				}
			}

			// 如果没有别名，检查是否是包的最后部分
			if strings.HasSuffix(pkg.PkgPath, "/"+packageName) ||
				strings.HasSuffix(pkg.PkgPath, packageName) {
				return a.findFunctionDeclarationInPackage(pkg, functionName)
			}
		}
	}

	// 如果在当前项目中没找到，可能是外部包，创建一个占位符
	fmt.Printf("[DEBUG] findFunctionInPackage: 创建占位符函数 %s.%s\n", packageName, functionName)
	return &ast.FuncDecl{
		Name: &ast.Ident{Name: functionName},
		Type: &ast.FuncType{},
		Body: &ast.BlockStmt{},
	}
}

// findFunctionInImportedPackage 在导入的包中查找函数
func (a *Analyzer) findFunctionInImportedPackage(importPath, functionName string) *ast.FuncDecl {
	// 清理导入路径的引号
	importPath = strings.Trim(importPath, "\"")

	for _, pkg := range a.project.Packages {
		if pkg.PkgPath == importPath {
			return a.findFunctionDeclarationInPackage(pkg, functionName)
		}
	}
	return nil
}

// findFunctionDeclarationInPackage 在特定包中查找函数声明
func (a *Analyzer) findFunctionDeclarationInPackage(pkg *packages.Package, functionName string) *ast.FuncDecl {
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				if funcDecl.Name.Name == functionName {
					return funcDecl
				}
			}
		}
	}
	return nil
}
