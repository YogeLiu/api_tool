package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"log"
	"os"
	"reflect"
	"strings"

	"golang.org/x/tools/go/packages"
)

// API Schema 结构定义 (符合技术规范)
type APISchema struct {
	Type        string                `json:"type"`
	Properties  map[string]*APISchema `json:"properties,omitempty"`
	Items       *APISchema            `json:"items,omitempty"`
	Description string                `json:"description,omitempty"`
	JSONTag     string                `json:"json_tag,omitempty"`
}

// 请求参数信息
type RequestParamInfo struct {
	ParamType   string     `json:"param_type"`   // "query", "body", "path"
	ParamName   string     `json:"param_name"`   // 参数名称
	ParamSchema *APISchema `json:"param_schema"` // 参数结构
	IsRequired  bool       `json:"is_required"`  // 是否必需
	Source      string     `json:"source"`       // 来源方法: "c.Query", "c.ShouldBindJSON", etc.
}

// Handler分析结果 (包含请求和响应)
type HandlerAnalysisResult struct {
	HandlerName   string             `json:"handler"`
	RequestParams []RequestParamInfo `json:"request_params,omitempty"`
	Response      *APISchema         `json:"response,omitempty"`
}

// 响应封装函数信息
type ResponseWrapperFunc struct {
	FuncObj         *types.Func    // 函数对象
	GinContextIdx   int            // gin.Context 参数索引
	DataParamIdx    int            // 业务数据参数索引
	JSONCallSite    *ast.CallExpr  // 内部 c.JSON 调用位置
	ReturnType      *types.Named   // 返回的结构体类型
	ParamToFieldMap map[string]int // 参数→字段映射
}

// 全局预处理映射 (重新设计的数据结构)
type GlobalMappings struct {
	ResponseWrappers map[*types.Func]*ResponseWrapperFunc `json:"-"` // 响应封装函数映射
	StructTagMap     map[*types.Named]map[string]string   `json:"-"` // 结构体字段的 JSON Tag
}

// 响应解析引擎 (技术规范实现)
type ResponseParsingEngine struct {
	allPackages    []*packages.Package
	globalMappings *GlobalMappings
	maxDepth       int // 递归深度限制
}

// 请求参数解析器
type RequestParamAnalyzer struct {
	engine     *ResponseParsingEngine
	typeInfo   *types.Info
	currentPkg *packages.Package
}

// API 响应结构定义 (保持向后兼容)
type APIResponse struct {
	ResponseType string                 // 响应类型
	DataRealType string                 // 实际数据类型
	Fields       map[string]FieldSchema // 字段结构
}

// 字段结构定义 (保持向后兼容)
type FieldSchema struct {
	Type      string                 // 字段类型
	JSONTag   string                 // json tag
	IsPointer bool                   // 是否是指针
	IsArray   bool                   // 是否是切片
	Children  map[string]FieldSchema // 嵌套结构
}

// 创建新的响应解析引擎
func NewResponseParsingEngine(packages []*packages.Package) *ResponseParsingEngine {
	engine := &ResponseParsingEngine{
		allPackages: packages,
		maxDepth:    10, // 增加递归深度限制，支持更深层嵌套
		globalMappings: &GlobalMappings{
			ResponseWrappers: make(map[*types.Func]*ResponseWrapperFunc),
			StructTagMap:     make(map[*types.Named]map[string]string),
		},
	}

	// 执行全局预处理
	engine.performGlobalPreprocessing()
	return engine
}

// 全局预处理阶段 (技术规范步骤1)
func (engine *ResponseParsingEngine) performGlobalPreprocessing() {
	fmt.Printf("[DEBUG] 开始全局预处理阶段...\n")

	// 遍历所有包进行预处理
	for _, pkg := range engine.allPackages {
		engine.preprocessPackage(pkg)
	}

	fmt.Printf("[DEBUG] 全局预处理完成: 发现 %d 个响应封装函数, %d 个结构体\n",
		len(engine.globalMappings.ResponseWrappers),
		len(engine.globalMappings.StructTagMap))
}

// 预处理单个包
func (engine *ResponseParsingEngine) preprocessPackage(pkg *packages.Package) {
	if pkg.Types == nil {
		return
	}

	// 1. 构建结构体字段的JSON Tag映射
	engine.buildStructTagMap(pkg)

	// 2. 识别响应封装函数 (关键步骤)
	engine.identifyResponseWrapperFunctions(pkg)
}

// 识别响应封装函数 (核心逻辑重新实现)
func (engine *ResponseParsingEngine) identifyResponseWrapperFunctions(pkg *packages.Package) {
	// 遍历所有函数声明
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				// 跳过没有函数体的函数（注释、接口声明等）
				if funcDecl.Body == nil {
					fmt.Printf("[DEBUG] 跳过无函数体的函数: %s\n", funcDecl.Name.Name)
					continue
				}

				// 检查是否为响应封装函数
				if wrapper := engine.analyzeResponseWrapperCandidate(funcDecl, pkg); wrapper != nil {
					funcObj := pkg.TypesInfo.ObjectOf(funcDecl.Name).(*types.Func)
					engine.globalMappings.ResponseWrappers[funcObj] = wrapper
					fmt.Printf("[DEBUG] 发现响应封装函数: %s (gin.Context参数索引: %d, 数据参数索引: %d)\n",
						funcDecl.Name.Name, wrapper.GinContextIdx, wrapper.DataParamIdx)
				}
			}
		}
	}
}

// 分析函数是否为响应封装函数候选者
func (engine *ResponseParsingEngine) analyzeResponseWrapperCandidate(funcDecl *ast.FuncDecl, pkg *packages.Package) *ResponseWrapperFunc {
	if funcDecl.Type.Params == nil || len(funcDecl.Type.Params.List) < 1 {
		return nil // 响应封装函数至少需要1个参数: gin.Context
	}

	// 1. 查找gin.Context参数
	ginContextIdx := engine.findGinContextParameter(funcDecl, pkg)
	if ginContextIdx == -1 {
		return nil // 必须有gin.Context参数
	}

	// 2. 确保不是Handler (Handler只有一个gin.Context参数)
	if engine.isGinHandlerFunction(funcDecl, pkg.TypesInfo) {
		return nil // 排除Handler函数
	}

	// 3. 查找函数体内的c.JSON调用
	jsonCallSite := engine.findJSONCallInFunction(funcDecl, pkg)
	if jsonCallSite == nil {
		return nil // 必须内部调用c.JSON
	}

	// 4. 获取返回类型 (可能返回结构体或void)
	returnType := engine.getReturnStructType(funcDecl, pkg)

	// 5. 查找数据参数索引 (interface{} 或具体类型的参数)
	dataParamIdx := engine.findDataParameter(funcDecl, ginContextIdx)

	// 6. 分析参数→字段映射
	paramToFieldMap := engine.analyzeParameterFieldMapping(funcDecl, pkg)

	return &ResponseWrapperFunc{
		FuncObj:         pkg.TypesInfo.ObjectOf(funcDecl.Name).(*types.Func),
		GinContextIdx:   ginContextIdx,
		DataParamIdx:    dataParamIdx,
		JSONCallSite:    jsonCallSite,
		ReturnType:      returnType,
		ParamToFieldMap: paramToFieldMap,
	}
}

// 查找gin.Context参数索引
func (engine *ResponseParsingEngine) findGinContextParameter(funcDecl *ast.FuncDecl, pkg *packages.Package) int {
	paramIdx := 0
	for _, paramList := range funcDecl.Type.Params.List {
		for range paramList.Names {
			// 检查参数类型是否为*gin.Context
			if engine.isGinContextType(paramList.Type, pkg) {
				return paramIdx
			}
			paramIdx++
		}
	}
	return -1
}

// 检查类型是否为*gin.Context
func (engine *ResponseParsingEngine) isGinContextType(expr ast.Expr, _ *packages.Package) bool {
	if starExpr, ok := expr.(*ast.StarExpr); ok {
		if selExpr, ok := starExpr.X.(*ast.SelectorExpr); ok {
			if ident, ok := selExpr.X.(*ast.Ident); ok {
				return ident.Name == "gin" && selExpr.Sel.Name == "Context"
			}
		}
	}
	return false
}

// 检查是否为Gin Handler (只有一个gin.Context参数)
func (engine *ResponseParsingEngine) isGinHandlerFunction(funcDecl *ast.FuncDecl, typeInfo *types.Info) bool {
	if funcDecl.Type.Params == nil || len(funcDecl.Type.Params.List) != 1 {
		return false
	}

	param := funcDecl.Type.Params.List[0]
	if len(param.Names) != 1 {
		return false
	}

	if paramType := typeInfo.TypeOf(param.Type); paramType != nil {
		typeStr := paramType.String()
		return typeStr == "*github.com/gin-gonic/gin.Context" || typeStr == "*gin.Context"
	}
	return false
}

