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
	project              *parser.Project
	responseFuncAnalysis *models.ResponseFunctionAnalysis // 响应函数分析结果
}

// GetFrameworkName 返回框架名称
func (g *GinExtractor) GetFrameworkName() string {
	return "gin"
}

// InitializeAnalysis 初始化分析器，进行预扫描
func (g *GinExtractor) InitializeAnalysis() error {
	fmt.Printf("[DEBUG] GinExtractor: 开始预扫描响应函数\n")

	// 初始化响应函数分析结果
	g.responseFuncAnalysis = &models.ResponseFunctionAnalysis{
		Functions:           make(map[string]*models.ResponseFunction),
		SuccessFunctions:    make([]string, 0),
		ErrorFunctions:      make([]string, 0),
		DirectJSONFunctions: make([]string, 0),
	}

	// 扫描所有包，查找响应函数
	for _, pkg := range g.project.Packages {
		g.scanPackageForResponseFunctions(pkg)
	}

	fmt.Printf("[DEBUG] GinExtractor: 预扫描完成，找到 %d 个响应函数\n", len(g.responseFuncAnalysis.Functions))
	return nil
}

// scanPackageForResponseFunctions 扫描包中的响应函数
func (g *GinExtractor) scanPackageForResponseFunctions(pkg *packages.Package) {
	fmt.Printf("[DEBUG] 扫描包响应函数: %s\n", pkg.PkgPath)

	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				g.analyzeFunction(funcDecl, pkg)
			}
		}
	}
}

// analyzeFunction 分析函数是否为响应函数
func (g *GinExtractor) analyzeFunction(funcDecl *ast.FuncDecl, pkg *packages.Package) {
	if funcDecl.Type.Params == nil {
		return
	}

	// 查找gin.Context参数的索引
	contextParamIdx := g.findGinContextParamIndex(funcDecl, pkg.TypesInfo)
	if contextParamIdx == -1 {
		return // 不包含gin.Context参数，跳过
	}

	// 分析函数内部是否有JSON调用
	jsonCallSite := g.findJSONCallInFunction(funcDecl)
	if jsonCallSite == nil {
		return // 没有JSON调用，跳过
	}

	// 查找数据参数索引
	dataParamIdx := g.findDataParamIndex(funcDecl)

	// 分析基础响应结构
	baseResponse, dataFieldPath := g.analyzeJSONCallStructure(jsonCallSite, pkg.TypesInfo)

	// 动态判断是否为成功响应函数（基于JSON调用分析）
	isSuccessFunc := g.analyzeResponseFunctionType(funcDecl, jsonCallSite, pkg.TypesInfo)

	// 创建响应函数信息
	uniqueKey := pkg.PkgPath + "+" + funcDecl.Name.Name
	responseFunc := &models.ResponseFunction{
		PackagePath:     pkg.PkgPath,
		FunctionName:    funcDecl.Name.Name,
		FuncDecl:        funcDecl,
		Package:         pkg,
		ContextParamIdx: contextParamIdx,
		DataParamIdx:    dataParamIdx,
		JSONCallSite:    jsonCallSite,
		BaseResponse:    baseResponse,
		DataFieldPath:   dataFieldPath,
		UniqueKey:       uniqueKey,
		IsSuccessFunc:   isSuccessFunc,
	}

	// 存储到分析结果中
	g.responseFuncAnalysis.Functions[uniqueKey] = responseFunc

	// 分类存储
	if isSuccessFunc {
		g.responseFuncAnalysis.SuccessFunctions = append(g.responseFuncAnalysis.SuccessFunctions, uniqueKey)
	} else {
		g.responseFuncAnalysis.ErrorFunctions = append(g.responseFuncAnalysis.ErrorFunctions, uniqueKey)
	}

	fmt.Printf("[DEBUG] 找到响应函数: %s (成功函数: %t)\n", uniqueKey, isSuccessFunc)
}

// findGinContextParamIndex 查找gin.Context参数的索引
func (g *GinExtractor) findGinContextParamIndex(funcDecl *ast.FuncDecl, typeInfo *types.Info) int {
	if funcDecl.Type.Params == nil {
		return -1
	}

	for i, param := range funcDecl.Type.Params.List {
		if param.Type != nil {
			if typ := typeInfo.TypeOf(param.Type); typ != nil {
				if g.isGinContextType(typ) {
					return i
				}
			}
		}
	}
	return -1
}

// isGinContextType 检查类型是否为gin.Context
func (g *GinExtractor) isGinContextType(typ types.Type) bool {
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
	return false
}

// findJSONCallInFunction 查找函数内部的JSON调用
func (g *GinExtractor) findJSONCallInFunction(funcDecl *ast.FuncDecl) *ast.CallExpr {
	if funcDecl.Body == nil {
		return nil
	}

	var jsonCall *ast.CallExpr

	// 遍历函数体，查找JSON方法调用
	ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
				methodName := selExpr.Sel.Name
				// 检查是否为JSON相关方法
				if g.isJSONMethod(methodName) {
					jsonCall = callExpr
					return false // 找到第一个就停止搜索
				}
			}
		}
		return true
	})

	return jsonCall
}

// findDataParamIndex 查找数据参数索引（通常命名为data或类似）
func (g *GinExtractor) findDataParamIndex(funcDecl *ast.FuncDecl) int {
	if funcDecl.Type.Params == nil {
		return -1
	}

	for i, param := range funcDecl.Type.Params.List {
		if len(param.Names) > 0 {
			paramName := param.Names[0].Name
			// 检查参数名是否符合数据参数的模式
			if g.isDataParameterName(paramName) {
				// 进一步检查参数类型是否为interface{}
				if param.Type != nil {
					if ident, ok := param.Type.(*ast.InterfaceType); ok {
						if ident.Methods == nil || len(ident.Methods.List) == 0 {
							return i // 是interface{}类型
						}
					}
				}
			}
		}
	}
	return -1
}

// isDataParameterName 检查参数名是否为数据参数
func (g *GinExtractor) isDataParameterName(paramName string) bool {
	dataParamNames := []string{
		"data", "Data",
		"resp", "response", "Response",
		"result", "Result",
		"body", "Body",
		"payload", "Payload",
	}

	for _, name := range dataParamNames {
		if paramName == name {
			return true
		}
	}
	return false
}

// analyzeJSONCallStructure 分析JSON调用的结构
func (g *GinExtractor) analyzeJSONCallStructure(jsonCall *ast.CallExpr, typeInfo *types.Info) (*models.FieldInfo, string) {
	if jsonCall == nil || len(jsonCall.Args) < 2 {
		return nil, ""
	}

	// 第二个参数是响应数据结构
	responseArg := jsonCall.Args[1]

	// 分析响应结构
	if compositeLit, ok := responseArg.(*ast.CompositeLit); ok {
		return g.analyzeCompositeLitStructure(compositeLit, typeInfo)
	}

	// 如果是变量引用，尝试分析类型
	if typ := typeInfo.TypeOf(responseArg); typ != nil {
		baseResponse := g.resolveTypeStructure(typ)
		// 对于包装结构，通常数据字段为"Data"或"data"
		dataFieldPath := g.findDataFieldInStructure(baseResponse)
		return baseResponse, dataFieldPath
	}

	return nil, ""
}

// analyzeCompositeLitStructure 分析复合字面量结构
func (g *GinExtractor) analyzeCompositeLitStructure(lit *ast.CompositeLit, typeInfo *types.Info) (*models.FieldInfo, string) {
	if typ := typeInfo.TypeOf(lit); typ != nil {
		baseResponse := g.resolveTypeStructure(typ)

		// 分析字面量中的字段，查找数据字段
		dataFieldPath := g.findDataFieldInCompositeLit(lit)
		if dataFieldPath == "" {
			// 如果在字面量中没找到，从结构体定义中查找
			dataFieldPath = g.findDataFieldInStructure(baseResponse)
		}

		return baseResponse, dataFieldPath
	}
	return nil, ""
}

