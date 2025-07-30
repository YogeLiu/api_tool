// 文件位置: pkg/analyzer/analyzer.go
package analyzer

import (
	"fmt"
	"go/ast"
	"os"
	"strings"

	"go/types"

	"path/filepath"

	"github.com/YogeLiu/api-tool/pkg/extractor"
	"github.com/YogeLiu/api-tool/pkg/models"
	"github.com/YogeLiu/api-tool/pkg/parser"
	"golang.org/x/tools/go/packages"
)

// Analyzer 核心分析器，执行与框架无关的业务逻辑分析
type Analyzer struct {
	project              *parser.Project
	extractor            extractor.Extractor
	routeCache           map[string]bool                        // 路由去重映射
	routerGroupFunctions map[string]*models.RouterGroupFunction // 路由分组函数索引
}

// RouteContext 路由解析上下文
type RouteContext struct {
	ParentPath     string            // 累积的父级路径
	RouterObject   types.Object      // 当前路由器对象
	VisitedFuncs   map[string]bool   // 已访问的函数，防止循环调用
	CallingPackage *packages.Package // 调用的包
}

// NewAnalyzer 创建新的分析器实例
func NewAnalyzer(proj *parser.Project, ext extractor.Extractor) *Analyzer {
	return &Analyzer{
		project:              proj,
		extractor:            ext,
		routeCache:           make(map[string]bool),
		routerGroupFunctions: make(map[string]*models.RouterGroupFunction),
	}
}

// Analyze 执行主分析流程
func (a *Analyzer) Analyze() (*models.APIInfo, error) {
	fmt.Printf("[DEBUG] 开始两阶段路由分析\n")

	// 预处理阶段：初始化提取器，进行预扫描
	fmt.Printf("[DEBUG] === 预处理阶段：初始化提取器 ===\n")
	if err := a.extractor.InitializeAnalysis(); err != nil {
		return nil, &models.AnalysisError{
			Context: "初始化提取器",
			Reason:  fmt.Sprintf("提取器初始化失败: %v", err),
		}
	}

	// 第一阶段：扫描并索引所有路由分组函数
	fmt.Printf("[DEBUG] === 第一阶段：索引路由分组函数 ===\n")
	a.routerGroupFunctions = a.extractor.FindRouterGroupFunctions(a.project.Packages)
	fmt.Printf("[DEBUG] 索引完成，找到 %d 个路由分组函数:\n", len(a.routerGroupFunctions))
	for key := range a.routerGroupFunctions {
		fmt.Printf("[DEBUG]   - %s\n", key)
	}

	// 第二阶段：从根路由开始递归解析
	fmt.Printf("[DEBUG] === 第二阶段：递归解析路由 ===\n")
	rootRouters := a.extractor.FindRootRouters(a.project.Packages)
	if len(rootRouters) == 0 {
		return nil, &models.AnalysisError{
			Context: "查找根路由器",
			Reason:  fmt.Sprintf("未找到 %s 框架的根路由器", a.extractor.GetFrameworkName()),
		}
	}

	var routes []models.RouteInfo

	// 为每个根路由器开始递归解析
	for _, rootRouter := range rootRouters {
		fmt.Printf("[DEBUG] 开始分析根路由器: %s\n", rootRouter.Name())
		context := &RouteContext{
			ParentPath:     "",
			RouterObject:   rootRouter,
			VisitedFuncs:   make(map[string]bool),
			CallingPackage: nil, // 根路由器没有调用包
		}

		foundRoutes := a.analyzeRouterRecursively(context)
		routes = append(routes, foundRoutes...)
	}

	// 保存调试信息
	debugFilePath := "api_routes.debug"
	err := a.saveRoutesToDebugFile(routes, debugFilePath)
	if err != nil {
		fmt.Printf("[DEBUG] 保存路由信息到debug文件失败: %v\n", err)
	} else {
		fmt.Printf("[DEBUG] 路由信息已保存到 %s\n", debugFilePath)
	}

	fmt.Printf("[DEBUG] 分析完成，总共找到 %d 个路由\n", len(routes))
	return &models.APIInfo{
		Routes: routes,
	}, nil
}

