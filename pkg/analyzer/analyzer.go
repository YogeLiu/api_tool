// 文件位置: pkg/analyzer/analyzer.go
package analyzer

import (
	"fmt"
	"go/ast"
	"strings"

	"go/types"

	"path/filepath"

	"github.com/YogeLiu/api-tool/helper"
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
	funcBodyEngine       *helper.GinHandlerAnalyzer
}

// RouteContext 路由解析上下文
type RouteContext struct {
	ParentPath     string            // 累积的父级路径
	RouterObject   types.Object      // 当前路由器对象
	VisitedFuncs   map[string]bool   // 已访问的函数，防止循环调用
	CallingPackage *packages.Package // 调用的包
}

// HandlerInfo 处理函数信息
type HandlerInfo struct {
	FuncDecl    *ast.FuncDecl     // 函数声明
	PackageName string            // 函数所在包名
	PackagePath string            // 函数所在包路径
	Package     *packages.Package // 函数所在包
}

// NewAnalyzer 创建新的分析器实例
func NewAnalyzer(dir string, proj *parser.Project, ext extractor.Extractor) *Analyzer {
	funcBodyEngine, err := helper.NewGinHandlerAnalyzer(dir)
	if err != nil {
		panic(err)
	}
	return &Analyzer{
		project:              proj,
		extractor:            ext,
		routeCache:           make(map[string]bool),
		routerGroupFunctions: make(map[string]*models.RouterGroupFunction),
		funcBodyEngine:       funcBodyEngine,
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

	routes := make(map[string]models.RouteInfo)

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
		for k, v := range foundRoutes {
			routes[k] = v
		}
	}

	fmt.Printf("[DEBUG] 分析完成，总共找到 %d 个路由\n", len(routes))
	return &models.APIInfo{}, nil
}

// analyzeRouterRecursively 递归解析路由器对象的使用
func (a *Analyzer) analyzeRouterRecursively(context *RouteContext) map[string]models.RouteInfo {
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
									fmt.Printf("[DEBUG] 添加路由: %s %s -> %s (包: %s)\n", route.Method, route.Path, route.Handler, route.PackagePath)
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

	ans := make(map[string]models.RouteInfo)
	for _, route := range routes {
		ans[route.PackagePath+"."+route.Handler] = route
	}

	return ans
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
	for _, route := range nestedRoutes {
		routes = append(routes, route)
	}

	return routes
}