// findDataFieldInCompositeLit 在复合字面量中查找数据字段
func (g *GinExtractor) findDataFieldInCompositeLit(lit *ast.CompositeLit) string {
	for _, elt := range lit.Elts {
		if keyValue, ok := elt.(*ast.KeyValueExpr); ok {
			if ident, ok := keyValue.Key.(*ast.Ident); ok {
				fieldName := ident.Name
				// 检查是否为数据字段名
				if g.isDataFieldName(fieldName) {
					return fieldName
				}
			}
		}
	}
	return ""
}

// findDataFieldInStructure 在结构体中查找数据字段
func (g *GinExtractor) findDataFieldInStructure(structInfo *models.FieldInfo) string {
	if structInfo == nil {
		return ""
	}

	for _, field := range structInfo.Fields {
		if g.isDataFieldName(field.Name) {
			return field.Name
		}
	}
	return ""
}

// isDataFieldName 检查字段名是否为数据字段
func (g *GinExtractor) isDataFieldName(fieldName string) bool {
	dataFieldNames := []string{
		"Data", "data",
		"Result", "result",
		"Payload", "payload",
		"Body", "body",
		"Content", "content",
	}

	for _, name := range dataFieldNames {
		if fieldName == name {
			return true
		}
	}
	return false
}

// resolveTypeStructure 解析类型结构（简化版）
func (g *GinExtractor) resolveTypeStructure(typ types.Type) *models.FieldInfo {
	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		return &models.FieldInfo{
			Name: named.Obj().Name(),
			Type: named.Obj().Name(),
		}
	}

	if structType, ok := typ.(*types.Struct); ok {
		fields := make([]models.FieldInfo, 0)
		for i := 0; i < structType.NumFields(); i++ {
			field := structType.Field(i)
			fields = append(fields, models.FieldInfo{
				Name: field.Name(),
				Type: field.Type().String(),
			})
		}
		return &models.FieldInfo{
			Type:   "struct",
			Fields: fields,
		}
	}

	return &models.FieldInfo{
		Type: typ.String(),
	}
}

// analyzeResponseFunctionType 动态分析响应函数类型（成功/错误）
func (g *GinExtractor) analyzeResponseFunctionType(funcDecl *ast.FuncDecl, jsonCall *ast.CallExpr, typeInfo *types.Info) bool {
	if jsonCall == nil || len(jsonCall.Args) < 2 {
		// 没有JSON调用或参数不足，根据函数名推断
		return g.inferResponseTypeFromName(funcDecl.Name.Name)
	}

	fmt.Printf("[DEBUG] analyzeResponseFunctionType: 分析函数 %s\n", funcDecl.Name.Name)

	// 分析JSON调用的响应结构
	responseArg := jsonCall.Args[1]

	// 分析响应结构是否包含错误信息字段
	hasErrorFields := g.analyzeResponseStructureForErrors(responseArg, typeInfo)

	// 分析HTTP状态码
	successStatusCode := g.analyzeStatusCodeForSuccess(jsonCall)

	fmt.Printf("[DEBUG] analyzeResponseFunctionType: 函数 %s, 有错误字段: %t, 成功状态码: %t\n",
		funcDecl.Name.Name, hasErrorFields, successStatusCode)

	// 如果有明确的成功状态码（200）且没有错误字段，认为是成功函数
	if successStatusCode && !hasErrorFields {
		return true
	}

	// 回退到基于函数名的推断
	return g.inferResponseTypeFromName(funcDecl.Name.Name)
}

// analyzeResponseStructureForErrors 分析响应结构是否包含错误字段
func (g *GinExtractor) analyzeResponseStructureForErrors(responseArg ast.Expr, typeInfo *types.Info) bool {
	if compositeLit, ok := responseArg.(*ast.CompositeLit); ok {
		// 分析结构体字面量中的字段
		for _, elt := range compositeLit.Elts {
			if keyValue, ok := elt.(*ast.KeyValueExpr); ok {
				if fieldIdent, ok := keyValue.Key.(*ast.Ident); ok {
					fieldName := fieldIdent.Name
					// 检查是否为错误相关字段
					if g.isErrorField(fieldName) {
						fmt.Printf("[DEBUG] analyzeResponseStructureForErrors: 发现错误字段 %s\n", fieldName)
						return true
					}
				}
			}
		}
	}

	// 检查类型定义中是否包含错误字段
	if typ := typeInfo.TypeOf(responseArg); typ != nil {
		return g.checkTypeForErrorFields(typ)
	}

	return false
}

// analyzeStatusCodeForSuccess 分析状态码是否为成功状态码
func (g *GinExtractor) analyzeStatusCodeForSuccess(jsonCall *ast.CallExpr) bool {
	if len(jsonCall.Args) < 1 {
		return false
	}

	statusArg := jsonCall.Args[0]

	// 检查是否为数字字面量
	if basicLit, ok := statusArg.(*ast.BasicLit); ok {
		if basicLit.Value == "200" || basicLit.Value == "http.StatusOK" {
			return true
		}
	}

	// 检查是否为标准库常量
	if selExpr, ok := statusArg.(*ast.SelectorExpr); ok {
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			if ident.Name == "http" && strings.Contains(selExpr.Sel.Name, "OK") {
				return true
			}
		}
	}

	return false
}

// isErrorField 检查字段名是否为错误相关字段
func (g *GinExtractor) isErrorField(fieldName string) bool {
	errorFields := []string{
		"Error", "error", "Err", "err",
		"Message", "message", "Msg", "msg",
		"Code", "code", "ErrCode", "errCode",
		"Status", "status",
	}

	for _, errField := range errorFields {
		if fieldName == errField {
			return true
		}
	}
	return false
}

// checkTypeForErrorFields 检查类型定义中是否包含错误字段
func (g *GinExtractor) checkTypeForErrorFields(typ types.Type) bool {
	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	// 检查结构体类型
	if structType, ok := typ.(*types.Struct); ok {
		for i := 0; i < structType.NumFields(); i++ {
			field := structType.Field(i)
			if g.isErrorField(field.Name()) {
				return true
			}
		}
	}

	return false
}

// inferResponseTypeFromName 基于函数名推断响应类型（回退方案）
func (g *GinExtractor) inferResponseTypeFromName(funcName string) bool {
	// 错误相关关键词
	errorKeywords := []string{"Err", "Error", "Fail", "Failed", "Bad", "Invalid"}
	for _, keyword := range errorKeywords {
		if strings.Contains(funcName, keyword) {
			return false
		}
	}

	// 成功相关关键词
	successKeywords := []string{"OK", "Ok", "Success", "Successful"}
	for _, keyword := range successKeywords {
		if strings.Contains(funcName, keyword) {
			return true
		}
	}

	// 默认认为是成功函数（更保守的策略）
	return true
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

// ExtractResponse 提取响应信息 - 使用预索引的响应函数信息
func (g *GinExtractor) ExtractResponse(handlerDecl *ast.FuncDecl, typeInfo *types.Info, resolver TypeResolver) models.ResponseInfo {
	response := models.ResponseInfo{}

	if handlerDecl.Body == nil {
		return response
	}

	fmt.Printf("[DEBUG] ExtractResponse: 开始分析处理函数 %s\n", handlerDecl.Name.Name)

	// 获取Context参数名
	contextParam := g.findContextParameter(handlerDecl)
	if contextParam == "" {
		fmt.Printf("[DEBUG] ExtractResponse: 未找到Context参数\n")
		return response
	}

	fmt.Printf("[DEBUG] ExtractResponse: 找到Context参数: %s\n", contextParam)

	// 步骤1: 直接查找 ctx.JSON 调用
	directJSONCall := g.findDirectContextJSONCall(handlerDecl, contextParam)
	if directJSONCall != nil {
		fmt.Printf("[DEBUG] ExtractResponse: 找到直接的ctx.JSON调用\n")
		businessData := g.extractBusinessDataFromDirectCall(directJSONCall, typeInfo, resolver)
		if businessData != nil {
			response.Body = businessData
			fmt.Printf("[DEBUG] ExtractResponse: 直接JSON调用找到业务数据，类型: %s\n", businessData.Type)
			return response
		}
	}

	// 步骤2: 查找预索引的响应函数调用
	responseFuncCall := g.findResponseFunctionCall(handlerDecl)
	if responseFuncCall != nil {
		fmt.Printf("[DEBUG] ExtractResponse: 找到响应函数调用: %s\n", responseFuncCall.FunctionName)

		// 只处理成功响应函数
		if responseFuncCall.IsSuccessFunc {
			businessData := g.extractBusinessDataFromResponseFunc(responseFuncCall, handlerDecl, typeInfo, resolver)
			if businessData != nil {
				response.Body = businessData
				fmt.Printf("[DEBUG] ExtractResponse: 响应函数找到业务数据，类型: %s\n", businessData.Type)
				return response
			}
		} else {
			fmt.Printf("[DEBUG] ExtractResponse: 跳过错误响应函数\n")
		}
	}

	fmt.Printf("[DEBUG] ExtractResponse: 未找到有效的业务数据\n")
	return response
}

// findDirectContextJSONCall 查找Handler中直接的ctx.JSON调用
func (g *GinExtractor) findDirectContextJSONCall(handlerDecl *ast.FuncDecl, contextParam string) *ast.CallExpr {
	if handlerDecl.Body == nil {
		return nil
	}

	var jsonCall *ast.CallExpr

	// 遍历函数体，查找 contextParam.JSON 调用
	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
				// 检查是否为 contextParam.JSON 形式的调用
				if ident, ok := selExpr.X.(*ast.Ident); ok {
					if ident.Name == contextParam && g.isJSONMethod(selExpr.Sel.Name) {
						jsonCall = callExpr
						return false // 找到第一个就停止
					}
				}
			}
		}
		return true
	})

	return jsonCall
}