// 查找函数内的c.JSON调用
func (engine *ResponseParsingEngine) findJSONCallInFunction(funcDecl *ast.FuncDecl, pkg *packages.Package) *ast.CallExpr {
	if funcDecl.Body == nil {
		return nil
	}

	var jsonCall *ast.CallExpr
	ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			if engine.isGinJSONCall(callExpr, pkg) {
				jsonCall = callExpr
				return false // 找到第一个就停止
			}
		}
		return true
	})

	return jsonCall
}

// 获取函数返回的结构体类型 (可能为nil，因为有些封装函数是void)
func (engine *ResponseParsingEngine) getReturnStructType(funcDecl *ast.FuncDecl, pkg *packages.Package) *types.Named {
	if funcDecl.Type.Results == nil || len(funcDecl.Type.Results.List) == 0 {
		return nil // void函数
	}

	// 获取第一个返回值的类型
	returnExpr := funcDecl.Type.Results.List[0].Type
	returnType := pkg.TypesInfo.TypeOf(returnExpr)

	return engine.resolveNamedStruct(returnType)
}

// 查找数据参数索引 (非gin.Context的参数)
func (engine *ResponseParsingEngine) findDataParameter(funcDecl *ast.FuncDecl, ginContextIdx int) int {
	paramIdx := 0
	for _, paramList := range funcDecl.Type.Params.List {
		for range paramList.Names {
			if paramIdx != ginContextIdx {
				return paramIdx // 返回第一个非gin.Context参数
			}
			paramIdx++
		}
	}
	return -1
}

// 分析参数→字段映射
func (engine *ResponseParsingEngine) analyzeParameterFieldMapping(funcDecl *ast.FuncDecl, pkg *packages.Package) map[string]int {
	fieldMapping := make(map[string]int)

	if funcDecl.Body == nil {
		return fieldMapping
	}

	ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
		if retStmt, ok := node.(*ast.ReturnStmt); ok && len(retStmt.Results) > 0 {
			// 检查返回值是否为结构体字面量
			if compLit, ok := retStmt.Results[0].(*ast.CompositeLit); ok {
				engine.analyzeStructLiteralMapping(compLit, funcDecl, fieldMapping, pkg)
			}
			// 检查返回值是否为结构体指针字面量
			if unaryExpr, ok := retStmt.Results[0].(*ast.UnaryExpr); ok && unaryExpr.Op == token.AND {
				if compLit, ok := unaryExpr.X.(*ast.CompositeLit); ok {
					engine.analyzeStructLiteralMapping(compLit, funcDecl, fieldMapping, pkg)
				}
			}
		}
		return true
	})

	return fieldMapping
}

// 解析命名结构体类型
func (engine *ResponseParsingEngine) resolveNamedStruct(typ types.Type) *types.Named {
	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	// 检查是否为命名类型
	if named, ok := typ.(*types.Named); ok {
		// 检查底层类型是否为结构体
		if _, ok := named.Underlying().(*types.Struct); ok {
			return named
		}
	}

	return nil
}

// 构建结构体字段的JSON Tag映射
func (engine *ResponseParsingEngine) buildStructTagMap(pkg *packages.Package) {
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			if genDecl, ok := node.(*ast.GenDecl); ok && genDecl.Tok == token.TYPE {
				for _, spec := range genDecl.Specs {
					if typeSpec, ok := spec.(*ast.TypeSpec); ok {
						if structType, ok := typeSpec.Type.(*ast.StructType); ok {
							// 获取类型对象
							if obj := pkg.TypesInfo.ObjectOf(typeSpec.Name); obj != nil {
								if named := obj.Type().(*types.Named); named != nil {
									engine.extractStructTags(named, structType)
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

// 提取结构体字段的JSON Tag
func (engine *ResponseParsingEngine) extractStructTags(named *types.Named, structType *ast.StructType) {
	tagMap := make(map[string]string)

	for _, field := range structType.Fields.List {
		if len(field.Names) > 0 && field.Tag != nil {
			fieldName := field.Names[0].Name
			tag := strings.Trim(field.Tag.Value, "`")

			// 解析JSON标签
			if jsonTag := reflect.StructTag(tag).Get("json"); jsonTag != "" {
				if idx := strings.Index(jsonTag, ","); idx != -1 {
					jsonTag = jsonTag[:idx]
				}
				if jsonTag != "-" && jsonTag != "" {
					tagMap[fieldName] = jsonTag
				}
			}
		}
	}

	if len(tagMap) > 0 {
		engine.globalMappings.StructTagMap[named] = tagMap
	}
}

// 分析结构体字面量中的参数映射
func (engine *ResponseParsingEngine) analyzeStructLiteralMapping(
	compLit *ast.CompositeLit,
	funcDecl *ast.FuncDecl,
	fieldMapping map[string]int,
	pkg *packages.Package) {

	for _, elt := range compLit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			// 获取字段名
			var fieldName string
			if ident, ok := kv.Key.(*ast.Ident); ok {
				fieldName = ident.Name
			}

			// 检查值是否为参数
			if ident, ok := kv.Value.(*ast.Ident); ok {
				if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
					// 检查是否为函数参数
					if paramIdx := engine.getParameterIndex(obj, funcDecl); paramIdx != -1 {
						fieldMapping[fieldName] = paramIdx
						fmt.Printf("[DEBUG] 发现参数映射: %s.%s <- 参数[%d]\n",
							funcDecl.Name.Name, fieldName, paramIdx)
					}
				}
			}
		}
	}
}

// 获取参数在函数参数列表中的索引
func (engine *ResponseParsingEngine) getParameterIndex(obj types.Object, funcDecl *ast.FuncDecl) int {
	if funcDecl.Type.Params == nil {
		return -1
	}

	paramIndex := 0
	for _, paramList := range funcDecl.Type.Params.List {
		for _, paramName := range paramList.Names {
			if paramName.Name == obj.Name() {
				return paramIndex
			}
			paramIndex++
		}
	}
	return -1
}

// Handler解析阶段 (技术规范步骤2) - 核心响应表达式解析
func (engine *ResponseParsingEngine) AnalyzeHandlerResponse(handlerDecl *ast.FuncDecl, pkg *packages.Package) *APISchema {
	// 步骤1: 定位业务响应表达式（c.JSON调用或响应封装函数调用）
	responseExpr := engine.findLastResponseExpression(handlerDecl, pkg)
	if responseExpr == nil {
		fmt.Printf("[DEBUG] 未找到响应表达式\n")
		return nil
	}

	// 步骤2: 响应表达式类型解析（核心）
	return engine.resolveResponseExpression(responseExpr, pkg)
}

// 检查是否为gin.Context的JSON调用
func (engine *ResponseParsingEngine) isGinJSONCall(callExpr *ast.CallExpr, pkg *packages.Package) bool {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		// 检查方法名是否为JSON
		if selExpr.Sel.Name != "JSON" {
			return false
		}

		// 检查调用对象是否为*gin.Context类型
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
				objType := obj.Type()
				// 处理指针类型
				if ptr, ok := objType.(*types.Pointer); ok {
					objType = ptr.Elem()
				}
				// 检查是否为gin.Context
				if named, ok := objType.(*types.Named); ok {
					return named.Obj().Name() == "Context"
				}
			}
		}
	}
	return false
}

// 响应表达式类型解析 (技术规范核心算法)
func (engine *ResponseParsingEngine) resolveResponseExpression(expr ast.Expr, pkg *packages.Package) *APISchema {
	fmt.Printf("[DEBUG] 统一递归解析响应表达式: %T\n", expr)

	switch e := expr.(type) {
	case *ast.CallExpr:
		// 1. 函数调用表达式 - 递归解析函数调用
		return engine.resolveFunctionCallRecursive(e, pkg)
	case *ast.CompositeLit:
		// 2. 字面量表达式 - 递归解析复合字面量 (结构体、map等)
		return engine.resolveCompositeLiteralRecursive(e, pkg)
	case *ast.Ident:
		// 3. 标识符表达式 - 递归解析变量
		return engine.resolveIdentifierRecursive(e, pkg)
	case *ast.SelectorExpr:
		// 4. 选择器表达式 - 递归解析包选择器
		return engine.resolveSelectorExprRecursive(e, pkg)
	default:
		fmt.Printf("[DEBUG] 未支持的响应表达式类型: %T\n", expr)
		return &APISchema{Type: "unknown", Description: "unsupported expression type"}
	}
}

// 直接分析封装函数的参数 (简化版本)
func (engine *ResponseParsingEngine) analyzeWrapperFunctionArgs(wrapper *ResponseWrapperFunc, callArgs []ast.Expr, pkg *packages.Package) *APISchema {
	fmt.Printf("[DEBUG] 直接分析封装函数参数，参数数量: %d，数据参数索引: %d\n", len(callArgs), wrapper.DataParamIdx)

	// 创建基础响应结构 (基于Response类型)
	responseSchema := &APISchema{
		Type: "object",
		Properties: map[string]*APISchema{
			"request_id": {Type: "string", JSONTag: "request_id"},
			"code":       {Type: "integer", JSONTag: "code"},
			"message":    {Type: "string", JSONTag: "message"},
			"data":       {Type: "any", JSONTag: "data", Description: "interface{}"},
		},
	}

	// 如果有数据参数，解析其具体类型
	if wrapper.DataParamIdx >= 0 && wrapper.DataParamIdx < len(callArgs) {
		dataArg := callArgs[wrapper.DataParamIdx]
		fmt.Printf("[DEBUG] 分析数据参数[%d]: %T\n", wrapper.DataParamIdx, dataArg)

		dataType := pkg.TypesInfo.TypeOf(dataArg)
		if dataType != nil {
			fmt.Printf("[DEBUG] 数据参数类型: %s\n", dataType.String())
			injectedSchema := engine.resolveType(dataType, engine.maxDepth)
			fmt.Printf("[DEBUG] ✅ 参数类型注入成功: Data字段 interface{} -> %s\n", injectedSchema.Type)

			// 替换 Data 字段的类型信息
			responseSchema.Properties["data"] = injectedSchema
		} else {
			fmt.Printf("[DEBUG] ❌ 无法获取数据参数类型\n")
		}
	} else {
		fmt.Printf("[DEBUG] ❌ 数据参数索引无效: %d >= %d\n", wrapper.DataParamIdx, len(callArgs))
	}

	return responseSchema
}

// 获取参数的实际类型 (用于类型注入)
func (engine *ResponseParsingEngine) getParameterType(paramName string, funcDecl *ast.FuncDecl, callArgs []ast.Expr, pkg *packages.Package) types.Type {
	// 查找参数在函数签名中的索引
	paramIdx := -1
	currentIdx := 0

	if funcDecl.Type.Params != nil {
		for _, paramList := range funcDecl.Type.Params.List {
			for _, paramIdent := range paramList.Names {
				if paramIdent.Name == paramName {
					paramIdx = currentIdx
					break
				}
				currentIdx++
			}
			if paramIdx != -1 {
				break
			}
		}
	}

	// 如果找到参数且有对应的调用参数，返回调用参数的类型
	if paramIdx >= 0 && paramIdx < len(callArgs) {
		return pkg.TypesInfo.TypeOf(callArgs[paramIdx])
	}

	return nil
}

// 查找函数声明
func (engine *ResponseParsingEngine) findFunctionDeclaration(funcObj *types.Func, targetPkg *packages.Package) *ast.FuncDecl {
	// 首先在目标包中查找
	for _, file := range targetPkg.Syntax {
		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				if obj := targetPkg.TypesInfo.ObjectOf(funcDecl.Name); obj == funcObj {
					// 验证函数确实有函数体（不是注释或声明）
					if funcDecl.Body != nil {
						fmt.Printf("[DEBUG] 找到函数定义: %s (有函数体)\n", funcDecl.Name.Name)
						return funcDecl
					} else {
						fmt.Printf("[DEBUG] 跳过函数声明: %s (无函数体)\n", funcDecl.Name.Name)
					}
				}
			}
		}
	}

	// 如果在目标包中找不到，搜索所有包
	for _, pkg := range engine.allPackages {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if funcDecl, ok := decl.(*ast.FuncDecl); ok {
					if obj := pkg.TypesInfo.ObjectOf(funcDecl.Name); obj == funcObj {
						// 验证函数确实有函数体（不是注释或声明）
						if funcDecl.Body != nil {
							fmt.Printf("[DEBUG] 找到函数定义: %s (有函数体)\n", funcDecl.Name.Name)
							return funcDecl
						} else {
							fmt.Printf("[DEBUG] 跳过函数声明: %s (无函数体)\n", funcDecl.Name.Name)
						}
					}
				}
			}
		}
	}

	return nil
}