// handleHTTPMethodCall 处理HTTP方法调用
func (a *Analyzer) handleHTTPMethodCall(callExpr *ast.CallExpr, context *RouteContext, method, pathSegment string, typeInfo *types.Info) *models.RouteInfo {
	// 组合完整路径
	fullPath := a.combinePaths(context.ParentPath, pathSegment)
	fmt.Printf("[DEBUG] handleHTTPMethodCall: 完整路径: %s\n", fullPath)

	// 提取处理函数信息（包含包信息）
	handlerInfo := a.extractHandlerInfo(callExpr, typeInfo)
	if handlerInfo == nil || handlerInfo.FuncDecl == nil {
		fmt.Printf("[DEBUG] 未找到处理函数\n")
		return nil
	}

	return &models.RouteInfo{
		PackageName: handlerInfo.PackageName,
		PackagePath: handlerInfo.PackagePath,
		Method:      method,
		Path:        fullPath,
		Handler:     handlerInfo.FuncDecl.Name.Name,
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

// extractHandlerInfo 提取处理函数信息（包括包信息）
func (a *Analyzer) extractHandlerInfo(callExpr *ast.CallExpr, typeInfo *types.Info) *HandlerInfo {
	if len(callExpr.Args) == 0 {
		return nil
	}

	lastArg := callExpr.Args[len(callExpr.Args)-1]

	fmt.Printf("[DEBUG] extractHandlerInfo: 提取处理函数，参数类型: %T\n", lastArg)

	// 1. 处理标识符（本包中的函数）
	if ident, ok := lastArg.(*ast.Ident); ok {
		if obj := typeInfo.ObjectOf(ident); obj != nil {
			fmt.Printf("[DEBUG] extractHandlerInfo: 通过标识符查找函数: %s\n", obj.Name())

			// 获取函数所在的包信息
			pkg := obj.Pkg()
			if pkg != nil {
				funcDecl := a.findFunctionDeclaration(obj.Name())
				if funcDecl != nil {
					return &HandlerInfo{
						FuncDecl:    funcDecl,
						PackageName: pkg.Name(),
						PackagePath: pkg.Path(),
						Package:     a.findPackageByPath(pkg.Path()),
					}
				}
			}
		}
	}

	// 2. 处理选择器表达式（其他包中的函数）
	if selExpr, ok := lastArg.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			packageName := ident.Name
			functionName := selExpr.Sel.Name
			fmt.Printf("[DEBUG] extractHandlerInfo: 通过包选择器查找函数: %s.%s\n", packageName, functionName)

			// 获取选择器对象的类型信息
			if obj := typeInfo.ObjectOf(ident); obj != nil {
				if pkg := obj.Pkg(); pkg != nil {
					funcDecl := a.findFunctionInPackage(packageName, functionName)
					if funcDecl != nil {
						return &HandlerInfo{
							FuncDecl:    funcDecl,
							PackageName: pkg.Name(),
							PackagePath: pkg.Path(),
							Package:     a.findPackageByPath(pkg.Path()),
						}
					}
				}
			}

			// 如果无法从类型信息获取，尝试通过包名查找
			targetPkg := a.findPackageByName(packageName)
			if targetPkg != nil {
				funcDecl := a.findFunctionInPackage(packageName, functionName)
				if funcDecl != nil {
					return &HandlerInfo{
						FuncDecl:    funcDecl,
						PackageName: targetPkg.Name,
						PackagePath: targetPkg.PkgPath,
						Package:     targetPkg,
					}
				}
			}
		}
	}

	// 3. 处理匿名函数
	if funcLit, ok := lastArg.(*ast.FuncLit); ok {
		// 对于匿名函数，使用当前包的信息
		return &HandlerInfo{
			FuncDecl: &ast.FuncDecl{
				Name: &ast.Ident{Name: "anonymous"},
				Type: funcLit.Type,
				Body: funcLit.Body,
			},
			PackageName: "anonymous",
			PackagePath: "anonymous",
			Package:     nil,
		}
	}

	return nil
}

// findPackageByPath 根据包路径查找包
func (a *Analyzer) findPackageByPath(pkgPath string) *packages.Package {
	for _, pkg := range a.project.Packages {
		if pkg.PkgPath == pkgPath {
			return pkg
		}
	}
	return nil
}

// findPackageByName 根据包名查找包
func (a *Analyzer) findPackageByName(pkgName string) *packages.Package {
	for _, pkg := range a.project.Packages {
		if pkg.Name == pkgName {
			return pkg
		}
	}
	return nil
}