// extractBusinessDataFromDirectCall 从直接的JSON调用中提取业务数据
func (g *GinExtractor) extractBusinessDataFromDirectCall(jsonCall *ast.CallExpr, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	if jsonCall == nil || len(jsonCall.Args) < 2 {
		return nil
	}

	// 第二个参数是响应数据
	responseArg := jsonCall.Args[1]

	// 使用增强版解析器解析响应数据类型
	return g.parseResponseDataTypeEnhanced(responseArg, typeInfo, resolver)
}

// findResponseFunctionCall 查找Handler中的响应函数调用
func (g *GinExtractor) findResponseFunctionCall(handlerDecl *ast.FuncDecl) *models.ResponseFunction {
	if handlerDecl.Body == nil || g.responseFuncAnalysis == nil {
		return nil
	}

	var foundFunc *models.ResponseFunction

	// 遍历函数体，查找响应函数调用
	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			funcName := g.extractFunctionName(callExpr)
			if funcName != "" {
				// 检查是否在预索引的响应函数中
				for _, responseFunc := range g.responseFuncAnalysis.Functions {
					if responseFunc.FunctionName == funcName ||
						responseFunc.UniqueKey == funcName {
						foundFunc = responseFunc
						return false // 找到就停止
					}
				}
			}
		}
		return true
	})

	return foundFunc
}

// extractBusinessDataFromResponseFunc 从响应函数调用中提取业务数据
func (g *GinExtractor) extractBusinessDataFromResponseFunc(responseFunc *models.ResponseFunction, handlerDecl *ast.FuncDecl, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	if responseFunc == nil {
		return nil
	}

	fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 分析响应函数 %s\n", responseFunc.FunctionName)

	// 查找Handler中对该响应函数的调用
	var responseFuncCall *ast.CallExpr

	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			funcName := g.extractFunctionName(callExpr)
			if funcName == responseFunc.FunctionName {
				responseFuncCall = callExpr
				fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 找到响应函数调用，参数数量: %d\n", len(callExpr.Args))
				return false
			}
		}
		return true
	})

	if responseFuncCall == nil {
		fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 未找到响应函数调用\n")
		return nil
	}

	// 根据响应函数的DataParamIdx提取业务数据参数
	if responseFunc.DataParamIdx >= 0 && len(responseFuncCall.Args) > responseFunc.DataParamIdx {
		dataArg := responseFuncCall.Args[responseFunc.DataParamIdx]
		fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 数据参数索引: %d, 参数类型: %T\n",
			responseFunc.DataParamIdx, dataArg)

		// 获取Handler所在包的类型信息
		handlerPkg := g.findPackageForHandler(handlerDecl)
		if handlerPkg != nil && handlerPkg.TypesInfo != nil {
			fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 使用Handler包的类型信息: %s\n", handlerPkg.PkgPath)
			businessData := g.parseResponseDataTypeEnhanced(dataArg, handlerPkg.TypesInfo, resolver)
			fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: parseResponseDataTypeEnhanced调用完成\n")

			fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 解析得到业务数据: %v (类型: %s, 字段数: %d)\n",
				businessData != nil,
				func() string {
					if businessData != nil {
						return businessData.Type
					} else {
						return "nil"
					}
				}(),
				func() int {
					if businessData != nil {
						return len(businessData.Fields)
					} else {
						return 0
					}
				}())

			fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: BaseResponse存在: %v, DataFieldPath: '%s'\n",
				responseFunc.BaseResponse != nil, responseFunc.DataFieldPath)

			// 如果有基础响应结构，需要合并
			if responseFunc.BaseResponse != nil && responseFunc.DataFieldPath != "" {
				fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 调用合并函数\n")
				return g.mergeBaseResponseWithBusinessData(responseFunc.BaseResponse, businessData, responseFunc.DataFieldPath)
			}

			fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 直接返回业务数据\n")
			return businessData
		} else {
			fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 回退到传入的类型信息\n")
			// 回退到传入的类型信息
			businessData := g.parseResponseDataTypeEnhanced(dataArg, typeInfo, resolver)

			fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 解析得到业务数据(回退): %v (类型: %s, 字段数: %d)\n",
				businessData != nil,
				func() string {
					if businessData != nil {
						return businessData.Type
					} else {
						return "nil"
					}
				}(),
				func() int {
					if businessData != nil {
						return len(businessData.Fields)
					} else {
						return 0
					}
				}())

			fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: BaseResponse存在(回退): %v, DataFieldPath: '%s'\n",
				responseFunc.BaseResponse != nil, responseFunc.DataFieldPath)

			// 如果有基础响应结构，需要合并
			if responseFunc.BaseResponse != nil && responseFunc.DataFieldPath != "" {
				fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 调用合并函数(回退)\n")
				return g.mergeBaseResponseWithBusinessData(responseFunc.BaseResponse, businessData, responseFunc.DataFieldPath)
			}

			fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 直接返回业务数据(回退)\n")
			return businessData
		}
	}

	// 如果没有数据参数，返回基础响应结构
	if responseFunc.BaseResponse != nil {
		fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 返回基础响应结构\n")
		return responseFunc.BaseResponse
	}

	fmt.Printf("[DEBUG] extractBusinessDataFromResponseFunc: 无法解析业务数据\n")
	return nil
}

// findPackageForHandler 查找Handler函数所在的包
func (g *GinExtractor) findPackageForHandler(handlerDecl *ast.FuncDecl) *packages.Package {
	// 在所有包中查找包含该函数的包
	for _, pkg := range g.project.Packages {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl == handlerDecl {
						fmt.Printf("[DEBUG] findPackageForHandler: 找到Handler包: %s\n", pkg.PkgPath)
						return pkg
					}
				}
			}
		}
	}

	fmt.Printf("[DEBUG] findPackageForHandler: 未找到Handler所在的包\n")
	return nil
}

