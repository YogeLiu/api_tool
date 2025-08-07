// 文件位置: pkg/analyzer/analyzer.go
package analyzer

import (
	"fmt"
	"go/ast"
	"log"
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
	project               *parser.Project
	extractor             extractor.Extractor
	routeCache            map[string]bool                        // 路由去重映射
	routerGroupFunctions  map[string]*models.RouterGroupFunction // 路由分组函数索引
	responseParsingEngine *helper.ResponseParsingEngine
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
	// 使用现有的包信息创建响应解析引擎，避免重复加载包
	responseParsingEngine := helper.NewResponseParsingEngine(proj.Packages)

	return &Analyzer{
		project:               proj,
		extractor:             ext,
		routeCache:            make(map[string]bool),
		routerGroupFunctions:  make(map[string]*models.RouterGroupFunction),
		responseParsingEngine: responseParsingEngine,
	}
}

// Analyze 执行主分析流程
func (a *Analyzer) Analyze() (*models.APIInfo, error) {
	log.Printf("[DEBUG] 开始两阶段路由分析\n")

	// 预处理阶段：初始化提取器，进行预扫描
	log.Printf("[DEBUG] === 预处理阶段：初始化提取器 ===\n")
	if err := a.extractor.InitializeAnalysis(); err != nil {
		return nil, &models.AnalysisError{
			Context: "初始化提取器",
			Reason:  fmt.Sprintf("提取器初始化失败: %v", err),
		}
	}

	// 第一阶段：扫描并索引所有路由分组函数
	log.Printf("[DEBUG] === 第一阶段：索引路由分组函数 ===\n")
	a.routerGroupFunctions = a.extractor.FindRouterGroupFunctions(a.project.Packages)
	log.Printf("[DEBUG] 索引完成，找到 %d 个路由分组函数:\n", len(a.routerGroupFunctions))
	for key := range a.routerGroupFunctions {
		log.Printf("[DEBUG]   - %s\n", key)
	}

	// 第二阶段：从根路由开始递归解析
	log.Printf("[DEBUG] === 第二阶段：递归解析路由 ===\n")
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
		log.Printf("[DEBUG] 开始分析根路由器: %s\n", rootRouter.Name())
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

	log.Printf("[DEBUG] 分析完成，总共找到 %d 个路由\n", len(routes))

	// 将 map 转换为 slice
	var routeList []models.RouteInfo
	for _, route := range routes {
		routeList = append(routeList, route)
	}

	return &models.APIInfo{
		Routes: routeList,
	}, nil
}