// analyzeRouterRecursively 递归解析路由器对象的使用
func (a *Analyzer) analyzeRouterRecursively(context *RouteContext) []models.RouteInfo {
	var routes []models.RouteInfo

	fmt.Printf("[DEBUG] analyzeRouterRecursively: 分析路由器 %s，当前路径: %s\n",
		context.RouterObject.Name(), context.ParentPath)

	// 遍历所有包，查找对当前路由器对象的使用
	for _, pkg := range a.project.Packages {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(node ast.Node) bool {
				if callExpr, ok := node.(*ast.CallExpr); ok {
					// 检查是否为对当前路由器对象的调用
					if a.isCallOnRouter(callExpr, context.RouterObject, pkg.TypesInfo) {
						// 检查是否为路由分组调用
						if isGroup, pathSegment := a.extractor.IsRouteGroupCall(callExpr, pkg.TypesInfo); isGroup {
							fmt.Printf("[DEBUG] 发现路由分组调用: %s\n", pathSegment)
							newRoutes := a.handleRouteGroupCall(callExpr, context, pathSegment, pkg)
							routes = append(routes, newRoutes...)
						} else if isHTTP, method, pathSegment := a.extractor.IsHTTPMethodCall(callExpr, pkg.TypesInfo); isHTTP {
							fmt.Printf("[DEBUG] 发现HTTP方法调用: %s %s\n", method, pathSegment)
							route := a.handleHTTPMethodCall(callExpr, context, method, pathSegment, pkg.TypesInfo)
							if route != nil {
								routeKey := fmt.Sprintf("%s:%s:%s", route.Method, route.Path, route.Handler)
								if !a.routeCache[routeKey] {
									a.routeCache[routeKey] = true
									routes = append(routes, *route)
									fmt.Printf("[DEBUG] 添加路由: %s %s -> %s\n", route.Method, route.Path, route.Handler)
								}
							}
						}
					}

					// 检查是否为路由分组函数调用
					routerGroupRoutes := a.checkRouterGroupFunctionCall(callExpr, context, pkg)
					routes = append(routes, routerGroupRoutes...)
				}
				return true
			})
		}
	}

	return routes
}

// checkRouterGroupFunctionCall 检查是否为路由分组函数调用
func (a *Analyzer) checkRouterGroupFunctionCall(callExpr *ast.CallExpr, context *RouteContext, pkg *packages.Package) []models.RouteInfo {
	var routes []models.RouteInfo

	// 检查是否为函数调用，且传递了当前路由器对象作为参数
	for _, arg := range callExpr.Args {
		if a.isRouterArgument(arg, context.RouterObject, pkg.TypesInfo) {
			// 找到路由分组函数调用
			funcKey := a.getFunctionCallKey(callExpr, pkg)
			if funcKey != "" {
				// 检查是否在循环调用
				if context.VisitedFuncs[funcKey] {
					fmt.Printf("[DEBUG] 检测到循环调用，跳过: %s\n", funcKey)
					continue
				}

				// 查找对应的路由分组函数
				if rgf, exists := a.routerGroupFunctions[funcKey]; exists {
					fmt.Printf("[DEBUG] 找到路由分组函数调用: %s\n", funcKey)

					// 创建新的上下文，递归解析路由分组函数
					newContext := &RouteContext{
						ParentPath:     context.ParentPath,
						RouterObject:   a.getRouterParameterObject(rgf),
						VisitedFuncs:   a.copyVisitedFuncs(context.VisitedFuncs),
						CallingPackage: pkg,
					}
					newContext.VisitedFuncs[funcKey] = true

					// 递归解析路由分组函数内部的路由
					nestedRoutes := a.analyzeRouterGroupFunction(rgf, newContext)
					routes = append(routes, nestedRoutes...)
				}
			}
		}
	}

	return routes
}

// analyzeRouterGroupFunction 分析路由分组函数内部的路由定义
func (a *Analyzer) analyzeRouterGroupFunction(rgf *models.RouterGroupFunction, context *RouteContext) []models.RouteInfo {
	var routes []models.RouteInfo

	fmt.Printf("[DEBUG] analyzeRouterGroupFunction: 分析函数 %s\n", rgf.UniqueKey)

	// 分析函数体中的路由定义
	if rgf.FuncDecl.Body != nil {
		ast.Inspect(rgf.FuncDecl.Body, func(node ast.Node) bool {
			if callExpr, ok := node.(*ast.CallExpr); ok {
				// 检查是否为对路由器参数的调用
				if a.isCallOnRouter(callExpr, context.RouterObject, rgf.Package.TypesInfo) {
					// 检查是否为路由分组调用
					if isGroup, pathSegment := a.extractor.IsRouteGroupCall(callExpr, rgf.Package.TypesInfo); isGroup {
						fmt.Printf("[DEBUG] 在路由分组函数中发现子分组: %s\n", pathSegment)
						newRoutes := a.handleRouteGroupCall(callExpr, context, pathSegment, rgf.Package)
						routes = append(routes, newRoutes...)
					} else if isHTTP, method, pathSegment := a.extractor.IsHTTPMethodCall(callExpr, rgf.Package.TypesInfo); isHTTP {
						fmt.Printf("[DEBUG] 在路由分组函数中发现HTTP方法: %s %s\n", method, pathSegment)
						route := a.handleHTTPMethodCall(callExpr, context, method, pathSegment, rgf.Package.TypesInfo)
						if route != nil {
							routeKey := fmt.Sprintf("%s:%s:%s", route.Method, route.Path, route.Handler)
							if !a.routeCache[routeKey] {
								a.routeCache[routeKey] = true
								routes = append(routes, *route)
								fmt.Printf("[DEBUG] 添加路由: %s %s -> %s\n", route.Method, route.Path, route.Handler)
							}
						}
					}
				}

				// 检查嵌套的路由分组函数调用
				nestedRoutes := a.checkRouterGroupFunctionCall(callExpr, context, rgf.Package)
				routes = append(routes, nestedRoutes...)
			}
			return true
		})
	}

	return routes
}