// mergeBaseResponseWithBusinessData 合并基础响应结构和业务数据
func (g *GinExtractor) mergeBaseResponseWithBusinessData(baseResponse *models.FieldInfo, businessData *models.FieldInfo, dataFieldPath string) *models.FieldInfo {
	if baseResponse == nil {
		return businessData
	}
	if businessData == nil {
		return baseResponse
	}

	fmt.Printf("[DEBUG] mergeBaseResponseWithBusinessData: 合并基础响应 %s 和业务数据 %s\n", baseResponse.Type, businessData.Type)

	// 如果业务数据有具体的字段信息，优先返回业务数据
	// 这样可以确保API文档显示的是实际的业务数据结构，而不是通用的Response包装
	if len(businessData.Fields) > 0 {
		fmt.Printf("[DEBUG] mergeBaseResponseWithBusinessData: 业务数据有 %d 个字段，直接返回业务数据\n", len(businessData.Fields))
		return businessData
	}

	// 如果业务数据没有字段信息，但基础响应有完整结构，则进行合并
	if len(baseResponse.Fields) > 0 && dataFieldPath != "" {
		fmt.Printf("[DEBUG] mergeBaseResponseWithBusinessData: 执行字段级合并，数据字段路径: %s\n", dataFieldPath)

		// 创建合并后的结构
		mergedResponse := &models.FieldInfo{
			Name:    baseResponse.Name,
			Type:    baseResponse.Type,
			JsonTag: baseResponse.JsonTag,
			Fields:  make([]models.FieldInfo, 0),
		}

		// 复制基础响应的所有字段
		for _, field := range baseResponse.Fields {
			if field.Name == dataFieldPath {
				// 替换数据字段为实际的业务数据
				mergedField := models.FieldInfo{
					Name:    field.Name,
					JsonTag: field.JsonTag,
					Type:    businessData.Type,
					Fields:  businessData.Fields,
					Items:   businessData.Items,
				}
				mergedResponse.Fields = append(mergedResponse.Fields, mergedField)
				fmt.Printf("[DEBUG] mergeBaseResponseWithBusinessData: 替换数据字段 %s 为 %s\n", field.Name, businessData.Type)
			} else {
				mergedResponse.Fields = append(mergedResponse.Fields, field)
			}
		}

		return mergedResponse
	}

	// 默认返回业务数据
	fmt.Printf("[DEBUG] mergeBaseResponseWithBusinessData: 默认返回业务数据\n")
	return businessData
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

// findContextParameter 查找Context参数名
func (g *GinExtractor) findContextParameter(handlerDecl *ast.FuncDecl) string {
	if handlerDecl.Type.Params == nil {
		return ""
	}

	for _, param := range handlerDecl.Type.Params.List {
		if len(param.Names) > 0 {
			// 检查参数类型是否为gin.Context
			if starExpr, ok := param.Type.(*ast.StarExpr); ok {
				if selExpr, ok := starExpr.X.(*ast.SelectorExpr); ok {
					if ident, ok := selExpr.X.(*ast.Ident); ok {
						if ident.Name == "gin" && selExpr.Sel.Name == "Context" {
							return param.Names[0].Name
						}
					}
				}
			}
		}
	}
	return ""
}

// findDirectJSONCalls 查找所有直接的JSON调用
func (g *GinExtractor) findDirectJSONCalls(handlerDecl *ast.FuncDecl, contextParam string, typeInfo *types.Info) []*models.DirectJSONCall {
	var directCalls []*models.DirectJSONCall

	// 遍历函数体，查找所有JSON调用
	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
				methodName := selExpr.Sel.Name

				// 检查是否为Context的JSON方法调用
				if ident, ok := selExpr.X.(*ast.Ident); ok && ident.Name == contextParam {
					if g.isJSONMethod(methodName) {
						// 分析分支上下文
						branchInfo := g.analyzeBranchContext(node, handlerDecl.Body)

						directCall := &models.DirectJSONCall{
							CallExpr:    callExpr,
							ContextName: contextParam,
							Method:      methodName,
							LineNumber:  g.getLineNumber(callExpr),
							IsInBranch:  branchInfo != nil,
							BranchInfo:  branchInfo,
						}

						// 提取状态码和响应数据参数
						if len(callExpr.Args) > 0 {
							directCall.StatusCode = callExpr.Args[0]
						}
						if len(callExpr.Args) > 1 {
							directCall.ResponseData = callExpr.Args[1]
						}

						directCalls = append(directCalls, directCall)
						fmt.Printf("[DEBUG] findDirectJSONCalls: 找到直接调用 %s.%s 在第 %d 行\n",
							contextParam, methodName, directCall.LineNumber)
					}
				}
			}
		}
		return true
	})

	return directCalls
}

// parseDirectJSONCall 解析直接JSON调用
func (g *GinExtractor) parseDirectJSONCall(call *models.DirectJSONCall, typeInfo *types.Info, resolver TypeResolver) *models.ResponseDetail {
	if call.CallExpr == nil {
		return nil
	}

	detail := &models.ResponseDetail{
		CallSite: &models.CallSiteInfo{
			LineNumber: call.LineNumber,
			Method:     call.Method,
			IsInBranch: call.IsInBranch,
			BranchInfo: call.BranchInfo,
		},
	}

	// 解析状态码
	if call.StatusCode != nil {
		statusCode := g.extractStatusCode(call.StatusCode, typeInfo)
		detail.StatusCode = statusCode
		detail.Description = g.getStatusDescription(statusCode)
	}

	// 解析响应数据类型
	if call.ResponseData != nil {
		schema := g.parseResponseDataType(call.ResponseData, typeInfo, resolver)
		detail.Schema = schema
	}

	// 设置条件描述
	if call.BranchInfo != nil {
		detail.Condition = call.BranchInfo.Condition
		if call.BranchInfo.IsErrorPath {
			detail.Description += " (错误响应)"
		}
	}

	return detail
}

// findSuccessResponseCalls 查找成功响应的调用链（忽略错误响应）
func (g *GinExtractor) findSuccessResponseCalls(handlerDecl *ast.FuncDecl, contextParam string, typeInfo *types.Info) []*models.CallChain {
	var callChains []*models.CallChain

	// 查找所有以Context为参数的函数调用
	contextCalls := g.findContextFunctionCalls(handlerDecl, contextParam, typeInfo)

	for _, contextCall := range contextCalls {
		// 只处理成功响应函数，跳过错误响应函数
		if g.isErrorResponseFunction(contextCall.FuncName) {
			fmt.Printf("[DEBUG] findSuccessResponseCalls: 跳过错误响应函数 %s\n", contextCall.FuncName)
			continue
		}

		chain := &models.CallChain{
			MaxDepth:    5, // 最大递归深度
			Visited:     make(map[string]bool),
			TraceResult: "unknown",
		}

		// 追踪调用链
		if g.traceCallChain(contextCall, chain, typeInfo) {
			callChains = append(callChains, chain)
		}
	}

	return callChains
}

// findEncapsulatedJSONCalls 查找封装的JSON调用（保留原方法用于其他地方）
func (g *GinExtractor) findEncapsulatedJSONCalls(handlerDecl *ast.FuncDecl, contextParam string, typeInfo *types.Info) []*models.CallChain {
	var callChains []*models.CallChain

	// 查找所有以Context为参数的函数调用
	contextCalls := g.findContextFunctionCalls(handlerDecl, contextParam, typeInfo)

	for _, contextCall := range contextCalls {
		chain := &models.CallChain{
			MaxDepth:    5, // 最大递归深度
			Visited:     make(map[string]bool),
			TraceResult: "unknown",
		}

		// 追踪调用链
		if g.traceCallChain(contextCall, chain, typeInfo) {
			callChains = append(callChains, chain)
		}
	}

	return callChains
}

// isJSONMethod 检查是否为JSON相关方法
func (g *GinExtractor) isJSONMethod(methodName string) bool {
	jsonMethods := []string{"JSON", "IndentedJSON", "SecureJSON", "JSONP", "String", "HTML", "XML", "YAML"}
	for _, method := range jsonMethods {
		if methodName == method {
			return true
		}
	}
	return false
}

// analyzeBranchContext 分析分支上下文
func (g *GinExtractor) analyzeBranchContext(node ast.Node, body *ast.BlockStmt) *models.BranchContext {
	// 查找包含当前节点的父节点
	var parent ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		// 检查当前节点是否在某个分支结构中
		switch p := n.(type) {
		case *ast.IfStmt:
			if g.containsNode(p, node) {
				parent = p
				return false
			}
		case *ast.SwitchStmt:
			if g.containsNode(p, node) {
				parent = p
				return false
			}
		case *ast.TypeSwitchStmt:
			if g.containsNode(p, node) {
				parent = p
				return false
			}
		}
		return true
	})

	if parent == nil {
		return nil
	}

	// 根据父节点类型创建分支上下文
	switch p := parent.(type) {
	case *ast.IfStmt:
		return &models.BranchContext{
			Type:        "if",
			Condition:   g.extractConditionString(p.Cond),
			IsErrorPath: g.isErrorCondition(p.Cond),
		}
	case *ast.SwitchStmt:
		return &models.BranchContext{
			Type:      "switch",
			Condition: "switch语句",
		}
	case *ast.TypeSwitchStmt:
		return &models.BranchContext{
			Type:      "type_switch",
			Condition: "类型switch语句",
		}
	}

	return nil
}