// analyzeRouterRecursively 递归解析路由器对象的使用
func (a *Analyzer) analyzeRouterRecursively(context *RouteContext) map[string]models.RouteInfo {
	var routes []models.RouteInfo

	log.Printf("[DEBUG] analyzeRouterRecursively: 分析路由器 %s，当前路径: %s\n",
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
							log.Printf("[DEBUG] 发现路由分组调用: %s\n", pathSegment)
							newRoutes := a.handleRouteGroupCall(callExpr, context, pathSegment, pkg)
							routes = append(routes, newRoutes...)
						} else if isHTTP, method, pathSegment := a.extractor.IsHTTPMethodCall(callExpr, pkg.TypesInfo); isHTTP {
							log.Printf("[DEBUG] 发现HTTP方法调用: %s %s\n", method, pathSegment)
							route := a.handleHTTPMethodCall(callExpr, context, method, pathSegment, pkg.TypesInfo)
							if route != nil {
								routeKey := fmt.Sprintf("%s:%s:%s", route.Method, route.Path, route.Handler)
								if !a.routeCache[routeKey] {
									a.routeCache[routeKey] = true
									routes = append(routes, *route)
									log.Printf("[DEBUG] 添加路由: %s %s -> %s (包: %s)\n", route.Method, route.Path, route.Handler, route.PackagePath)
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
		// 使用更唯一的Key: Method + Path + PackagePath + Handler
		// 这样即使相同Handler处理不同路径也不会冲突
		uniqueKey := fmt.Sprintf("%s:%s:%s.%s", route.Method, route.Path, route.PackagePath, route.Handler)
		ans[uniqueKey] = route
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
					log.Printf("[DEBUG] 检测到循环调用，跳过: %s\n", funcKey)
					continue
				}

				// 查找对应的路由分组函数
				if rgf, exists := a.routerGroupFunctions[funcKey]; exists {
					log.Printf("[DEBUG] 找到路由分组函数调用: %s\n", funcKey)

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

	log.Printf("[DEBUG] analyzeRouterGroupFunction: 分析函数 %s\n", rgf.UniqueKey)

	// 分析函数体中的路由定义
	if rgf.FuncDecl.Body != nil {
		ast.Inspect(rgf.FuncDecl.Body, func(node ast.Node) bool {
			if callExpr, ok := node.(*ast.CallExpr); ok {
				// 检查是否为对路由器参数的调用
				if a.isCallOnRouter(callExpr, context.RouterObject, rgf.Package.TypesInfo) {
					// 检查是否为路由分组调用
					if isGroup, pathSegment := a.extractor.IsRouteGroupCall(callExpr, rgf.Package.TypesInfo); isGroup {
						log.Printf("[DEBUG] 在路由分组函数中发现子分组: %s\n", pathSegment)
						newRoutes := a.handleRouteGroupCall(callExpr, context, pathSegment, rgf.Package)
						routes = append(routes, newRoutes...)
					} else if isHTTP, method, pathSegment := a.extractor.IsHTTPMethodCall(callExpr, rgf.Package.TypesInfo); isHTTP {
						log.Printf("[DEBUG] 在路由分组函数中发现HTTP方法: %s %s\n", method, pathSegment)
						route := a.handleHTTPMethodCall(callExpr, context, method, pathSegment, rgf.Package.TypesInfo)
						if route != nil {
							routeKey := fmt.Sprintf("%s:%s:%s", route.Method, route.Path, route.Handler)
							if !a.routeCache[routeKey] {
								a.routeCache[routeKey] = true
								routes = append(routes, *route)
								log.Printf("[DEBUG] 添加路由: %s %s -> %s\n", route.Method, route.Path, route.Handler)
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
	log.Printf("[DEBUG] handleRouteGroupCall: 新路径 %s\n", newPath)

	// 查找分组调用的结果对象
	groupObj := a.findGroupResultObject(callExpr, pkg)
	if groupObj == nil {
		log.Printf("[DEBUG] 未找到分组结果对象\n")
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
	log.Printf("[DEBUG] handleHTTPMethodCall: 完整路径: %s\n", fullPath)

	// 提取处理函数信息（包含包信息）
	handlerInfo := a.extractHandlerInfo(callExpr, typeInfo)
	if handlerInfo == nil || handlerInfo.FuncDecl == nil {
		log.Printf("[DEBUG] 未找到处理函数\n")
		return nil
	}

	var startLine, endLine int
	if handlerInfo.Package != nil && handlerInfo.Package.Fset != nil {
		startPos := handlerInfo.Package.Fset.Position(handlerInfo.FuncDecl.Pos())
		endPos := handlerInfo.Package.Fset.Position(handlerInfo.FuncDecl.End())
		startLine = startPos.Line
		endLine = endPos.Line
	} else {
		// 如果无法获取FileSet，使用默认值
		startLine = 0
		endLine = 0
		log.Printf("[DEBUG] 无法获取FileSet，使用默认行号\n")
	}

	// 创建基础路由信息
	routeInfo := &models.RouteInfo{
		PackageName:      handlerInfo.PackageName,
		PackagePath:      handlerInfo.PackagePath,
		Handler:          handlerInfo.FuncDecl.Name.Name,
		HandlerStartLine: startLine,
		HandlerEndLine:   endLine,
		Method:           method,
		Path:             fullPath,
	}

	// 使用 responseParsingEngine 分析 Handler 的请求和响应参数
	if a.responseParsingEngine != nil {
		handlerKey := handlerInfo.PackagePath + "." + handlerInfo.FuncDecl.Name.Name
		log.Printf("[DEBUG] 尝试分析Handler参数: %s\n", handlerKey)

		// 分析Handler的请求和响应参数
		if handlerAnalysisResult := a.analyzeHandlerWithResponseEngine(handlerInfo); handlerAnalysisResult != nil {
			// 将分析结果集成到路由信息中
			routeInfo.RequestParams = a.convertToModelRequestParams(handlerAnalysisResult.RequestParams)
			routeInfo.ResponseSchema = a.convertToModelAPISchema(handlerAnalysisResult.Response)
			log.Printf("[DEBUG] 成功集成Handler参数分析结果: 请求参数%d个\n", len(handlerAnalysisResult.RequestParams))
		}
	}

	return routeInfo
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

	log.Printf("[DEBUG] extractHandlerInfo: 提取处理函数，参数类型: %T\n", lastArg)

	// 1. 处理标识符（本包中的函数）
	if ident, ok := lastArg.(*ast.Ident); ok {
		if obj := typeInfo.ObjectOf(ident); obj != nil {
			log.Printf("[DEBUG] extractHandlerInfo: 通过标识符查找函数: %s\n", obj.Name())

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
			log.Printf("[DEBUG] extractHandlerInfo: 通过包选择器查找函数: %s.%s\n", packageName, functionName)

			// 1. 优先使用类型安全的 types.PkgName.Imported() 方法
			if obj := typeInfo.ObjectOf(ident); obj != nil {
				if pkgName, ok := obj.(*types.PkgName); ok {
					importedPkg := pkgName.Imported() // *types.Package
					realPkgPath := importedPkg.Path()
					log.Printf("[DEBUG] extractHandlerInfo: 通过types.PkgName解析别名 %s -> %s\n", packageName, realPkgPath)

					realPkg := a.findPackageByPath(realPkgPath)
					if realPkg != nil {
						funcDecl := a.findFunctionDeclarationInPackage(realPkg, functionName)
						if funcDecl != nil {
							hasGinContext := a.hasGinContextParameter(funcDecl)
							log.Printf("[DEBUG] extractHandlerInfo: 在真实包中找到函数 %s (%s) - 有gin.Context: %v\n",
								functionName, realPkgPath, hasGinContext)

							return &HandlerInfo{
								FuncDecl:    funcDecl,
								PackageName: realPkg.Name,
								PackagePath: realPkg.PkgPath,
								Package:     realPkg,
							}
						}
					}
				} else {
					// 可能是变量或其他类型的对象，记录但继续fallback
					log.Printf("[DEBUG] extractHandlerInfo: 标识符 %s 不是包名 (类型: %T)\n", packageName, obj)
				}
			} else {
				log.Printf("[DEBUG] extractHandlerInfo: TypesInfo中无法找到别名对象: %s\n", packageName)
			}

			// 2. 使用 packages.Imports 精准fallback
			candidates := a.findHandlerCandidatesViaImports(packageName, functionName)
			if len(candidates) > 0 {
				bestCandidate := a.selectBestHandlerCandidate(candidates)
				if bestCandidate != nil {
					log.Printf("[DEBUG] extractHandlerInfo: 通过imports映射找到Handler: %s (%s)\n",
						bestCandidate.FuncDecl.Name.Name, bestCandidate.PackagePath)
					return bestCandidate
				}
			}

			// 3. 最后才使用暴力扫描（保留作为最后手段）
			log.Printf("[DEBUG] extractHandlerInfo: 使用暴力扫描作为最后手段: %s.%s\n", packageName, functionName)
			legacyCandidates := a.findHandlerCandidatesViaLegacyScan(packageName, functionName)
			if len(legacyCandidates) > 0 {
				bestCandidate := a.selectBestHandlerCandidate(legacyCandidates)
				if bestCandidate != nil {
					log.Printf("[DEBUG] extractHandlerInfo: 通过暴力扫描找到Handler: %s (%s)\n",
						bestCandidate.FuncDecl.Name.Name, bestCandidate.PackagePath)
					return bestCandidate
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
	log.Printf("[DEBUG] findFunctionDeclaration: 找到 %d 个同名函数 %s，进行筛选\n", len(candidates), funcName)

	for i, candidate := range candidates {
		hasGinContext := a.hasGinContextParameter(candidate)
		isMethod := candidate.Recv != nil
		log.Printf("[DEBUG] findFunctionDeclaration: 候选 %d - 有gin.Context参数: %v, 是方法: %v\n",
			i+1, hasGinContext, isMethod)

		// 优先选择有gin.Context参数的函数（通常是Handler）
		if hasGinContext {
			log.Printf("[DEBUG] findFunctionDeclaration: 选择有gin.Context参数的函数\n")
			return candidate
		}
	}

	// 如果没有找到有gin.Context的，返回第一个
	log.Printf("[DEBUG] findFunctionDeclaration: 未找到有gin.Context参数的函数，返回第一个\n")
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

// analyzeHandlerWithFuncBody 使用funcBodyEngine分析Handler的请求和响应参数
func (a *Analyzer) analyzeHandlerWithResponseEngine(handlerInfo *HandlerInfo) *helper.HandlerAnalysisResult {
	if handlerInfo == nil || handlerInfo.FuncDecl == nil || handlerInfo.Package == nil {
		log.Printf("[DEBUG] analyzeHandlerWithResponseEngine: 参数检查失败 - handlerInfo: %v, FuncDecl: %v, Package: %v\n",
			handlerInfo != nil, handlerInfo != nil && handlerInfo.FuncDecl != nil, handlerInfo != nil && handlerInfo.Package != nil)
		return nil
	}

	log.Printf("[DEBUG] analyzeHandlerWithResponseEngine: 分析 %s.%s\n", handlerInfo.PackagePath, handlerInfo.FuncDecl.Name.Name)
	log.Printf("[DEBUG] analyzeHandlerWithResponseEngine: Package路径: %s, Package名称: %s\n", handlerInfo.Package.PkgPath, handlerInfo.Package.Name)
	log.Printf("[DEBUG] analyzeHandlerWithResponseEngine: TypesInfo为空: %v\n", handlerInfo.Package.TypesInfo == nil)

	// 使用responseParsingEngine直接分析Handler
	result := a.responseParsingEngine.AnalyzeHandlerComplete(handlerInfo.FuncDecl, handlerInfo.Package)

	if result != nil {
		log.Printf("[DEBUG] responseParsingEngine分析成功: 请求参数%d个, 响应类型%s\n",
			len(result.RequestParams),
			func() string {
				if result.Response != nil {
					return result.Response.Type
				}
				return "nil"
			}())
		return result
	} else {
		log.Printf("[DEBUG] responseParsingEngine分析失败\n")
		return nil
	}
}

// convertToModelRequestParams 转换helper.RequestParamInfo到models.RequestParamInfo
func (a *Analyzer) convertToModelRequestParams(helperParams []helper.RequestParamInfo) []models.RequestParamInfo {
	var modelParams []models.RequestParamInfo

	for _, helperParam := range helperParams {
		modelParam := models.RequestParamInfo{
			ParamType:   helperParam.ParamType,
			ParamName:   helperParam.ParamName,
			IsRequired:  helperParam.IsRequired,
			Source:      helperParam.Source,
			ParamSchema: a.convertToModelAPISchema(helperParam.ParamSchema),
		}
		modelParams = append(modelParams, modelParam)
	}

	return modelParams
}

// convertToModelAPISchema 转换helper.APISchema到models.APISchema
func (a *Analyzer) convertToModelAPISchema(helperSchema *helper.APISchema) *models.APISchema {
	if helperSchema == nil {
		return nil
	}

	modelSchema := &models.APISchema{
		Type:        helperSchema.Type,
		Description: helperSchema.Description,
		JSONTag:     helperSchema.JSONTag,
	}

	// 转换Properties
	if helperSchema.Properties != nil {
		modelSchema.Properties = make(map[string]*models.APISchema)
		for key, value := range helperSchema.Properties {
			modelSchema.Properties[key] = a.convertToModelAPISchema(value)
		}
	}

	// 转换Items
	if helperSchema.Items != nil {
		modelSchema.Items = a.convertToModelAPISchema(helperSchema.Items)
	}

	return modelSchema
}

// packageMatchesAlias 检查包是否匹配给定的别名
func (a *Analyzer) packageMatchesAlias(pkg *packages.Package, alias string) bool {
	// 1. 检查包名是否直接匹配
	if pkg.Name == alias {
		return true
	}

	// 2. 检查包路径的最后一部分是否匹配
	pathParts := strings.Split(pkg.PkgPath, "/")
	if len(pathParts) > 0 && pathParts[len(pathParts)-1] == alias {
		return true
	}

	// 3. 特殊处理版本化的别名，如 v1Order, v2Order 等
	// 这些别名通常指向 .../v1/order, .../v2/order 等路径
	if len(pathParts) >= 2 {
		// 检查是否为 vX + 包名 的模式
		lastPart := pathParts[len(pathParts)-1]       // 如 "order"
		secondLastPart := pathParts[len(pathParts)-2] // 如 "v1"

		// 构建可能的别名 如 "v1" + "order" = "v1order"
		possibleAlias := secondLastPart + lastPart
		if strings.EqualFold(possibleAlias, alias) { // 忽略大小写比较
			return true
		}

		// 也尝试首字母大写的版本 如 "v1Order"
		if len(lastPart) > 0 {
			capitalizedAlias := secondLastPart + strings.ToUpper(lastPart[:1]) + lastPart[1:]
			if capitalizedAlias == alias {
				return true
			}
		}
	}

	// 4. 处理驼峰命名转下划线的情况
	// 如 healthGroupInsurance -> health_group_insurance
	underscoreAlias := a.camelToUnderscore(alias)
	if len(pathParts) > 0 && pathParts[len(pathParts)-1] == underscoreAlias {
		return true
	}

	// 5. 处理别名中包含包名的情况
	// 如 healthGroupInsurance 可能对应包名 health_group_insurance
	if strings.Contains(pkg.PkgPath, underscoreAlias) {
		return true
	}

	return false
}

// camelToUnderscore 将驼峰命名转换为下划线命名
func (a *Analyzer) camelToUnderscore(s string) string {
	var result []rune
	for i, r := range s {
		if i > 0 && 'A' <= r && r <= 'Z' {
			result = append(result, '_')
		}
		result = append(result, 'a'+(r-'A'))
		if 'a' <= r && r <= 'z' {
			result[len(result)-1] = r
		}
	}
	return string(result)
}

// selectBestHandlerCandidate 从候选函数中选择最佳的Handler
func (a *Analyzer) selectBestHandlerCandidate(candidates []*HandlerInfo) *HandlerInfo {
	if len(candidates) == 0 {
		return nil
	}

	if len(candidates) == 1 {
		return candidates[0]
	}

	log.Printf("[DEBUG] selectBestHandlerCandidate: 评估 %d 个候选函数\n", len(candidates))

	var bestCandidate *HandlerInfo
	bestScore := -1

	for i, candidate := range candidates {
		score := a.calculateHandlerScore(candidate)
		hasGinContext := a.hasGinContextParameter(candidate.FuncDecl)

		log.Printf("[DEBUG] selectBestHandlerCandidate: 候选 %d - %s (%s) - gin.Context: %v, 评分: %d\n",
			i+1, candidate.FuncDecl.Name.Name, candidate.PackagePath, hasGinContext, score)

		if score > bestScore {
			bestScore = score
			bestCandidate = candidate
		}
	}

	return bestCandidate
}

// calculateHandlerScore 计算Handler候选函数的评分
func (a *Analyzer) calculateHandlerScore(candidate *HandlerInfo) int {
	score := 0

	// 1. 有gin.Context参数的函数得分更高（+100）
	if a.hasGinContextParameter(candidate.FuncDecl) {
		score += 100
	}

	// 2. 在API包中的函数得分更高（+50）
	if strings.Contains(candidate.PackagePath, "/api/") {
		score += 50
	}

	// 3. 不在route包中的函数得分更高（+20）
	if !strings.Contains(candidate.PackagePath, "/route") {
		score += 20
	}

	// 4. 包路径更深的（更具体的）函数得分更高（+路径深度）
	pathDepth := strings.Count(candidate.PackagePath, "/")
	score += pathDepth

	return score
}

// findHandlerCandidatesViaImports 通过packages.Imports精准查找Handler候选
func (a *Analyzer) findHandlerCandidatesViaImports(aliasName, functionName string) []*HandlerInfo {
	var candidates []*HandlerInfo

	log.Printf("[DEBUG] findHandlerCandidatesViaImports: 搜索别名 %s 对应的导入包\n", aliasName)

	// 遍历所有包的导入映射
	for _, pkg := range a.project.Packages {
		for importPath, importedPkg := range pkg.Imports {
			// 检查导入的包是否匹配别名
			// 1. 检查包名是否匹配别名
			// 2. 检查导入时是否使用了别名
			if a.importMatchesAlias(importedPkg, aliasName, importPath) {
				log.Printf("[DEBUG] findHandlerCandidatesViaImports: 找到匹配的导入 %s -> %s (包名: %s)\n",
					aliasName, importPath, importedPkg.Name)

				// 在这个导入包中查找函数
				funcDecl := a.findFunctionDeclarationInPackage(importedPkg, functionName)
				if funcDecl != nil {
					candidates = append(candidates, &HandlerInfo{
						FuncDecl:    funcDecl,
						PackageName: importedPkg.Name,
						PackagePath: importedPkg.PkgPath,
						Package:     importedPkg,
					})
				}
			}
		}
	}

	log.Printf("[DEBUG] findHandlerCandidatesViaImports: 找到 %d 个候选\n", len(candidates))
	return candidates
}

// findHandlerCandidatesViaLegacyScan 通过暴力扫描查找Handler候选（作为最后手段）
func (a *Analyzer) findHandlerCandidatesViaLegacyScan(aliasName, functionName string) []*HandlerInfo {
	var candidates []*HandlerInfo

	log.Printf("[DEBUG] findHandlerCandidatesViaLegacyScan: 暴力扫描所有包查找 %s.%s\n", aliasName, functionName)

	for _, pkg := range a.project.Packages {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Name.Name == functionName {
						// 检查这个包是否匹配包别名
						if pkg.Name == aliasName || a.packageMatchesAlias(pkg, aliasName) {
							log.Printf("[DEBUG] findHandlerCandidatesViaLegacyScan: 找到候选函数 %s 在包 %s (%s)\n",
								functionName, pkg.Name, pkg.PkgPath)
							candidates = append(candidates, &HandlerInfo{
								FuncDecl:    funcDecl,
								PackageName: pkg.Name,
								PackagePath: pkg.PkgPath,
								Package:     pkg,
							})
						}
					}
				}
			}
		}
	}

	log.Printf("[DEBUG] findHandlerCandidatesViaLegacyScan: 找到 %d 个候选\n", len(candidates))
	return candidates
}

// importMatchesAlias 检查导入的包是否匹配给定别名
func (a *Analyzer) importMatchesAlias(importedPkg *packages.Package, aliasName, importPath string) bool {
	// 1. 检查包名是否直接匹配
	if importedPkg.Name == aliasName {
		return true
	}

	// 2. 使用现有的包匹配逻辑
	if a.packageMatchesAlias(importedPkg, aliasName) {
		return true
	}

	// 3. 检查是否通过路径部分匹配（更精确的匹配）
	// 比如 healthGroupInsurance 可能对应 .../health_group_insurance
	pathParts := strings.Split(importPath, "/")
	if len(pathParts) > 0 {
		lastPart := pathParts[len(pathParts)-1]
		if lastPart == aliasName {
			return true
		}

		// 驼峰转下划线匹配
		if a.camelToUnderscore(aliasName) == lastPart {
			return true
		}
	}

	return false
}