// 递归解析函数调用 (统一的函数调用处理)
func (engine *ResponseParsingEngine) resolveFunctionCallRecursive(callExpr *ast.CallExpr, pkg *packages.Package) *APISchema {
	fmt.Printf("[DEBUG] 递归解析函数调用\n")

	// 1. 获取函数对象
	funcObj := engine.getFunctionObject(callExpr, pkg)
	if funcObj == nil {
		fmt.Printf("[DEBUG] 无法获取函数对象，使用fallback解析\n")
		return engine.resolveFallbackType(callExpr, pkg)
	}

	fmt.Printf("[DEBUG] 函数名: %s\n", funcObj.Name())

	// 2. 检查是否为响应封装函数
	if wrapper, ok := engine.globalMappings.ResponseWrappers[funcObj]; ok {
		fmt.Printf("[DEBUG] 发现响应封装函数，直接解析参数\n")
		return engine.analyzeWrapperFunctionArgs(wrapper, callExpr.Args, pkg)
	}

	// 3. 普通函数：分析函数返回的内容
	fmt.Printf("[DEBUG] 普通函数，分析返回类型和参数\n")

	// 3.1 获取函数声明
	funcDecl := engine.findFunctionDeclaration(funcObj, pkg)
	if funcDecl == nil {
		fmt.Printf("[DEBUG] 无法找到函数声明，使用类型信息\n")
		return engine.resolveFunctionByTypeInfo(callExpr, pkg)
	}

	// 3.2 分析函数返回语句
	return engine.analyzeFunctionReturnRecursive(funcDecl, callExpr.Args, pkg)
}

// 通过类型信息解析函数 (备用方案)
func (engine *ResponseParsingEngine) resolveFunctionByTypeInfo(callExpr *ast.CallExpr, pkg *packages.Package) *APISchema {
	returnType := pkg.TypesInfo.TypeOf(callExpr)
	if returnType != nil {
		schema := engine.resolveType(returnType, engine.maxDepth)

		// 如果是Response类型，尝试参数注入
		if schema != nil && schema.Properties != nil {
			if dataField, exists := schema.Properties["Data"]; exists && dataField.Type == "any" {
				fmt.Printf("[DEBUG] 尝试Response类型参数注入\n")
				if len(callExpr.Args) >= 2 {
					dataArg := callExpr.Args[1]
					dataType := pkg.TypesInfo.TypeOf(dataArg)
					if dataType != nil {
						injectedSchema := engine.resolveType(dataType, engine.maxDepth)
						schema.Properties["Data"] = injectedSchema
						fmt.Printf("[DEBUG] ✅ 参数注入成功: %s\n", injectedSchema.Type)
					}
				}
			}
		}

		return schema
	}
	return &APISchema{Type: "unknown", Description: "unable to resolve function"}
}

// 递归分析函数返回语句
func (engine *ResponseParsingEngine) analyzeFunctionReturnRecursive(funcDecl *ast.FuncDecl, callArgs []ast.Expr, pkg *packages.Package) *APISchema {
	fmt.Printf("[DEBUG] 递归分析函数 %s 的返回语句\n", funcDecl.Name.Name)

	if funcDecl.Body == nil {
		return &APISchema{Type: "unknown", Description: "no function body"}
	}

	// 查找return语句
	var returnExpr ast.Expr
	ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
		if retStmt, ok := node.(*ast.ReturnStmt); ok && len(retStmt.Results) > 0 {
			returnExpr = retStmt.Results[0]
			return false // 找到第一个return就停止
		}
		return true
	})

	if returnExpr == nil {
		return &APISchema{Type: "unknown", Description: "no return statement"}
	}

	fmt.Printf("[DEBUG] 找到返回表达式: %T\n", returnExpr)

	// 递归解析返回表达式，并注入调用参数的类型信息
	return engine.resolveReturnExpressionWithArgs(returnExpr, funcDecl, callArgs, pkg)
}

// 解析返回表达式并注入参数类型
func (engine *ResponseParsingEngine) resolveReturnExpressionWithArgs(returnExpr ast.Expr, funcDecl *ast.FuncDecl, callArgs []ast.Expr, pkg *packages.Package) *APISchema {
	switch retExpr := returnExpr.(type) {
	case *ast.CompositeLit:
		// 复合字面量 (如 gin.H{...} 或 Response{...})
		return engine.resolveCompositeLiteralWithArgs(retExpr, funcDecl, callArgs, pkg)
	case *ast.CallExpr:
		// 函数调用 (如 ResponseOK(ctx, data))
		return engine.resolveFunctionCallRecursive(retExpr, pkg)
	case *ast.Ident:
		// 变量引用
		return engine.resolveIdentifierRecursive(retExpr, pkg)
	case *ast.UnaryExpr:
		// 一元表达式 (如 &Response{...})
		return engine.resolveUnaryExpressionWithArgs(retExpr, funcDecl, callArgs, pkg)
	default:
		// 其他类型，使用基础解析
		returnType := pkg.TypesInfo.TypeOf(returnExpr)
		if returnType == nil {
			fmt.Printf("[DEBUG] ❌ 无法获取返回表达式类型: %T\n", returnExpr)
			return &APISchema{Type: "unknown", Description: "unable to get return expression type"}
		}
		return engine.resolveType(returnType, engine.maxDepth)
	}
}