// getLineNumber 获取AST节点的行号
func (g *GinExtractor) getLineNumber(node ast.Node) int {
	if node == nil {
		return 0
	}
	// 在生产环境中，需要通过token.FileSet来获取准确的行号
	// 这里简化处理，返回Position的Offset作为近似行号
	return int(node.Pos())
}

// extractStatusCode 提取状态码
func (g *GinExtractor) extractStatusCode(expr ast.Expr, typeInfo *types.Info) int {
	switch e := expr.(type) {
	case *ast.BasicLit:
		// 直接的数字字面量
		if e.Kind.String() == "INT" {
			if val := e.Value; val != "" {
				// 简化处理：解析常见的HTTP状态码
				switch val {
				case "200":
					return 200
				case "201":
					return 201
				case "400":
					return 400
				case "401":
					return 401
				case "403":
					return 403
				case "404":
					return 404
				case "500":
					return 500
				}
			}
		}
	case *ast.SelectorExpr:
		// http.StatusOK 等常量
		if ident, ok := e.X.(*ast.Ident); ok {
			if ident.Name == "http" {
				switch e.Sel.Name {
				case "StatusOK":
					return 200
				case "StatusCreated":
					return 201
				case "StatusBadRequest":
					return 400
				case "StatusUnauthorized":
					return 401
				case "StatusForbidden":
					return 403
				case "StatusNotFound":
					return 404
				case "StatusInternalServerError":
					return 500
				}
			}
		}
	}

	// 默认返回200
	return 200
}

// getStatusDescription 获取状态码描述
func (g *GinExtractor) getStatusDescription(statusCode int) string {
	descriptions := map[int]string{
		200: "成功",
		201: "创建成功",
		400: "请求错误",
		401: "未授权",
		403: "禁止访问",
		404: "未找到",
		500: "服务器内部错误",
	}

	if desc, exists := descriptions[statusCode]; exists {
		return desc
	}
	return fmt.Sprintf("HTTP %d", statusCode)
}

// parseResponseDataType 解析响应数据类型
func (g *GinExtractor) parseResponseDataType(expr ast.Expr, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	if expr == nil {
		return &models.FieldInfo{Type: "unknown"}
	}

	fmt.Printf("[DEBUG] parseResponseDataType: 开始解析响应数据类型\n")

	// 优先从类型信息解析
	if typ := typeInfo.TypeOf(expr); typ != nil {
		fmt.Printf("[DEBUG] parseResponseDataType: 从类型信息解析，类型: %s\n", typ.String())
		result := resolver(typ)
		if result != nil && result.Type != "unknown" {
			return result
		}
	}

	// 从表达式结构分析
	switch e := expr.(type) {
	case *ast.CompositeLit:
		fmt.Printf("[DEBUG] parseResponseDataType: 解析结构体字面量\n")
		return g.parseCompositeLiteral(e, typeInfo, resolver)
	case *ast.CallExpr:
		fmt.Printf("[DEBUG] parseResponseDataType: 解析函数调用返回值\n")
		return g.parseFunctionCallReturn(e, typeInfo, resolver)
	case *ast.Ident:
		fmt.Printf("[DEBUG] parseResponseDataType: 解析变量引用: %s\n", e.Name)
		return g.parseVariableReference(e, typeInfo, resolver)
	case *ast.SelectorExpr:
		fmt.Printf("[DEBUG] parseResponseDataType: 解析选择器表达式\n")
		return g.parseSelectorExpression(e, typeInfo, resolver)
	default:
		fmt.Printf("[DEBUG] parseResponseDataType: 未识别的表达式类型: %T\n", expr)
		return &models.FieldInfo{Type: "interface{}"}
	}
}

// parseResponseDataTypeEnhanced 增强版响应数据类型解析，更积极地使用类型解析器
func (g *GinExtractor) parseResponseDataTypeEnhanced(expr ast.Expr, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	if expr == nil {
		return &models.FieldInfo{Type: "unknown"}
	}

	fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 开始解析响应数据类型\n")

	// 第一步：从类型信息解析，更积极地处理结果
	if typ := typeInfo.TypeOf(expr); typ != nil {
		fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 类型信息: %s\n", typ.String())

		// 调用类型解析器
		result := resolver(typ)
		if result != nil {
			fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 解析器返回类型: %s, 字段数: %d\n",
				result.Type, len(result.Fields))

			// 即使类型是"unknown"，如果有字段信息也返回
			if result.Type != "unknown" || len(result.Fields) > 0 {
				return result
			}
		}
	}

	// 第二步：表达式结构分析，更详细的处理
	switch e := expr.(type) {
	case *ast.CompositeLit:
		fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 解析结构体字面量\n")
		result := g.parseCompositeLiteralEnhanced(e, typeInfo, resolver)
		if result != nil && (result.Type != "unknown" || len(result.Fields) > 0) {
			return result
		}

	case *ast.CallExpr:
		fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 解析函数调用返回值\n")
		result := g.parseFunctionCallReturnEnhanced(e, typeInfo, resolver)
		if result != nil && (result.Type != "unknown" || len(result.Fields) > 0) {
			return result
		}

	case *ast.Ident:
		fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 解析变量引用: %s\n", e.Name)
		result := g.parseVariableReferenceEnhanced(e, typeInfo, resolver)
		if result != nil && (result.Type != "unknown" || len(result.Fields) > 0) {
			return result
		}

	case *ast.SelectorExpr:
		fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 解析选择器表达式\n")
		result := g.parseSelectorExpressionEnhanced(e, typeInfo, resolver)
		if result != nil && (result.Type != "unknown" || len(result.Fields) > 0) {
			return result
		}

	case *ast.UnaryExpr:
		// 处理取地址等一元表达式
		if e.Op.String() == "&" {
			fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 解析取地址表达式\n")
			return g.parseResponseDataTypeEnhanced(e.X, typeInfo, resolver)
		}

	default:
		fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 未识别的表达式类型: %T\n", expr)
	}

	// 最后返回默认值
	fmt.Printf("[DEBUG] parseResponseDataTypeEnhanced: 无法解析，返回默认值\n")
	return &models.FieldInfo{Type: "interface{}"}
}

// containsNode 检查父节点是否包含子节点
func (g *GinExtractor) containsNode(parent, child ast.Node) bool {
	found := false
	ast.Inspect(parent, func(n ast.Node) bool {
		if n == child {
			found = true
			return false
		}
		return true
	})
	return found
}

// extractConditionString 提取条件表达式的字符串表示
func (g *GinExtractor) extractConditionString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}

	// 简化处理：返回表达式的基本描述
	switch e := expr.(type) {
	case *ast.BinaryExpr:
		left := g.extractConditionString(e.X)
		right := g.extractConditionString(e.Y)
		op := e.Op.String()
		return fmt.Sprintf("%s %s %s", left, op, right)
	case *ast.Ident:
		return e.Name
	case *ast.BasicLit:
		return e.Value
	default:
		return "条件表达式"
	}
}

// isErrorCondition 判断是否为错误条件
func (g *GinExtractor) isErrorCondition(expr ast.Expr) bool {
	// 简单的错误条件判断
	conditionStr := g.extractConditionString(expr)
	errorKeywords := []string{"err", "error", "Error", "!=", "nil"}

	for _, keyword := range errorKeywords {
		if strings.Contains(conditionStr, keyword) {
			return true
		}
	}
	return false
}