// handleRouteGroupCall 处理路由分组调用
func (a *Analyzer) handleRouteGroupCall(callExpr *ast.CallExpr, context *RouteContext, pathSegment string, pkg *packages.Package) []models.RouteInfo {
	var routes []models.RouteInfo

	// 组合新的路径
	newPath := a.combinePaths(context.ParentPath, pathSegment)
	fmt.Printf("[DEBUG] handleRouteGroupCall: 新路径 %s\n", newPath)

	// 查找分组调用的结果对象
	groupObj := a.findGroupResultObject(callExpr, pkg)
	if groupObj == nil {
		fmt.Printf("[DEBUG] 未找到分组结果对象\n")
		return routes
	}

	// 创建新的上下文继续递归
	newContext := &RouteContext{
		ParentPath:     newPath,
		RouterObject:   groupObj,
		VisitedFuncs:   context.VisitedFuncs, // 共享访问记录
		CallingPackage: pkg,
	}

	nestedRoutes := a.analyzeRouterRecursively(newContext)
	routes = append(routes, nestedRoutes...)

	return routes
}

// handleHTTPMethodCall 处理HTTP方法调用
func (a *Analyzer) handleHTTPMethodCall(callExpr *ast.CallExpr, context *RouteContext, method, pathSegment string, typeInfo *types.Info) *models.RouteInfo {
	// 组合完整路径
	fullPath := a.combinePaths(context.ParentPath, pathSegment)
	fmt.Printf("[DEBUG] handleHTTPMethodCall: 完整路径: %s\n", fullPath)

	// 提取处理函数
	handlerFunc := a.extractHandlerFunction(callExpr, typeInfo)
	if handlerFunc == nil {
		fmt.Printf("[DEBUG] 未找到处理函数\n")
		return nil
	}

	// 提取请求和响应信息
	request := a.extractor.ExtractRequest(handlerFunc, typeInfo, a.resolveType)
	response := a.extractor.ExtractResponse(handlerFunc, typeInfo, a.resolveType)

	return &models.RouteInfo{
		Method:   method,
		Path:     fullPath,
		Handler:  handlerFunc.Name.Name,
		Request:  request,
		Response: response,
	}
}

// 辅助方法
func (a *Analyzer) isCallOnRouter(callExpr *ast.CallExpr, targetRouter types.Object, typeInfo *types.Info) bool {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			if obj := typeInfo.ObjectOf(ident); obj != nil {
				return obj == targetRouter
			}
		}
	}
	return false
}

func (a *Analyzer) isRouterArgument(arg ast.Expr, targetRouter types.Object, typeInfo *types.Info) bool {
	if ident, ok := arg.(*ast.Ident); ok {
		if obj := typeInfo.ObjectOf(ident); obj != nil {
			return obj == targetRouter
		}
	}
	return false
}

func (a *Analyzer) getFunctionCallKey(callExpr *ast.CallExpr, pkg *packages.Package) string {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			// 跨包调用，需要解析包路径
			packageAlias := ident.Name
			functionName := selExpr.Sel.Name

			// 查找实际包路径
			actualPackagePath := a.resolvePackagePath(packageAlias, pkg)
			if actualPackagePath != "" {
				return actualPackagePath + "+" + functionName
			}
		}
	} else if ident, ok := callExpr.Fun.(*ast.Ident); ok {
		// 同包调用
		return pkg.PkgPath + "+" + ident.Name
	}
	return ""
}

func (a *Analyzer) resolvePackagePath(packageAlias string, currentPkg *packages.Package) string {
	for _, file := range currentPkg.Syntax {
		for _, imp := range file.Imports {
			if imp.Name != nil && imp.Name.Name == packageAlias {
				return strings.Trim(imp.Path.Value, "\"")
			} else if imp.Name == nil {
				importPath := strings.Trim(imp.Path.Value, "\"")
				parts := strings.Split(importPath, "/")
				if len(parts) > 0 && parts[len(parts)-1] == packageAlias {
					return importPath
				}
			}
		}
	}
	return ""
}