// 解析复合字面量并注入参数类型
func (engine *ResponseParsingEngine) resolveCompositeLiteralWithArgs(compLit *ast.CompositeLit, funcDecl *ast.FuncDecl, callArgs []ast.Expr, pkg *packages.Package) *APISchema {
	fmt.Printf("[DEBUG] 解析复合字面量并注入参数类型\n")

	// 创建字段映射
	properties := make(map[string]*APISchema)

	for i, elt := range compLit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			// 获取key名称
			var keyName string
			if basicLit, ok := kv.Key.(*ast.BasicLit); ok && basicLit.Kind == token.STRING {
				// 字符串字面量（如 gin.H{"key": value}）
				keyName = strings.Trim(basicLit.Value, "`\"")
			} else if ident, ok := kv.Key.(*ast.Ident); ok {
				// 标识符（如 struct{Field: value}）
				keyName = ident.Name
			} else {
				keyName = fmt.Sprintf("field_%d", i)
			}

			// 解析value，如果是参数则注入实际类型
			valueSchema := engine.resolveValueWithParameterInjection(kv.Value, funcDecl, callArgs, pkg)
			properties[keyName] = valueSchema

			fmt.Printf("[DEBUG] 字段 %s: %s\n", keyName, valueSchema.Type)
		}
	}

	return &APISchema{
		Type:       "object",
		Properties: properties,
	}
}

// 解析值并注入参数类型
func (engine *ResponseParsingEngine) resolveValueWithParameterInjection(valueExpr ast.Expr, funcDecl *ast.FuncDecl, callArgs []ast.Expr, pkg *packages.Package) *APISchema {
	switch val := valueExpr.(type) {
	case *ast.Ident:
		// 如果是参数标识符，注入实际参数类型
		if paramType := engine.getParameterType(val.Name, funcDecl, callArgs, pkg); paramType != nil {
			fmt.Printf("[DEBUG] ✅ 参数类型注入: %s -> %s\n", val.Name, paramType.String())
			return engine.resolveType(paramType, engine.maxDepth)
		}
		// 否则使用默认解析
		return engine.resolveIdentifierRecursive(val, pkg)
	default:
		// 其他类型使用基础解析
		valueType := pkg.TypesInfo.TypeOf(valueExpr)
		if valueType != nil {
			return engine.resolveType(valueType, engine.maxDepth)
		}
		return &APISchema{Type: "any", Description: "interface{}"}
	}
}

// 解析一元表达式 (如 &Response{...})
func (engine *ResponseParsingEngine) resolveUnaryExpressionWithArgs(unaryExpr *ast.UnaryExpr, funcDecl *ast.FuncDecl, callArgs []ast.Expr, pkg *packages.Package) *APISchema {
	fmt.Printf("[DEBUG] 解析一元表达式: %s\n", unaryExpr.Op.String())

	// 处理取地址操作符 (&)
	if unaryExpr.Op == token.AND {
		// 递归解析内部表达式
		if compLit, ok := unaryExpr.X.(*ast.CompositeLit); ok {
			// &Response{...} 这种情况
			return engine.resolveCompositeLiteralWithArgs(compLit, funcDecl, callArgs, pkg)
		} else {
			// 其他情况，使用基础解析
			innerType := pkg.TypesInfo.TypeOf(unaryExpr.X)
			if innerType != nil {
				return engine.resolveType(innerType, engine.maxDepth)
			}
		}
	}

	// 其他一元操作符
	exprType := pkg.TypesInfo.TypeOf(unaryExpr)
	if exprType != nil {
		return engine.resolveType(exprType, engine.maxDepth)
	}

	return &APISchema{Type: "unknown", Description: "unable to resolve unary expression"}
}

// 递归解析复合字面量 (暂时使用原有逻辑)
func (engine *ResponseParsingEngine) resolveCompositeLiteralRecursive(compLit *ast.CompositeLit, pkg *packages.Package) *APISchema {
	// 目前使用原有的解析逻辑
	return engine.resolveCompositeLiteral(compLit, pkg)
}

// 递归解析标识符 (暂时使用原有逻辑)
func (engine *ResponseParsingEngine) resolveIdentifierRecursive(ident *ast.Ident, pkg *packages.Package) *APISchema {
	// 目前使用原有的解析逻辑
	return engine.resolveIdentifier(ident, pkg)
}

// 递归解析选择器表达式 (暂时使用原有逻辑)
func (engine *ResponseParsingEngine) resolveSelectorExprRecursive(selExpr *ast.SelectorExpr, pkg *packages.Package) *APISchema {
	// 目前使用原有的解析逻辑
	return engine.resolveSelectorExpr(selExpr, pkg)
}

// 获取函数对象
func (engine *ResponseParsingEngine) getFunctionObject(callExpr *ast.CallExpr, pkg *packages.Package) *types.Func {
	switch fun := callExpr.Fun.(type) {
	case *ast.Ident:
		// 直接函数调用
		fmt.Printf("[DEBUG] 尝试解析标识符: %s\n", fun.Name)
		if obj := pkg.TypesInfo.ObjectOf(fun); obj != nil {
			fmt.Printf("[DEBUG] 找到对象: %T, %s\n", obj, obj.String())
			if funcObj, ok := obj.(*types.Func); ok {
				fmt.Printf("[DEBUG] 成功解析函数: %s\n", funcObj.Name())
				return funcObj
			}
		} else {
			fmt.Printf("[DEBUG] 无法找到标识符对象: %s\n", fun.Name)
		}
	case *ast.SelectorExpr:
		// 包选择器调用
		if obj := pkg.TypesInfo.ObjectOf(fun.Sel); obj != nil {
			if funcObj, ok := obj.(*types.Func); ok {
				return funcObj
			}
		}
	}
	return nil
}

// 解析直接结构体字面量
func (engine *ResponseParsingEngine) resolveCompositeLiteral(compLit *ast.CompositeLit, pkg *packages.Package) *APISchema {
	structType := pkg.TypesInfo.TypeOf(compLit)
	if structType != nil {
		return engine.resolveType(structType, engine.maxDepth)
	}
	return &APISchema{Type: "object", Description: "composite literal"}
}

// 解析标识符（变量）
func (engine *ResponseParsingEngine) resolveIdentifier(ident *ast.Ident, pkg *packages.Package) *APISchema {
	if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
		return engine.resolveType(obj.Type(), engine.maxDepth)
	}
	return &APISchema{Type: "unknown", Description: "unresolved identifier"}
}

// 解析选择器表达式
func (engine *ResponseParsingEngine) resolveSelectorExpr(selExpr *ast.SelectorExpr, pkg *packages.Package) *APISchema {
	exprType := pkg.TypesInfo.TypeOf(selExpr)
	if exprType != nil {
		return engine.resolveType(exprType, engine.maxDepth)
	}
	return &APISchema{Type: "unknown", Description: "unresolved selector"}
}

// 回退类型解析
func (engine *ResponseParsingEngine) resolveFallbackType(expr ast.Expr, pkg *packages.Package) *APISchema {
	if exprType := pkg.TypesInfo.TypeOf(expr); exprType != nil {
		return engine.resolveType(exprType, engine.maxDepth)
	}
	return &APISchema{Type: "unknown", Description: "fallback resolution failed"}
}

// 递归结构体解析 (技术规范步骤3) - 类型系统优先
func (engine *ResponseParsingEngine) resolveType(typ types.Type, depth int) *APISchema {
	if depth <= 0 {
		return &APISchema{Type: "object", Description: "max depth reached"}
	}

	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		return engine.resolveType(ptr.Elem(), depth)
	}

	// 处理基础类型
	if basic, ok := typ.(*types.Basic); ok {
		return &APISchema{Type: engine.mapBasicType(basic.Kind())}
	}

	// 处理切片类型
	if slice, ok := typ.(*types.Slice); ok {
		return &APISchema{
			Type:  "array",
			Items: engine.resolveType(slice.Elem(), depth-1),
		}
	}

	// 处理数组类型
	if array, ok := typ.(*types.Array); ok {
		return &APISchema{
			Type:  "array",
			Items: engine.resolveType(array.Elem(), depth-1),
		}
	}

	// 处理Map类型
	if mapType, ok := typ.(*types.Map); ok {
		keyType := engine.resolveType(mapType.Key(), depth-1)
		valueType := engine.resolveType(mapType.Elem(), depth-1)
		return &APISchema{
			Type: fmt.Sprintf("map[%s]%s", keyType.Type, valueType.Type),
			Properties: map[string]*APISchema{
				"<key>":   keyType,
				"<value>": valueType,
			},
		}
	}

	// 处理接口类型
	if iface, ok := typ.(*types.Interface); ok {
		if iface.Empty() {
			return &APISchema{Type: "any", Description: "interface{}"}
		}
		return &APISchema{Type: "interface", Description: "non-empty interface"}
	}

	// 处理命名类型（结构体、自定义类型等）
	if named, ok := typ.(*types.Named); ok {
		return engine.resolveNamedType(named, depth)
	}

	// 处理结构体类型
	if structType, ok := typ.(*types.Struct); ok {
		return engine.resolveStructType(structType, depth, nil)
	}

	return &APISchema{Type: typ.String(), Description: "unhandled type"}
}