// findContextFunctionCalls 查找所有以Context为参数的函数调用
func (g *GinExtractor) findContextFunctionCalls(handlerDecl *ast.FuncDecl, contextParam string, typeInfo *types.Info) []*models.FunctionCall {
	var calls []*models.FunctionCall

	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			// 检查调用参数中是否包含context参数
			hasContextParam := false
			for _, arg := range callExpr.Args {
				if ident, ok := arg.(*ast.Ident); ok && ident.Name == contextParam {
					hasContextParam = true
					break
				}
			}

			// 或者检查是否为常见的响应封装函数（即使没有直接传递context参数）
			funcName := g.extractFunctionName(callExpr)
			isResponseFunction := g.isCommonResponseFunction(funcName)

			if hasContextParam || isResponseFunction {
				funcCall := &models.FunctionCall{
					CallSite:   callExpr,
					FuncName:   funcName,
					IsExternal: false,
				}

				if funcCall.FuncName != "" {
					calls = append(calls, funcCall)
					fmt.Printf("[DEBUG] findContextFunctionCalls: 找到函数调用 %s\n", funcCall.FuncName)
				}
			}
		}
		return true
	})

	return calls
}

// isCommonResponseFunction 检查是否为常见的响应封装函数
func (g *GinExtractor) isCommonResponseFunction(funcName string) bool {
	commonResponseFunctions := []string{
		"ApiResponseOK",
		"ApiResponseErr",
		"ApiResponse",
		"SuccessResponse",
		"ErrorResponse",
		"Response",
		"SendResponse",
		"WriteResponse",
		"JsonResponse",
		"ApiSuccess",
		"ApiError",
		"ApiResult",
		"Result",
		"Success",
		"Error",
		"ResponseOK",
		"ResponseError",
		"ResponseJSON",
	}

	for _, commonFunc := range commonResponseFunctions {
		if funcName == commonFunc {
			fmt.Printf("[DEBUG] isCommonResponseFunction: 识别到常见响应函数 %s\n", funcName)
			return true
		}
	}
	return false
}

// traceCallChain 追踪调用链
func (g *GinExtractor) traceCallChain(call *models.FunctionCall, chain *models.CallChain, typeInfo *types.Info) bool {
	if len(chain.Calls) >= chain.MaxDepth {
		chain.TraceResult = "max_depth_reached"
		return false
	}

	// 添加当前调用到链中
	chain.Calls = append(chain.Calls, *call)

	// 如果这是成功响应函数，直接创建虚拟的JSON调用，使用原始参数类型
	if g.isSuccessResponseFunction(call.FuncName) {
		fmt.Printf("[DEBUG] traceCallChain: 识别为成功响应函数 %s，直接处理\n", call.FuncName)

		// 从调用点获取原始参数类型信息
		responseData := g.extractResponseDataFromCall(call.CallSite, typeInfo)

		// 创建虚拟的JSON调用
		chain.FinalJSON = &models.DirectJSONCall{
			CallExpr:     call.CallSite,
			ContextName:  "c", // 假设context参数名为c
			Method:       "JSON",
			LineNumber:   g.getLineNumber(call.CallSite),
			IsInBranch:   false,
			StatusCode:   nil, // 将在上层设置
			ResponseData: responseData,
		}
		chain.TraceResult = "found"
		return true
	}

	// 查找函数定义
	funcDecl := g.findFunctionDefinition(call.FuncName)
	if funcDecl == nil {
		fmt.Printf("[DEBUG] traceCallChain: 未找到函数定义 %s\n", call.FuncName)
		chain.TraceResult = "function_not_found"
		return false
	}

	fmt.Printf("[DEBUG] traceCallChain: 开始分析函数 %s\n", call.FuncName)

	// 查找函数内部的Context参数名
	contextParam := g.findContextParameter(funcDecl)
	if contextParam == "" {
		fmt.Printf("[DEBUG] traceCallChain: 函数 %s 没有Context参数\n", call.FuncName)
		// 尝试查找通过参数传递的context
		contextParam = g.inferContextFromCall(call, funcDecl)
	}

	if contextParam != "" {
		// 在函数内部查找直接的JSON调用
		directCalls := g.findDirectJSONCalls(funcDecl, contextParam, typeInfo)
		if len(directCalls) > 0 {
			fmt.Printf("[DEBUG] traceCallChain: 在函数 %s 中找到 %d 个直接JSON调用\n", call.FuncName, len(directCalls))
			// 取第一个作为最终调用（可以根据需要改进）
			chain.FinalJSON = directCalls[0]
			chain.TraceResult = "found"
			return true
		}

		// 如果没有直接调用，继续查找嵌套的函数调用
		nestedCalls := g.findContextFunctionCalls(funcDecl, contextParam, typeInfo)
		for _, nestedCall := range nestedCalls {
			// 避免循环调用
			if !chain.Visited[nestedCall.FuncName] {
				chain.Visited[nestedCall.FuncName] = true
				if g.traceCallChain(nestedCall, chain, typeInfo) {
					return true
				}
			}
		}
	}

	chain.TraceResult = "no_json_found"
	return false
}

// findFunctionDefinition 查找函数定义
func (g *GinExtractor) findFunctionDefinition(funcName string) *ast.FuncDecl {
	// 支持包名.函数名的格式
	parts := strings.Split(funcName, ".")
	targetFuncName := funcName
	if len(parts) > 1 {
		targetFuncName = parts[len(parts)-1]
	}

	for _, pkg := range g.project.Packages {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if funcDecl.Name.Name == targetFuncName {
						fmt.Printf("[DEBUG] findFunctionDefinition: 找到函数定义 %s\n", funcName)
						return funcDecl
					}
				}
			}
		}
	}
	return nil
}

// inferContextFromCall 从调用点推断context参数名
func (g *GinExtractor) inferContextFromCall(call *models.FunctionCall, funcDecl *ast.FuncDecl) string {
	// 检查函数的第一个参数是否可能是context
	if funcDecl.Type.Params != nil && len(funcDecl.Type.Params.List) > 0 {
		firstParam := funcDecl.Type.Params.List[0]
		if len(firstParam.Names) > 0 {
			paramName := firstParam.Names[0].Name
			// 常见的context参数名
			if strings.Contains(strings.ToLower(paramName), "ctx") ||
				strings.Contains(strings.ToLower(paramName), "context") ||
				paramName == "c" {
				fmt.Printf("[DEBUG] inferContextFromCall: 推断context参数名为 %s\n", paramName)
				return paramName
			}
		}
	}
	return ""
}

// extractFunctionName 提取函数名
func (g *GinExtractor) extractFunctionName(callExpr *ast.CallExpr) string {
	switch fun := callExpr.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if ident, ok := fun.X.(*ast.Ident); ok {
			return fmt.Sprintf("%s.%s", ident.Name, fun.Sel.Name)
		}
		return fun.Sel.Name
	default:
		return ""
	}
}

// parseCompositeLiteral 解析结构体字面量
func (g *GinExtractor) parseCompositeLiteral(lit *ast.CompositeLit, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	fmt.Printf("[DEBUG] parseCompositeLiteral: 开始解析结构体字面量\n")

	// 优先从类型信息获取
	if typ := typeInfo.TypeOf(lit); typ != nil {
		fmt.Printf("[DEBUG] parseCompositeLiteral: 从类型信息解析，类型: %s\n", typ.String())
		result := resolver(typ)
		if result != nil && len(result.Fields) > 0 {
			fmt.Printf("[DEBUG] parseCompositeLiteral: 成功解析，包含 %d 个字段\n", len(result.Fields))
			return result
		}
	}

	// 尝试从结构体类型表达式分析
	if lit.Type != nil {
		fmt.Printf("[DEBUG] parseCompositeLiteral: 分析类型表达式\n")
		if typ := typeInfo.TypeOf(lit.Type); typ != nil {
			result := resolver(typ)
			if result != nil {
				fmt.Printf("[DEBUG] parseCompositeLiteral: 从类型表达式解析成功\n")
				return result
			}
		}
	}

	fmt.Printf("[DEBUG] parseCompositeLiteral: 回退到基本结构体类型\n")
	return &models.FieldInfo{Type: "struct"}
}

// parseFunctionCallReturn 解析函数调用返回值
func (g *GinExtractor) parseFunctionCallReturn(call *ast.CallExpr, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	if typ := typeInfo.TypeOf(call); typ != nil {
		return resolver(typ)
	}

	return &models.FieldInfo{
		Type: "unknown",
	}
}