func (a *Analyzer) findFunctionDeclaration(funcName string) *ast.FuncDecl {
	var candidates []*ast.FuncDecl

	// 收集所有同名函数
	for _, pkg := range a.project.Packages {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Name.Name == funcName {
						candidates = append(candidates, funcDecl)
					}
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	if len(candidates) == 1 {
		return candidates[0]
	}

	// 如果有多个候选函数，优先选择有gin.Context参数的方法
	fmt.Printf("[DEBUG] findFunctionDeclaration: 找到 %d 个同名函数 %s，进行筛选\n", len(candidates), funcName)

	for i, candidate := range candidates {
		hasGinContext := a.hasGinContextParameter(candidate)
		isMethod := candidate.Recv != nil
		fmt.Printf("[DEBUG] findFunctionDeclaration: 候选 %d - 有gin.Context参数: %v, 是方法: %v\n",
			i+1, hasGinContext, isMethod)

		// 优先选择有gin.Context参数的函数（通常是Handler）
		if hasGinContext {
			fmt.Printf("[DEBUG] findFunctionDeclaration: 选择有gin.Context参数的函数\n")
			return candidate
		}
	}

	// 如果没有找到有gin.Context的，返回第一个
	fmt.Printf("[DEBUG] findFunctionDeclaration: 未找到有gin.Context参数的函数，返回第一个\n")
	return candidates[0]
}

// hasGinContextParameter 检查函数是否有gin.Context参数
func (a *Analyzer) hasGinContextParameter(funcDecl *ast.FuncDecl) bool {
	if funcDecl.Type.Params == nil {
		return false
	}

	for _, param := range funcDecl.Type.Params.List {
		if len(param.Names) > 0 {
			// 检查参数类型是否为gin.Context
			if starExpr, ok := param.Type.(*ast.StarExpr); ok {
				if selExpr, ok := starExpr.X.(*ast.SelectorExpr); ok {
					if ident, ok := selExpr.X.(*ast.Ident); ok {
						if ident.Name == "gin" && selExpr.Sel.Name == "Context" {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func (a *Analyzer) findFunctionInPackage(packageName, functionName string) *ast.FuncDecl {
	fmt.Printf("[DEBUG] findFunctionInPackage: 查找 %s.%s\n", packageName, functionName)

	for _, pkg := range a.project.Packages {
		for _, file := range pkg.Syntax {
			for _, imp := range file.Imports {
				if imp.Name != nil && imp.Name.Name == packageName {
					result := a.findFunctionInImportedPackage(imp.Path.Value, functionName)
					if result != nil {
						fmt.Printf("[DEBUG] findFunctionInPackage: 在导入包中找到函数\n")
						return result
					}
				}
			}

			if strings.HasSuffix(pkg.PkgPath, "/"+packageName) ||
				strings.HasSuffix(pkg.PkgPath, packageName) {
				result := a.findFunctionDeclarationInPackage(pkg, functionName)
				if result != nil {
					fmt.Printf("[DEBUG] findFunctionInPackage: 在包中找到函数\n")
					return result
				}
			}
		}
	}

	// 作为最后的尝试，在所有包中查找方法（有receiver的函数）
	fmt.Printf("[DEBUG] findFunctionInPackage: 尝试查找方法 %s\n", functionName)
	result := a.findMethodByName(functionName)
	if result != nil {
		fmt.Printf("[DEBUG] findFunctionInPackage: 找到方法 %s (有receiver)\n", functionName)
		return result
	}

	fmt.Printf("[DEBUG] findFunctionInPackage: 未找到 %s.%s，创建空函数\n", packageName, functionName)
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

// findMethodByName 查找有receiver的方法（在所有包中搜索）
func (a *Analyzer) findMethodByName(methodName string) *ast.FuncDecl {
	var candidates []*ast.FuncDecl

	// 收集所有同名方法
	for _, pkg := range a.project.Packages {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Name.Name == methodName && funcDecl.Recv != nil {
						candidates = append(candidates, funcDecl)
					}
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	if len(candidates) == 1 {
		return candidates[0]
	}

	// 如果有多个候选方法，优先选择有gin.Context参数的
	fmt.Printf("[DEBUG] findMethodByName: 找到 %d 个同名方法 %s，进行筛选\n", len(candidates), methodName)

	for i, candidate := range candidates {
		hasGinContext := a.hasGinContextParameter(candidate)
		fmt.Printf("[DEBUG] findMethodByName: 候选 %d - 有gin.Context参数: %v\n", i+1, hasGinContext)

		// 优先选择有gin.Context参数的方法（通常是Handler）
		if hasGinContext {
			fmt.Printf("[DEBUG] findMethodByName: 选择有gin.Context参数的方法\n")
			return candidate
		}
	}

	// 如果没有找到有gin.Context的，返回第一个
	fmt.Printf("[DEBUG] findMethodByName: 未找到有gin.Context参数的方法，返回第一个\n")
	return candidates[0]
}