// 解析命名类型 (支持自定义结构体)
func (engine *ResponseParsingEngine) resolveNamedType(named *types.Named, depth int) *APISchema {
	obj := named.Obj()
	if obj == nil {
		return &APISchema{Type: named.String()}
	}

	// 检查底层类型
	underlying := named.Underlying()
	if structType, ok := underlying.(*types.Struct); ok {
		// 是结构体类型，递归解析字段
		schema := engine.resolveStructType(structType, depth-1, named)
		schema.Type = obj.Name() // 使用命名类型的名称
		return schema
	}

	// 其他命名类型（如type alias）
	underlyingSchema := engine.resolveType(underlying, depth-1)
	return &APISchema{
		Type:        obj.Name(),
		Description: fmt.Sprintf("alias for %s", underlyingSchema.Type),
		Properties:  underlyingSchema.Properties,
		Items:       underlyingSchema.Items,
	}
}

// 解析结构体类型 (核心字段解析逻辑)
func (engine *ResponseParsingEngine) resolveStructType(structType *types.Struct, depth int, named *types.Named) *APISchema {
	properties := make(map[string]*APISchema)

	for i := 0; i < structType.NumFields(); i++ {
		field := structType.Field(i)
		tag := structType.Tag(i)

		// 解析字段类型 (字段与结构体同级，只有在嵌套结构体时才减少深度)
		fieldSchema := engine.resolveType(field.Type(), depth)

		// 提取JSON标签
		jsonTag := engine.extractJSONTag(tag)

		// 跳过被忽略的字段
		if jsonTag == "-" {
			continue
		}

		// 如果没有JSON标签，使用字段名
		if jsonTag == "" {
			jsonTag = field.Name()
		}

		fieldSchema.JSONTag = jsonTag

		// 如果有命名类型且存在预构建的标签映射，使用预构建的标签
		if named != nil {
			if tagMap, ok := engine.globalMappings.StructTagMap[named]; ok {
				if prebuiltTag, exists := tagMap[field.Name()]; exists {
					fieldSchema.JSONTag = prebuiltTag
				}
			}
		}

		properties[field.Name()] = fieldSchema
	}

	return &APISchema{
		Type:       "object",
		Properties: properties,
	}
}

// 提取JSON标签
func (engine *ResponseParsingEngine) extractJSONTag(tag string) string {
	if tag == "" {
		return ""
	}

	// 解析结构体标签
	structTag := reflect.StructTag(tag)
	jsonTag := structTag.Get("json")

	if jsonTag == "" {
		return ""
	}

	// 处理JSON标签选项（如omitempty）
	if idx := strings.Index(jsonTag, ","); idx != -1 {
		jsonTag = jsonTag[:idx]
	}

	return jsonTag
}

// 映射Go基础类型到API Schema类型
func (engine *ResponseParsingEngine) mapBasicType(kind types.BasicKind) string {
	switch kind {
	case types.Bool:
		return "boolean"
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr:
		return "integer"
	case types.Float32, types.Float64:
		return "number"
	case types.Complex64, types.Complex128:
		return "complex"
	case types.String:
		return "string"
	case types.UnsafePointer:
		return "pointer"
	default:
		return "unknown"
	}
}

// 响应分析器
type ResponseAnalyzer struct {
	pkg  *packages.Package
	fset *token.FileSet
}

// 响应模式枚举
const (
	UnknownResponse = iota
	StructResponse
	StructLiteralResponse
	MapLiteralResponse
	FunctionCallResponse
	VariableResponse
	SelectorResponse
)

// 创建新的响应分析器
func NewResponseAnalyzer(pkg *packages.Package, fset *token.FileSet) *ResponseAnalyzer {
	return &ResponseAnalyzer{pkg: pkg, fset: fset}
}

// 分析响应表达式（入口函数）
func (ra *ResponseAnalyzer) AnalyzeResponse(expr ast.Expr) *APIResponse {
	mode := ra.detectResponseMode(expr)
	switch mode {
	case StructLiteralResponse:
		return ra.analyzeStructLiteral(expr)
	case StructResponse:
		return ra.analyzeStructResponse(expr)
	case MapLiteralResponse:
		return ra.analyzeMapLiteral(expr)
	case FunctionCallResponse:
		return ra.analyzeFunctionCall(expr)
	case VariableResponse:
		return ra.analyzeVariable(expr)
	case SelectorResponse:
		return ra.analyzeSelector(expr)
	default:
		return ra.analyzeFallback(expr)
	}
}

// 递归深度解析响应表达式（新增方法）
func (ra *ResponseAnalyzer) AnalyzeResponseRecursively(expr ast.Expr) *APIResponse {
	result := ra.AnalyzeResponse(expr)
	if result == nil {
		return nil
	}

	// 如果是包装函数，需要展开并合并结构
	if result.ResponseType == "function" || result.ResponseType == "wrapped-success" || result.ResponseType == "wrapped-error" {
		return ra.expandWrappedResponse(expr, result)
	}

	return result
}

// 展开包装响应，完全递归解析所有层级
func (ra *ResponseAnalyzer) expandWrappedResponse(expr ast.Expr, baseResponse *APIResponse) *APIResponse {
	callExpr, ok := expr.(*ast.CallExpr)
	if !ok {
		return baseResponse
	}

	// 获取函数名
	funcName := ""
	if sel, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		funcName = sel.Sel.Name
	}

	// 展开所有字段，创建完全扁平化的结构
	allFields := make(map[string]FieldSchema)

	// 首先添加包装函数的基础字段
	wrapperType := ra.pkg.TypesInfo.TypeOf(callExpr)
	if wrapperType != nil {
		baseFields := ra.parseTypeFields(wrapperType)
		for k, v := range baseFields {
			allFields[k] = v
		}
	}

	// 然后递归解析每个参数，展开所有嵌套结构
	for i, arg := range callExpr.Args {
		argResponse := ra.AnalyzeResponse(arg)
		if argResponse != nil && len(argResponse.Fields) > 0 {
			// 根据参数位置决定如何合并
			if funcName == "SuccessResponse" && i == 1 {
				// 第二个参数是data字段的内容
				if dataField, exists := allFields["Data"]; exists {
					dataField.Type = argResponse.DataRealType
					dataField.Children = ra.flattenFields(argResponse.Fields, "")
					allFields["Data"] = dataField
				}
			} else if funcName == "ErrorResponse" {
				// ErrorResponse通常不包含复杂的data结构
				continue
			} else {
				// 其他情况，直接合并字段
				for k, v := range argResponse.Fields {
					prefixedKey := fmt.Sprintf("arg%d_%s", i, k)
					allFields[prefixedKey] = v
				}
			}
		}
	}

	return &APIResponse{
		ResponseType: baseResponse.ResponseType,
		DataRealType: baseResponse.DataRealType,
		Fields:       allFields,
	}
}

// 扁平化字段结构，递归展开所有嵌套
func (ra *ResponseAnalyzer) flattenFields(fields map[string]FieldSchema, prefix string) map[string]FieldSchema {
	result := make(map[string]FieldSchema)

	for name, schema := range fields {
		fullName := name
		if prefix != "" {
			fullName = prefix + "." + name
		}

		// 复制当前字段
		flatSchema := FieldSchema{
			Type:      schema.Type,
			JSONTag:   schema.JSONTag,
			IsPointer: schema.IsPointer,
			IsArray:   schema.IsArray,
		}

		// 如果有子字段，递归展开
		if len(schema.Children) > 0 {
			flatSchema.Children = ra.flattenFields(schema.Children, fullName)
		}

		result[fullName] = flatSchema
	}

	return result
}

// 检测响应模式
func (ra *ResponseAnalyzer) detectResponseMode(expr ast.Expr) int {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		// 检查是否是结构体字面量
		if _, isStruct := e.Type.(*ast.StructType); isStruct {
			return StructLiteralResponse
		}
		// 检查是否是 map 字面量
		if _, isMap := e.Type.(*ast.MapType); isMap {
			return MapLiteralResponse
		}
		return UnknownResponse

	case *ast.CallExpr:
		return FunctionCallResponse

	case *ast.Ident:
		return VariableResponse

	case *ast.SelectorExpr:
		return SelectorResponse
	}
	return UnknownResponse
}

// 分析结构体响应（从类型声明）
func (ra *ResponseAnalyzer) analyzeStructResponse(expr ast.Expr) *APIResponse {
	typ := ra.pkg.TypesInfo.TypeOf(expr)
	if typ == nil {
		return nil
	}

	return &APIResponse{
		ResponseType: "struct",
		DataRealType: typ.String(),
		Fields:       ra.parseTypeFields(typ),
	}
}