// parseVariableReference 解析变量引用
func (g *GinExtractor) parseVariableReference(ident *ast.Ident, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	fmt.Printf("[DEBUG] parseVariableReference: 解析变量 %s\n", ident.Name)

	if obj := typeInfo.ObjectOf(ident); obj != nil {
		fmt.Printf("[DEBUG] parseVariableReference: 变量类型: %s\n", obj.Type().String())
		result := resolver(obj.Type())
		if result != nil {
			fmt.Printf("[DEBUG] parseVariableReference: 解析成功，类型: %s, 字段数: %d\n",
				result.Type, len(result.Fields))
			return result
		}
	}

	fmt.Printf("[DEBUG] parseVariableReference: 无法解析变量 %s\n", ident.Name)
	return &models.FieldInfo{Type: "unknown"}
}

// isErrorResponseFunction 检查是否为错误响应函数
func (g *GinExtractor) isErrorResponseFunction(funcName string) bool {
	errorFunctions := []string{
		"ApiResponseErr",
		"ErrorResponse",
		"ApiError",
		"Error",
		"ResponseError",
		"SendError",
		"WriteError",
		"FailResponse",
		"ApiResponseError",
		"ApiResponseFail",
	}

	for _, errorFunc := range errorFunctions {
		if funcName == errorFunc {
			return true
		}
	}
	return false
}

// isSuccessResponseFunction 检查是否为成功响应函数
func (g *GinExtractor) isSuccessResponseFunction(funcName string) bool {
	successFunctions := []string{
		"ApiResponseOK",
		"SuccessResponse",
		"ApiSuccess",
		"Success",
		"ResponseOK",
		"SendSuccess",
		"WriteSuccess",
		"ApiResponseSuccess",
		"ApiOK",
		"OK",
		"JSON",
	}

	for _, successFunc := range successFunctions {
		if funcName == successFunc {
			return true
		}
	}
	return false
}

// parseSelectorExpression 解析选择器表达式
func (g *GinExtractor) parseSelectorExpression(selExpr *ast.SelectorExpr, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	// 尝试从类型信息获取
	if typ := typeInfo.TypeOf(selExpr); typ != nil {
		return resolver(typ)
	}

	// 构造选择器的描述
	if ident, ok := selExpr.X.(*ast.Ident); ok {
		return &models.FieldInfo{
			Type: fmt.Sprintf("%s.%s", ident.Name, selExpr.Sel.Name),
		}
	}

	return &models.FieldInfo{Type: "unknown"}
}

// extractResponseDataFromCall 从响应函数调用中提取响应数据参数
func (g *GinExtractor) extractResponseDataFromCall(callExpr *ast.CallExpr, typeInfo *types.Info) ast.Expr {
	if callExpr == nil || len(callExpr.Args) < 2 {
		return nil
	}

	// 对于大多数响应函数，第一个参数是context，第二个参数是响应数据
	// 例如: ApiResponseOK(c, data) 或 ApiResponseErr(c, error)
	responseDataArg := callExpr.Args[1]

	fmt.Printf("[DEBUG] extractResponseDataFromCall: 提取响应数据参数\n")
	return responseDataArg
}

// extractBusinessDataFromJSONCall 从JSON调用中提取业务数据字段信息
func (g *GinExtractor) extractBusinessDataFromJSONCall(call *models.DirectJSONCall, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	if call == nil || call.CallExpr == nil {
		return nil
	}

	fmt.Printf("[DEBUG] extractBusinessDataFromJSONCall: 开始提取业务数据\n")

	// 从JSON调用的第二个参数（响应数据）中提取类型信息
	if call.ResponseData != nil {
		businessData := g.parseResponseDataTypeEnhanced(call.ResponseData, typeInfo, resolver)
		if businessData != nil && businessData.Type != "unknown" {
			fmt.Printf("[DEBUG] extractBusinessDataFromJSONCall: 成功提取业务数据，类型: %s, 字段数: %d\n",
				businessData.Type, len(businessData.Fields))
			return businessData
		}
	}

	// 如果ResponseData为空，尝试从调用表达式的参数中提取
	if len(call.CallExpr.Args) > 1 {
		responseArg := call.CallExpr.Args[1]
		businessData := g.parseResponseDataTypeEnhanced(responseArg, typeInfo, resolver)
		if businessData != nil && businessData.Type != "unknown" {
			fmt.Printf("[DEBUG] extractBusinessDataFromJSONCall: 从调用参数提取业务数据，类型: %s\n", businessData.Type)
			return businessData
		}
	}

	fmt.Printf("[DEBUG] extractBusinessDataFromJSONCall: 未能提取有效的业务数据\n")
	return nil
}

// parseCompositeLiteralEnhanced 增强版结构体字面量解析
func (g *GinExtractor) parseCompositeLiteralEnhanced(lit *ast.CompositeLit, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	fmt.Printf("[DEBUG] parseCompositeLiteralEnhanced: 开始解析结构体字面量\n")

	// 优先从类型信息获取
	if typ := typeInfo.TypeOf(lit); typ != nil {
		fmt.Printf("[DEBUG] parseCompositeLiteralEnhanced: 从类型信息解析，类型: %s\n", typ.String())
		result := resolver(typ)
		if result != nil && (result.Type != "unknown" || len(result.Fields) > 0) {
			fmt.Printf("[DEBUG] parseCompositeLiteralEnhanced: 成功解析，类型: %s, 字段数: %d\n", result.Type, len(result.Fields))
			return result
		}
	}

	// 尝试从结构体类型表达式分析
	if lit.Type != nil {
		fmt.Printf("[DEBUG] parseCompositeLiteralEnhanced: 分析类型表达式\n")
		if typ := typeInfo.TypeOf(lit.Type); typ != nil {
			result := resolver(typ)
			if result != nil && (result.Type != "unknown" || len(result.Fields) > 0) {
				fmt.Printf("[DEBUG] parseCompositeLiteralEnhanced: 从类型表达式解析成功\n")
				return result
			}
		}
	}

	fmt.Printf("[DEBUG] parseCompositeLiteralEnhanced: 回退到基本结构体类型\n")
	return &models.FieldInfo{Type: "struct"}
}

// parseFunctionCallReturnEnhanced 增强版函数调用返回值解析
func (g *GinExtractor) parseFunctionCallReturnEnhanced(call *ast.CallExpr, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	if typ := typeInfo.TypeOf(call); typ != nil {
		fmt.Printf("[DEBUG] parseFunctionCallReturnEnhanced: 函数返回类型: %s\n", typ.String())
		result := resolver(typ)
		if result != nil {
			return result
		}
	}

	return &models.FieldInfo{Type: "unknown"}
}

// parseVariableReferenceEnhanced 增强版变量引用解析
func (g *GinExtractor) parseVariableReferenceEnhanced(ident *ast.Ident, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	fmt.Printf("[DEBUG] parseVariableReferenceEnhanced: 解析变量 %s\n", ident.Name)

	obj := typeInfo.ObjectOf(ident)
	if obj == nil {
		fmt.Printf("[DEBUG] parseVariableReferenceEnhanced: 无法找到变量 %s 的对象信息\n", ident.Name)
		return &models.FieldInfo{Type: "unknown"}
	}

	fmt.Printf("[DEBUG] parseVariableReferenceEnhanced: 变量 %s 类型: %s, 对象类型: %T\n",
		ident.Name, obj.Type().String(), obj)

	// 使用增强的类型解析器，利用TypeRegistry
	result := g.resolveTypeWithRegistry(obj.Type(), resolver)
	if result != nil && result.Type != "unknown" {
		fmt.Printf("[DEBUG] parseVariableReferenceEnhanced: TypeRegistry解析成功，类型: %s, 字段数: %d\n",
			result.Type, len(result.Fields))
		return result
	}

	// 回退到默认解析器
	result = resolver(obj.Type())
	if result != nil {
		fmt.Printf("[DEBUG] parseVariableReferenceEnhanced: 默认解析器结果，类型: %s, 字段数: %d\n",
			result.Type, len(result.Fields))
		return result
	}

	fmt.Printf("[DEBUG] parseVariableReferenceEnhanced: 所有解析器都失败，变量 %s\n", ident.Name)
	return &models.FieldInfo{Type: "unknown"}
}