func (a *Analyzer) getRouterParameterObject(rgf *models.RouterGroupFunction) types.Object {
	if rgf.FuncDecl.Type.Params != nil && len(rgf.FuncDecl.Type.Params.List) > rgf.RouterParamIdx {
		param := rgf.FuncDecl.Type.Params.List[rgf.RouterParamIdx]
		if len(param.Names) > 0 {
			paramName := param.Names[0]
			if obj := rgf.Package.TypesInfo.ObjectOf(paramName); obj != nil {
				return obj
			}
		}
	}
	return nil
}

func (a *Analyzer) copyVisitedFuncs(original map[string]bool) map[string]bool {
	copy := make(map[string]bool)
	for k, v := range original {
		copy[k] = v
	}
	return copy
}

func (a *Analyzer) findGroupResultObject(callExpr *ast.CallExpr, pkg *packages.Package) types.Object {
	// 在包的语法树中查找赋值语句
	for _, file := range pkg.Syntax {
		var foundObj types.Object
		ast.Inspect(file, func(node ast.Node) bool {
			if foundObj != nil {
				return false
			}

			if assignStmt, ok := node.(*ast.AssignStmt); ok {
				for i, rhs := range assignStmt.Rhs {
					if rhs == callExpr {
						if i < len(assignStmt.Lhs) {
							if ident, ok := assignStmt.Lhs[i].(*ast.Ident); ok {
								if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
									foundObj = obj
									return false
								}
							}
						}
					}
				}
			}

			if genDecl, ok := node.(*ast.GenDecl); ok {
				for _, spec := range genDecl.Specs {
					if valueSpec, ok := spec.(*ast.ValueSpec); ok {
						for i, value := range valueSpec.Values {
							if value == callExpr {
								if i < len(valueSpec.Names) {
									if obj := pkg.TypesInfo.ObjectOf(valueSpec.Names[i]); obj != nil {
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
	return nil
}

func (a *Analyzer) combinePaths(basePath, segment string) string {
	if basePath == "" {
		return segment
	}
	if segment == "" {
		return basePath
	}

	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if !strings.HasPrefix(segment, "/") {
		segment = "/" + segment
	}

	return filepath.Join(basePath, segment)
}

func (a *Analyzer) extractHandlerFunction(callExpr *ast.CallExpr, typeInfo *types.Info) *ast.FuncDecl {
	if len(callExpr.Args) == 0 {
		return nil
	}

	lastArg := callExpr.Args[len(callExpr.Args)-1]

	if ident, ok := lastArg.(*ast.Ident); ok {
		if obj := typeInfo.ObjectOf(ident); obj != nil {
			return a.findFunctionDeclaration(obj.Name())
		}
	}

	if selExpr, ok := lastArg.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			packageName := ident.Name
			functionName := selExpr.Sel.Name
			return a.findFunctionInPackage(packageName, functionName)
		}
	}

	if funcLit, ok := lastArg.(*ast.FuncLit); ok {
		return &ast.FuncDecl{
			Name: &ast.Ident{Name: "anonymous"},
			Type: funcLit.Type,
			Body: funcLit.Body,
		}
	}

	return nil
}

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

func (a *Analyzer) findFunctionInPackage(packageName, functionName string) *ast.FuncDecl {
	for _, pkg := range a.project.Packages {
		for _, file := range pkg.Syntax {
			for _, imp := range file.Imports {
				if imp.Name != nil && imp.Name.Name == packageName {
					return a.findFunctionInImportedPackage(imp.Path.Value, functionName)
				}
			}

			if strings.HasSuffix(pkg.PkgPath, "/"+packageName) ||
				strings.HasSuffix(pkg.PkgPath, packageName) {
				return a.findFunctionDeclarationInPackage(pkg, functionName)
			}
		}
	}

	return &ast.FuncDecl{
		Name: &ast.Ident{Name: functionName},
		Type: &ast.FuncType{},
		Body: &ast.BlockStmt{},
	}
}

func (a *Analyzer) findFunctionInImportedPackage(importPath, functionName string) *ast.FuncDecl {
	importPath = strings.Trim(importPath, "\"")

	for _, pkg := range a.project.Packages {
		if pkg.PkgPath == importPath {
			return a.findFunctionDeclarationInPackage(pkg, functionName)
		}
	}
	return nil
}

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

func (a *Analyzer) saveRoutesToDebugFile(routes []models.RouteInfo, filePath string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	for _, route := range routes {
		fmt.Fprintf(file, "Method: %s, Path: %s, Handler: %s, Request: %v, Response: %v\n",
			route.Method, route.Path, route.Handler, route.Request, route.Response)
	}
	return nil
}