// 分析结构体字面量响应（关键功能）
func (ra *ResponseAnalyzer) analyzeStructLiteral(expr ast.Expr) *APIResponse {
	compLit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}

	// 获取结构体类型
	structType := ra.pkg.TypesInfo.TypeOf(compLit)
	if structType == nil {
		return nil
	}

	// 先按声明类型解析结构体
	fields := ra.parseTypeFields(structType)

	// 遍历字面量中的每个字段赋值
	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		// 获取字段名
		fieldName := ra.extractFieldName(kv.Key)
		if fieldName == "" {
			continue
		}

		// 检查字段是否存在且是 interface{}
		if schema, exists := fields[fieldName]; exists {
			if strings.Contains(schema.Type, "interface{}") {
				// 获取实际值的类型
				valueType := ra.pkg.TypesInfo.TypeOf(kv.Value)
				if valueType != nil {
					// 更新字段类型信息
					schema.Type = valueType.String()
					schema.Children = ra.parseTypeFields(valueType)
					fields[fieldName] = schema
				}
			}
		}
	}

	return &APIResponse{
		ResponseType: "struct-literal",
		DataRealType: structType.String(),
		Fields:       fields,
	}
}

// 分析 map 字面量响应
func (ra *ResponseAnalyzer) analyzeMapLiteral(expr ast.Expr) *APIResponse {
	compLit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}

	fields := make(map[string]FieldSchema)

	// 获取map的实际类型
	mapType := ra.pkg.TypesInfo.TypeOf(compLit)
	dataRealType := "map[string]interface{}"
	if mapType != nil {
		dataRealType = mapType.String()
	}

	for _, elt := range compLit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		// 提取键名（必须是字符串字面量）
		keyStr := ra.extractStringKey(kv.Key)
		if keyStr == "" {
			// 如果不是字符串字面量，尝试其他方式提取
			if ident, ok := kv.Key.(*ast.Ident); ok {
				keyStr = ident.Name
			} else {
				continue
			}
		}

		// 分析值的类型
		valueType := ra.pkg.TypesInfo.TypeOf(kv.Value)
		typeStr := "unknown"
		if valueType != nil {
			typeStr = valueType.String()
		}

		// 创建字段 schema
		schema := FieldSchema{
			Type:    typeStr,
			JSONTag: keyStr,
		}

		// 递归解析嵌套结构（如果可能）
		if valueType != nil && ra.isStructOrMap(valueType) {
			schema.Children = ra.parseTypeFields(valueType)
		}

		fields[keyStr] = schema
	}

	return &APIResponse{
		ResponseType: "map-literal",
		DataRealType: dataRealType,
		Fields:       fields,
	}
}

// 分析函数调用响应
func (ra *ResponseAnalyzer) analyzeFunctionCall(expr ast.Expr) *APIResponse {
	callExpr, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil
	}

	// 获取函数名
	funcName := ""
	if sel, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		funcName = sel.Sel.Name
	}

	// 特殊处理：如果是包装函数，尝试提取实际数据
	if ra.isCommonWrapperFunction(funcName) {
		return ra.extractWrappedData(callExpr)
	}

	// 一般函数调用
	returnType := ra.pkg.TypesInfo.TypeOf(callExpr)
	if returnType == nil {
		return nil
	}

	return &APIResponse{
		ResponseType: "function",
		DataRealType: returnType.String(),
		Fields:       ra.parseTypeFields(returnType),
	}
}

// 提取包装函数中的实际数据
func (ra *ResponseAnalyzer) extractWrappedData(callExpr *ast.CallExpr) *APIResponse {
	// 获取函数名
	funcName := ""
	if sel, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		funcName = sel.Sel.Name
	}

	// 分析包装函数的返回类型以获取基础响应结构
	wrapperReturnType := ra.pkg.TypesInfo.TypeOf(callExpr)
	baseFields := make(map[string]FieldSchema)

	if wrapperReturnType != nil {
		baseFields = ra.parseTypeFields(wrapperReturnType)
	}

	// 特殊处理 SuccessResponse 和 ErrorResponse
	if funcName == "SuccessResponse" && len(callExpr.Args) >= 2 {
		// 分析第二个参数（data字段的实际数据）
		dataExpr := callExpr.Args[1]
		dataResponse := ra.AnalyzeResponse(dataExpr)

		// 处理data字段
		if dataField, exists := baseFields["Data"]; exists {
			if dataResponse != nil {
				dataField.Type = dataResponse.DataRealType
				if len(dataResponse.Fields) > 0 {
					dataField.Children = dataResponse.Fields
				}
			} else {
				// 处理nil值的情况
				dataFieldType := ra.pkg.TypesInfo.TypeOf(dataExpr)
				if dataFieldType != nil {
					dataField.Type = dataFieldType.String()
				} else {
					dataField.Type = "nil"
				}
			}
			baseFields["Data"] = dataField
		}

		return &APIResponse{
			ResponseType: "wrapped-success",
			DataRealType: wrapperReturnType.String(),
			Fields:       baseFields,
		}
	}

	if funcName == "ErrorResponse" {
		return &APIResponse{
			ResponseType: "wrapped-error",
			DataRealType: wrapperReturnType.String(),
			Fields:       baseFields,
		}
	}

	// 其他包装函数的通用处理
	if len(callExpr.Args) >= 2 {
		dataExpr := callExpr.Args[1]
		dataResponse := ra.AnalyzeResponse(dataExpr)
		if dataResponse != nil {
			// 合并基础字段和数据字段
			for k, v := range baseFields {
				if k == "Data" && dataResponse.Fields != nil {
					v.Children = dataResponse.Fields
					v.Type = dataResponse.DataRealType
				}
				dataResponse.Fields[k] = v
			}
			dataResponse.ResponseType = "wrapped-generic"
			return dataResponse
		}
	}

	return ra.AnalyzeResponse(callExpr) // fallback
}

// 检查是否是常见包装函数
func (ra *ResponseAnalyzer) isCommonWrapperFunction(funcName string) bool {
	// 常见包装函数名称
	wrappers := []string{"SuccessResponse", "ErrorResponse", "JSONResponse", "WrapResponse", "NewResponse"}
	for _, w := range wrappers {
		if funcName == w {
			return true
		}
	}
	return false
}

// 分析变量响应
func (ra *ResponseAnalyzer) analyzeVariable(expr ast.Expr) *APIResponse {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return nil
	}

	typ := ra.pkg.TypesInfo.TypeOf(ident)
	if typ == nil {
		return nil
	}

	return &APIResponse{
		ResponseType: "variable",
		DataRealType: typ.String(),
		Fields:       ra.parseTypeFields(typ),
	}
}

// 分析选择器表达式 (如 obj.Field)
func (ra *ResponseAnalyzer) analyzeSelector(expr ast.Expr) *APIResponse {
	selExpr, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	typ := ra.pkg.TypesInfo.TypeOf(selExpr)
	if typ == nil {
		return nil
	}

	return &APIResponse{
		ResponseType: "selector",
		DataRealType: typ.String(),
		Fields:       ra.parseTypeFields(typ),
	}
}

// 通用回退分析
func (ra *ResponseAnalyzer) analyzeFallback(expr ast.Expr) *APIResponse {
	typ := ra.pkg.TypesInfo.TypeOf(expr)
	if typ == nil {
		return &APIResponse{
			ResponseType: "unknown",
			DataRealType: "unknown",
		}
	}

	return &APIResponse{
		ResponseType: "fallback",
		DataRealType: typ.String(),
		Fields:       ra.parseTypeFields(typ),
	}
}

// 提取字符串键名
func (ra *ResponseAnalyzer) extractStringKey(expr ast.Expr) string {
	if basicLit, ok := expr.(*ast.BasicLit); ok && basicLit.Kind == token.STRING {
		// 去掉引号
		return strings.Trim(basicLit.Value, `"`)
	}
	return ""
}