// resolveTypeWithRegistry 使用TypeRegistry增强类型解析
func (g *GinExtractor) resolveTypeWithRegistry(typ types.Type, resolver TypeResolver) *models.FieldInfo {
	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	// 处理命名类型
	if named, ok := typ.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			// 构建FullType
			fullType := parser.FullType{
				PackagePath: obj.Pkg().Path(),
				TypeName:    obj.Name(),
			}

			fmt.Printf("[DEBUG] resolveTypeWithRegistry: 查找类型 %s.%s\n", fullType.PackagePath, fullType.TypeName)

			// 从TypeRegistry中查找类型定义
			if typeSpec := g.project.GetTypeSpec(fullType); typeSpec != nil {
				fmt.Printf("[DEBUG] resolveTypeWithRegistry: 找到类型定义 %s\n", typeSpec.Name.Name)
				return g.parseTypeSpecToFieldInfo(typeSpec, fullType.PackagePath, resolver)
			}
		}
	}

	// 处理切片类型
	if slice, ok := typ.(*types.Slice); ok {
		fmt.Printf("[DEBUG] resolveTypeWithRegistry: 处理切片类型\n")
		elementType := g.resolveTypeWithRegistry(slice.Elem(), resolver)
		if elementType != nil {
			return &models.FieldInfo{
				Type:  "[]" + elementType.Type,
				Items: elementType,
			}
		}
	}

	// 处理数组类型
	if array, ok := typ.(*types.Array); ok {
		fmt.Printf("[DEBUG] resolveTypeWithRegistry: 处理数组类型\n")
		elementType := g.resolveTypeWithRegistry(array.Elem(), resolver)
		if elementType != nil {
			return &models.FieldInfo{
				Type:  fmt.Sprintf("[%d]%s", array.Len(), elementType.Type),
				Items: elementType,
			}
		}
	}

	// 处理结构体类型
	if structType, ok := typ.(*types.Struct); ok {
		fmt.Printf("[DEBUG] resolveTypeWithRegistry: 处理匿名结构体类型\n")
		return g.parseStructTypeToFieldInfo(structType)
	}

	return nil
}

// parseTypeSpecToFieldInfo 将AST类型规范转换为FieldInfo
func (g *GinExtractor) parseTypeSpecToFieldInfo(typeSpec *ast.TypeSpec, packagePath string, resolver TypeResolver) *models.FieldInfo {
	switch t := typeSpec.Type.(type) {
	case *ast.StructType:
		fmt.Printf("[DEBUG] parseTypeSpecToFieldInfo: 解析结构体 %s\n", typeSpec.Name.Name)
		fieldInfo := &models.FieldInfo{
			Name:   typeSpec.Name.Name,
			Type:   typeSpec.Name.Name,
			Fields: make([]models.FieldInfo, 0),
		}

		// 解析结构体字段
		if t.Fields != nil {
			for _, field := range t.Fields.List {
				for _, name := range field.Names {
					fieldType := g.parseFieldType(field.Type, packagePath)
					jsonTag := g.extractJSONTag(field.Tag)

					fieldInfo.Fields = append(fieldInfo.Fields, models.FieldInfo{
						Name:    name.Name,
						JsonTag: jsonTag,
						Type:    fieldType,
					})

					fmt.Printf("[DEBUG] parseTypeSpecToFieldInfo: 添加字段 %s, 类型: %s, JSON标签: %s\n",
						name.Name, fieldType, jsonTag)
				}
			}
		}

		return fieldInfo

	case *ast.ArrayType:
		fmt.Printf("[DEBUG] parseTypeSpecToFieldInfo: 解析数组类型 %s\n", typeSpec.Name.Name)
		elementType := g.parseFieldType(t.Elt, packagePath)
		return &models.FieldInfo{
			Name: typeSpec.Name.Name,
			Type: "[]" + elementType,
			Items: &models.FieldInfo{
				Type: elementType,
			},
		}

	case *ast.Ident:
		// 基本类型或其他命名类型
		fmt.Printf("[DEBUG] parseTypeSpecToFieldInfo: 解析基本类型 %s -> %s\n", typeSpec.Name.Name, t.Name)
		return &models.FieldInfo{
			Name: typeSpec.Name.Name,
			Type: t.Name,
		}

	default:
		fmt.Printf("[DEBUG] parseTypeSpecToFieldInfo: 未支持的类型 %T\n", t)
		return &models.FieldInfo{
			Name: typeSpec.Name.Name,
			Type: typeSpec.Name.Name,
		}
	}
}

// parseStructTypeToFieldInfo 解析匿名结构体类型
func (g *GinExtractor) parseStructTypeToFieldInfo(structType *types.Struct) *models.FieldInfo {
	fieldInfo := &models.FieldInfo{
		Type:   "struct",
		Fields: make([]models.FieldInfo, 0),
	}

	for i := 0; i < structType.NumFields(); i++ {
		field := structType.Field(i)
		tag := structType.Tag(i)

		jsonTag := g.parseJSONTagFromString(tag)

		fieldInfo.Fields = append(fieldInfo.Fields, models.FieldInfo{
			Name:    field.Name(),
			JsonTag: jsonTag,
			Type:    field.Type().String(),
		})

		fmt.Printf("[DEBUG] parseStructTypeToFieldInfo: 添加匿名结构体字段 %s, 类型: %s\n",
			field.Name(), field.Type().String())
	}

	return fieldInfo
}

// parseFieldType 解析字段类型
func (g *GinExtractor) parseFieldType(fieldType ast.Expr, packagePath string) string {
	switch t := fieldType.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + g.parseFieldType(t.X, packagePath)
	case *ast.ArrayType:
		return "[]" + g.parseFieldType(t.Elt, packagePath)
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok {
			return pkg.Name + "." + t.Sel.Name
		}
		return t.Sel.Name
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "interface{}"
		}
		return "interface"
	default:
		return "unknown"
	}
}

// extractJSONTag 提取JSON标签
func (g *GinExtractor) extractJSONTag(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}

	tagValue := tag.Value
	if len(tagValue) < 2 {
		return ""
	}

	// 移除引号
	tagValue = tagValue[1 : len(tagValue)-1]

	return g.parseJSONTagFromString(tagValue)
}

// parseJSONTagFromString 从标签字符串中解析JSON标签
func (g *GinExtractor) parseJSONTagFromString(tagStr string) string {
	// 查找json:"..."部分
	jsonPrefix := `json:"`
	jsonStart := strings.Index(tagStr, jsonPrefix)
	if jsonStart == -1 {
		return ""
	}

	jsonStart += len(jsonPrefix)
	jsonEnd := strings.Index(tagStr[jsonStart:], `"`)
	if jsonEnd == -1 {
		return ""
	}

	jsonTag := tagStr[jsonStart : jsonStart+jsonEnd]

	// 处理omitempty等选项
	if commaIndex := strings.Index(jsonTag, ","); commaIndex != -1 {
		jsonTag = jsonTag[:commaIndex]
	}

	return jsonTag
}

// parseSelectorExpressionEnhanced 增强版选择器表达式解析
func (g *GinExtractor) parseSelectorExpressionEnhanced(selExpr *ast.SelectorExpr, typeInfo *types.Info, resolver TypeResolver) *models.FieldInfo {
	// 尝试从类型信息获取
	if typ := typeInfo.TypeOf(selExpr); typ != nil {
		fmt.Printf("[DEBUG] parseSelectorExpressionEnhanced: 选择器类型: %s\n", typ.String())
		result := resolver(typ)
		if result != nil {
			return result
		}
	}

	// 构造选择器的描述
	if ident, ok := selExpr.X.(*ast.Ident); ok {
		return &models.FieldInfo{
			Type: fmt.Sprintf("%s.%s", ident.Name, selExpr.Sel.Name),
		}
	}

	return &models.FieldInfo{Type: "unknown"}
}