// 提取字段名
func (ra *ResponseAnalyzer) extractFieldName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// 递归解析类型字段
func (ra *ResponseAnalyzer) parseTypeFields(typ types.Type) map[string]FieldSchema {
	fields := make(map[string]FieldSchema)

	// 解指针
	if ptr, ok := typ.Underlying().(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	// 检查是否是结构体
	if strct, ok := typ.Underlying().(*types.Struct); ok {
		for i := 0; i < strct.NumFields(); i++ {
			field := strct.Field(i)
			tag := strct.Tag(i)

			// 解析 json tag
			jsonTag := reflect.StructTag(tag).Get("json")
			if jsonTag == "" || jsonTag == "-" {
				continue
			}
			if idx := strings.Index(jsonTag, ","); idx != -1 {
				jsonTag = jsonTag[:idx]
			}
			if jsonTag == "" {
				jsonTag = field.Name()
			}

			// 创建字段 schema
			schema := FieldSchema{
				Type:    field.Type().String(),
				JSONTag: jsonTag,
				// IsPointer: types.IsPointer(field.Type()),
			}

			// 检查是否是切片
			if slice, ok := field.Type().Underlying().(*types.Slice); ok {
				schema.IsArray = true
				schema.Type = slice.Elem().String()
			}

			// 递归解析嵌套结构
			if ra.isStructOrMap(field.Type()) {
				schema.Children = ra.parseTypeFields(field.Type())
			}

			fields[field.Name()] = schema
		}
		return fields
	}

	// 检查是否是 map
	if mapType, ok := typ.Underlying().(*types.Map); ok {
		// 特殊处理：如果 key 是 string 且 value 是 interface
		if keyType, ok := mapType.Key().(*types.Basic); ok && keyType.Info()&types.IsString != 0 {
			// 尝试获取 map 值的类型
			valueType := mapType.Elem()

			// 检查是否是interface{}类型
			isInterfaceType := false
			if named, ok := valueType.(*types.Named); ok && named.Obj().Name() == "interface{}" {
				isInterfaceType = true
			}
			if inter, ok := valueType.(*types.Interface); ok && inter.NumMethods() == 0 {
				isInterfaceType = true
			}

			if isInterfaceType {
				// 动态类型，无法确定具体结构
				fields["<dynamic-key>"] = FieldSchema{
					Type:    "any",
					JSONTag: "<dynamic>",
				}
			} else {
				// 有具体类型
				fields["<value>"] = FieldSchema{
					Type:     valueType.String(),
					JSONTag:  "<value>",
					Children: ra.parseTypeFields(valueType),
				}
			}
		}
	}

	return fields
}

// 检查类型是否是结构体或 map
func (ra *ResponseAnalyzer) isStructOrMap(typ types.Type) bool {
	if typ == nil {
		return false
	}

	// 解指针
	if ptr, ok := typ.Underlying().(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	// 检查是否是结构体
	if _, ok := typ.Underlying().(*types.Struct); ok {
		return true
	}

	// 检查是否是 map
	if _, ok := typ.Underlying().(*types.Map); ok {
		return true
	}

	return false
}

// ====================== Gin Handler 分析器 ======================

// Gin Handler 分析器 (集成新的响应解析引擎)
type GinHandlerAnalyzer struct {
	pkgs                  []*packages.Package
	responseParsingEngine *ResponseParsingEngine
}

// 创建新的 Gin 分析器
func NewGinHandlerAnalyzer(dir string) (*GinHandlerAnalyzer, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedDeps,
		Tests: false,
		Dir:   dir, // 设置当前目录，确保能正确解析模块
		Env:   append(os.Environ(), "GOFLAGS=-mod=vendor"),
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("加载包失败: %w", err)
	}

	// 创建响应解析引擎并执行全局预处理
	engine := NewResponseParsingEngine(pkgs)

	return &GinHandlerAnalyzer{
		pkgs:                  pkgs,
		responseParsingEngine: engine,
	}, nil
}

// 分析所有 Gin Handler
func (a *GinHandlerAnalyzer) Analyze() {
	for _, pkg := range a.pkgs {
		if pkg.Types == nil {
			continue
		}

		for _, file := range pkg.Syntax {

			ast.Inspect(file, func(n ast.Node) bool {
				funcDecl, ok := n.(*ast.FuncDecl)
				if !ok {
					return true
				}

				// 跳过没有函数体的Handler（注释、接口声明等）
				if funcDecl.Body == nil {
					return true
				}

				if a.isGinHandler(funcDecl, pkg.TypesInfo) {
					// 使用新的完整Handler分析方法（包含请求参数和响应）
					result := a.responseParsingEngine.AnalyzeHandlerComplete(funcDecl, pkg)

					// 输出完整的分析结果（包含请求参数和响应）
					if jsonData, err := json.MarshalIndent(result, "", "  "); err == nil {
						fmt.Printf("📋 Handler分析结果:\n%s\n\n", string(jsonData))
					}
				}
				return true
			})
		}
	}
}

// 检查是否是 Gin Handler
func (a *GinHandlerAnalyzer) isGinHandler(funcDecl *ast.FuncDecl, info *types.Info) bool {
	if len(funcDecl.Type.Params.List) != 1 {
		return false
	}

	param := funcDecl.Type.Params.List[0]
	if paramType := info.TypeOf(param.Type); paramType != nil {
		typeStr := paramType.String()
		return strings.Contains(typeStr, "gin.Context")
	}
	return false
}

// 完整分析Handler（包含请求参数和响应）
func (engine *ResponseParsingEngine) AnalyzeHandlerComplete(handlerDecl *ast.FuncDecl, pkg *packages.Package) *HandlerAnalysisResult {
	result := &HandlerAnalysisResult{
		HandlerName: handlerDecl.Name.Name,
	}

	// 分析请求参数
	paramAnalyzer := NewRequestParamAnalyzer(engine, pkg)
	result.RequestParams = paramAnalyzer.AnalyzeHandlerParams(handlerDecl)

	// 分析响应
	responseExpr := engine.findLastResponseExpression(handlerDecl, pkg)
	if responseExpr != nil {
		result.Response = engine.analyzeUnifiedResponseExpression(responseExpr, pkg)
	}

	return result
}

// 统一分析响应表达式（支持c.JSON第二个参数和响应封装函数调用）
func (engine *ResponseParsingEngine) analyzeUnifiedResponseExpression(responseExpr ast.Expr, pkg *packages.Package) *APISchema {
	switch expr := responseExpr.(type) {
	case *ast.CallExpr:
		// 响应封装函数调用
		if engine.isResponseWrapperCall(expr, pkg) {
			return engine.resolveFunctionCallRecursive(expr, pkg)
		}
		// 其他函数调用
		return engine.resolveFunctionCallRecursive(expr, pkg)
	case *ast.CompositeLit:
		// 结构体字面量
		return engine.resolveCompositeLiteral(expr, pkg)
	case *ast.Ident:
		// 变量
		return engine.resolveIdentifier(expr, pkg)
	case *ast.SelectorExpr:
		// 选择器表达式
		return engine.resolveSelectorExpr(expr, pkg)
	default:
		// 使用通用的类型解析
		if exprType := pkg.TypesInfo.TypeOf(responseExpr); exprType != nil {
			return engine.resolveType(exprType, engine.maxDepth)
		}
		return &APISchema{
			Type:        "unknown",
			Description: fmt.Sprintf("unsupported expression type: %T", responseExpr),
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: go run main.go <项目目录>")
		fmt.Println("示例: go run main.go ./my-gin-project")
		os.Exit(1)
	}

	projectDir := os.Args[1]
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		fmt.Printf("❌ 目录不存在: %s\n", projectDir)
		os.Exit(1)
	}

	fmt.Printf("🔍 开始解析项目: %s\n", projectDir)

	analyzer, err := NewGinHandlerAnalyzer(projectDir)
	if err != nil {
		log.Fatalf("❌ 初始化分析器失败: %v", err)
	}

	analyzer.Analyze()
	fmt.Println("\n✅ 解析完成")
}

// 查找最后一个响应表达式 (c.JSON 或响应封装函数调用)
func (engine *ResponseParsingEngine) findLastResponseExpression(handlerDecl *ast.FuncDecl, pkg *packages.Package) ast.Expr {
	var lastResponseExpr ast.Expr

	if handlerDecl.Body == nil {
		return nil
	}

	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			// 检查是否为c.JSON调用
			if engine.isGinJSONCall(callExpr, pkg) {
				if len(callExpr.Args) >= 2 {
					lastResponseExpr = callExpr.Args[1]
					fmt.Printf("[DEBUG] 找到c.JSON调用，响应表达式类型: %T\n", lastResponseExpr)
				}
			} else if engine.isResponseWrapperCall(callExpr, pkg) {
				// 检查是否为响应封装函数调用
				lastResponseExpr = callExpr
				fmt.Printf("[DEBUG] 找到响应封装函数调用: %T\n", lastResponseExpr)
			}
		}
		return true
	})

	return lastResponseExpr
}

// 检查是否为响应封装函数调用
func (engine *ResponseParsingEngine) isResponseWrapperCall(callExpr *ast.CallExpr, pkg *packages.Package) bool {
	funcObj := engine.getFunctionObject(callExpr, pkg)
	if funcObj == nil {
		return false
	}

	_, isWrapper := engine.globalMappings.ResponseWrappers[funcObj]
	return isWrapper
}

// ========== 请求参数解析功能 ==========

// 创建请求参数分析器
func NewRequestParamAnalyzer(engine *ResponseParsingEngine, pkg *packages.Package) *RequestParamAnalyzer {
	return &RequestParamAnalyzer{
		engine:     engine,
		typeInfo:   pkg.TypesInfo,
		currentPkg: pkg,
	}
}

// 分析Handler的请求参数
func (analyzer *RequestParamAnalyzer) AnalyzeHandlerParams(handlerDecl *ast.FuncDecl) []RequestParamInfo {
	var params []RequestParamInfo

	if handlerDecl.Body == nil {
		return params
	}

	fmt.Printf("[DEBUG] 开始分析Handler请求参数: %s\n", handlerDecl.Name.Name)

	// 遍历函数体，查找参数绑定调用
	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			// 分析Query参数
			if queryParams := analyzer.analyzeQueryParams(callExpr); len(queryParams) > 0 {
				params = append(params, queryParams...)
			}

			// 分析Body参数
			if bodyParams := analyzer.analyzeBodyParams(callExpr); len(bodyParams) > 0 {
				params = append(params, bodyParams...)
			}
		}
		return true
	})

	fmt.Printf("[DEBUG] Handler %s 发现 %d 个请求参数\n", handlerDecl.Name.Name, len(params))
	return params
}

// 分析Query参数
func (analyzer *RequestParamAnalyzer) analyzeQueryParams(callExpr *ast.CallExpr) []RequestParamInfo {
	var params []RequestParamInfo

	if !analyzer.isGinContextCall(callExpr) {
		return params
	}

	methodName := analyzer.getMethodName(callExpr)
	switch methodName {
	case "Query":
		// c.Query("key") -> string
		if param := analyzer.analyzeQueryCall(callExpr); param != nil {
			params = append(params, *param)
		}
	case "ShouldBindQuery":
		// c.ShouldBindQuery(&struct{}) -> struct type
		if param := analyzer.analyzeShouldBindQueryCall(callExpr); param != nil {
			params = append(params, *param)
		}
	case "QueryArray":
		// c.QueryArray("key") -> []string
		if param := analyzer.analyzeQueryArrayCall(callExpr); param != nil {
			params = append(params, *param)
		}
	case "QueryMap":
		// c.QueryMap("key") -> map[string]string
		if param := analyzer.analyzeQueryMapCall(callExpr); param != nil {
			params = append(params, *param)
		}
	}

	return params
}

// 分析Body参数
func (analyzer *RequestParamAnalyzer) analyzeBodyParams(callExpr *ast.CallExpr) []RequestParamInfo {
	var params []RequestParamInfo

	if !analyzer.isGinContextCall(callExpr) {
		return params
	}

	methodName := analyzer.getMethodName(callExpr)
	switch methodName {
	case "ShouldBindJSON":
		// c.ShouldBindJSON(&struct{}) -> struct type
		if param := analyzer.analyzeShouldBindJSONCall(callExpr); param != nil {
			params = append(params, *param)
		}
	case "Bind":
		// c.Bind(&struct{}) -> struct type
		if param := analyzer.analyzeBindCall(callExpr); param != nil {
			params = append(params, *param)
		}
	case "ShouldBind":
		// c.ShouldBind(&struct{}) -> struct type (supports multiple formats)
		if param := analyzer.analyzeShouldBindCall(callExpr); param != nil {
			params = append(params, *param)
		}
	case "ShouldBindUri":
		// c.ShouldBindUri(&struct{}) -> URI parameters
		if param := analyzer.analyzeShouldBindUriCall(callExpr); param != nil {
			params = append(params, *param)
		}
	}

	return params
}

// 检查是否为gin.Context的方法调用
func (analyzer *RequestParamAnalyzer) isGinContextCall(callExpr *ast.CallExpr) bool {
	if selector, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selector.X.(*ast.Ident); ok {
			if obj := analyzer.typeInfo.ObjectOf(ident); obj != nil {
				typeStr := obj.Type().String()
				return strings.Contains(typeStr, "gin.Context")
			}
		}
	}
	return false
}

// 获取方法名
func (analyzer *RequestParamAnalyzer) getMethodName(callExpr *ast.CallExpr) string {
	if selector, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		return selector.Sel.Name
	}
	return ""
}

// 分析c.Query()调用
func (analyzer *RequestParamAnalyzer) analyzeQueryCall(callExpr *ast.CallExpr) *RequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	// 获取参数名
	paramName := analyzer.extractStringFromExpr(callExpr.Args[0])
	if paramName == "" {
		return nil
	}

	return &RequestParamInfo{
		ParamType: "query",
		ParamName: paramName,
		ParamSchema: &APISchema{
			Type:        "string",
			Description: "Query parameter from c.Query()",
		},
		IsRequired: false, // Query参数通常是可选的
		Source:     "c.Query",
	}
}

// 分析c.ShouldBindQuery()调用
func (analyzer *RequestParamAnalyzer) analyzeShouldBindQueryCall(callExpr *ast.CallExpr) *RequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	// 获取绑定的结构体类型
	schema := analyzer.extractStructSchemaFromArg(callExpr.Args[0])
	if schema == nil {
		return nil
	}

	return &RequestParamInfo{
		ParamType:   "query",
		ParamName:   "query_struct",
		ParamSchema: schema,
		IsRequired:  false,
		Source:      "c.ShouldBindQuery",
	}
}

// 分析c.QueryArray()调用
func (analyzer *RequestParamAnalyzer) analyzeQueryArrayCall(callExpr *ast.CallExpr) *RequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	paramName := analyzer.extractStringFromExpr(callExpr.Args[0])
	if paramName == "" {
		return nil
	}

	return &RequestParamInfo{
		ParamType: "query",
		ParamName: paramName,
		ParamSchema: &APISchema{
			Type: "array",
			Items: &APISchema{
				Type: "string",
			},
			Description: "Query array parameter from c.QueryArray()",
		},
		IsRequired: false,
		Source:     "c.QueryArray",
	}
}

// 分析c.QueryMap()调用
func (analyzer *RequestParamAnalyzer) analyzeQueryMapCall(callExpr *ast.CallExpr) *RequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	paramName := analyzer.extractStringFromExpr(callExpr.Args[0])
	if paramName == "" {
		return nil
	}

	return &RequestParamInfo{
		ParamType: "query",
		ParamName: paramName,
		ParamSchema: &APISchema{
			Type:        "object",
			Description: "Query map parameter from c.QueryMap() -> map[string]string",
		},
		IsRequired: false,
		Source:     "c.QueryMap",
	}
}

// 分析c.ShouldBindJSON()调用
func (analyzer *RequestParamAnalyzer) analyzeShouldBindJSONCall(callExpr *ast.CallExpr) *RequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	schema := analyzer.extractStructSchemaFromArg(callExpr.Args[0])
	if schema == nil {
		return nil
	}

	return &RequestParamInfo{
		ParamType:   "body",
		ParamName:   "request_body",
		ParamSchema: schema,
		IsRequired:  true, // Body参数通常是必需的
		Source:      "c.ShouldBindJSON",
	}
}

// 分析c.Bind()调用
func (analyzer *RequestParamAnalyzer) analyzeBindCall(callExpr *ast.CallExpr) *RequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	schema := analyzer.extractStructSchemaFromArg(callExpr.Args[0])
	if schema == nil {
		return nil
	}

	return &RequestParamInfo{
		ParamType:   "body",
		ParamName:   "request_body",
		ParamSchema: schema,
		IsRequired:  true,
		Source:      "c.Bind",
	}
}

// 分析c.ShouldBind()调用
func (analyzer *RequestParamAnalyzer) analyzeShouldBindCall(callExpr *ast.CallExpr) *RequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	schema := analyzer.extractStructSchemaFromArg(callExpr.Args[0])
	if schema == nil {
		return nil
	}

	return &RequestParamInfo{
		ParamType:   "body", // ShouldBind 通常用于 body 绑定，也支持 form、query 等多种格式
		ParamName:   "request_body",
		ParamSchema: schema,
		IsRequired:  true,
		Source:      "c.ShouldBind",
	}
}

// 分析c.ShouldBindUri()调用
func (analyzer *RequestParamAnalyzer) analyzeShouldBindUriCall(callExpr *ast.CallExpr) *RequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	schema := analyzer.extractStructSchemaFromArg(callExpr.Args[0])
	if schema == nil {
		return nil
	}

	return &RequestParamInfo{
		ParamType:   "path",
		ParamName:   "uri_params",
		ParamSchema: schema,
		IsRequired:  true, // URI参数通常是必需的
		Source:      "c.ShouldBindUri",
	}
}

// 从表达式中提取字符串字面量
func (analyzer *RequestParamAnalyzer) extractStringFromExpr(expr ast.Expr) string {
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		// 移除引号
		return strings.Trim(lit.Value, `"`)
	}
	return ""
}

// 从参数中提取结构体Schema
func (analyzer *RequestParamAnalyzer) extractStructSchemaFromArg(arg ast.Expr) *APISchema {
	// 处理&struct{}形式的参数
	if unaryExpr, ok := arg.(*ast.UnaryExpr); ok && unaryExpr.Op == token.AND {
		arg = unaryExpr.X
	}

	// 获取类型信息
	argType := analyzer.typeInfo.TypeOf(arg)
	if argType == nil {
		return nil
	}

	// 处理指针类型
	if ptr, ok := argType.(*types.Pointer); ok {
		argType = ptr.Elem()
	}

	// 使用现有的响应解析引擎来解析结构体
	return analyzer.engine.resolveType(argType, analyzer.engine.maxDepth)
}
