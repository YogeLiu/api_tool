package helper

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

// ====================== Iris特定的数据结构 ======================

// IrisHandlerAnalysisResult Iris Handler分析结果
type IrisHandlerAnalysisResult struct {
	PackageName   string                 `json:"package_name"`
	PackagePath   string                 `json:"package_path"`
	HandlerName   string                 `json:"handler"`
	RequestParams []IrisRequestParamInfo `json:"request_params,omitempty"`
	Response      *APISchema             `json:"response,omitempty"`
}

// IrisRequestParamInfo Iris请求参数信息
type IrisRequestParamInfo struct {
	ParamType   string     `json:"param_type"`   // "query", "body", "path", "values"
	ParamName   string     `json:"param_name"`   // 参数名称
	ParamSchema *APISchema `json:"param_schema"` // 参数结构
	IsRequired  bool       `json:"is_required"`  // 是否必需
	Source      string     `json:"source"`       // 来源方法: "ctx.ReadJSON", "ctx.Params", etc.
}

// IrisResponseParsingEngine Iris响应解析引擎
type IrisResponseParsingEngine struct {
	allPackages    []*packages.Package
	globalMappings *IrisGlobalMappings
	maxDepth       int
}

// IrisGlobalMappings Iris全局映射
type IrisGlobalMappings struct {
	ResponseWrappers map[*types.Func]*IrisResponseWrapperFunc `json:"-"`
	StructTagMap     map[*types.Named]map[string]string       `json:"-"`
}

// IrisResponseWrapperFunc Iris响应封装函数信息
type IrisResponseWrapperFunc struct {
	FuncObj         *types.Func
	IrisContextIdx  int            // iris.Context 参数索引
	DataParamIdx    int            // 数据参数索引
	JSONCallSite    *ast.CallExpr  // 内部 ctx.JSON 调用位置
	ReturnType      *types.Named   // 返回的结构体类型
	ParamToFieldMap map[string]int // 参数→字段映射
}

// ====================== 创建Iris响应解析引擎 ======================

// NewIrisResponseParsingEngine 创建新的Iris响应解析引擎
func NewIrisResponseParsingEngine(packages []*packages.Package) *IrisResponseParsingEngine {
	engine := &IrisResponseParsingEngine{
		allPackages: packages,
		maxDepth:    10,
		globalMappings: &IrisGlobalMappings{
			ResponseWrappers: make(map[*types.Func]*IrisResponseWrapperFunc),
			StructTagMap:     make(map[*types.Named]map[string]string),
		},
	}

	// 执行全局预处理
	engine.performGlobalPreprocessing()
	return engine
}

// performGlobalPreprocessing 全局预处理阶段
func (engine *IrisResponseParsingEngine) performGlobalPreprocessing() {
	log.Printf("[DEBUG] 开始Iris全局预处理阶段...\n")

	for _, pkg := range engine.allPackages {
		engine.preprocessPackage(pkg)
	}

	log.Printf("[DEBUG] 🔍 Iris全局预处理完成: 发现 %d 个响应封装函数, %d 个结构体\n",
		len(engine.globalMappings.ResponseWrappers),
		len(engine.globalMappings.StructTagMap))

	// 输出所有发现的响应封装函数
	for funcObj, wrapper := range engine.globalMappings.ResponseWrappers {
		log.Printf("[DEBUG] 🔍 响应封装函数: %s，数据参数索引: %d\n", funcObj.Name(), wrapper.DataParamIdx)
	}
}

// preprocessPackage 预处理单个包
func (engine *IrisResponseParsingEngine) preprocessPackage(pkg *packages.Package) {
	if pkg.Types == nil {
		return
	}

	// 1. 构建结构体字段的JSON Tag映射
	engine.buildStructTagMap(pkg)

	// 2. 识别响应封装函数
	engine.identifyResponseWrapperFunctions(pkg)
}

// identifyResponseWrapperFunctions 识别响应封装函数
func (engine *IrisResponseParsingEngine) identifyResponseWrapperFunctions(pkg *packages.Package) {
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				if funcDecl.Body == nil {
					continue
				}

				// 检查是否为响应封装函数
				if wrapper := engine.analyzeResponseWrapperCandidate(funcDecl, pkg); wrapper != nil {
					funcObj := pkg.TypesInfo.ObjectOf(funcDecl.Name).(*types.Func)
					engine.globalMappings.ResponseWrappers[funcObj] = wrapper
					log.Printf("[DEBUG] 🔍 发现Iris响应封装函数: %s，数据参数索引: %d\n", funcDecl.Name.Name, wrapper.DataParamIdx)
				}
			}
		}
	}
}

// analyzeResponseWrapperCandidate 分析函数是否为响应封装函数
func (engine *IrisResponseParsingEngine) analyzeResponseWrapperCandidate(funcDecl *ast.FuncDecl, pkg *packages.Package) *IrisResponseWrapperFunc {
	if funcDecl.Type.Params == nil || len(funcDecl.Type.Params.List) < 1 {
		return nil
	}

	// 1. 查找iris.Context参数（可能不是第一个参数）
	irisContextIdx := engine.findIrisContextParameter(funcDecl, pkg)

	// 2. 如果没有iris.Context参数，检查是否内部调用了ctx.JSON
	hasInternalJSONCall := false
	if irisContextIdx == -1 {
		// 检查函数体内是否有ctx.JSON调用
		jsonCall := engine.findJSONCallInFunction(funcDecl, pkg)
		if jsonCall != nil {
			hasInternalJSONCall = true
		}
	}

	// 如果既没有iris.Context参数，也没有内部JSON调用，检查是否为响应构造函数
	if irisContextIdx == -1 && !hasInternalJSONCall {
		// 检查是否为响应构造函数（返回类型包含Response且有数据参数）
		if engine.isResponseConstructorFunction(funcDecl, pkg) {
			log.Printf("[DEBUG] 🎯 发现响应构造函数: %s\n", funcDecl.Name.Name)
		} else {
			return nil
		}
	}

	// 3. 确保不是Handler (Handler只有一个iris.Context参数)
	if engine.isIrisHandlerFunction(funcDecl, pkg.TypesInfo) {
		log.Printf("[DEBUG] 🚫 跳过Handler函数: %s\n", funcDecl.Name.Name)
		return nil
	}

	// 4. 查找函数体内的ctx.JSON调用
	jsonCallSite := engine.findJSONCallInFunction(funcDecl, pkg)

	// 5. 获取返回类型
	returnType := engine.getReturnStructType(funcDecl, pkg)

    // 6. 查找数据参数索引
    dataParamIdx := engine.findDataParameter(funcDecl, irisContextIdx)

    // 针对“响应构造函数”的特殊处理：
    // - 无 iris.Context 参数
    // - 无内部 ctx.JSON 调用
    // - 函数名或返回类型包含 Response
    // 这里进一步推断 Data 参数在调用实参中的索引。
    if irisContextIdx == -1 && !hasInternalJSONCall && engine.isResponseConstructorFunction(funcDecl, pkg) {
        // 情况 A：固定参数风格，例如 Response(code, msg, data)
        if strings.ToLower(funcDecl.Name.Name) == "response" {
            dataParamIdx = 2
            log.Printf("[DEBUG] 🎯 Response构造函数固定参数推断，数据参数索引: %d\n", dataParamIdx)
        } else {
            // 情况 B：变长参数风格，例如 ResponseWithRequestId(requestId string, arg ...interface{})
            // 社区/项目常见约定：arg[0]=code, arg[1]=msg, arg[2]=data, arg[3]=next
            // 因此在调用处，data 的索引 = 固定参数数量 + 2

            // 统计变长参数前的固定参数数量
            fixedParamCount := 0
            hasVariadic := false
            if funcDecl.Type.Params != nil && len(funcDecl.Type.Params.List) > 0 {
                lastIdx := len(funcDecl.Type.Params.List) - 1
                for i, p := range funcDecl.Type.Params.List {
                    // 检查是否为变长参数
                    if i == lastIdx {
                        if _, ok := p.Type.(*ast.Ellipsis); ok {
                            hasVariadic = true
                            // 固定参数不包含最后一个变长参数
                            break
                        }
                    }
                    // 计算该列表中的参数个数（a,b int 这种合并声明）
                    if len(p.Names) > 0 {
                        fixedParamCount += len(p.Names)
                    } else {
                        // 处理无参数名的情况（极少见），按1个计
                        fixedParamCount += 1
                    }
                }
            }

            if hasVariadic {
                dataParamIdx = fixedParamCount + 2
                log.Printf("[DEBUG] 🎯 变长响应构造函数推断，固定参数=%d，数据参数索引=%d\n", fixedParamCount, dataParamIdx)
            } else {
                // 非变长但又不是函数名为 Response 的场景，尽量保留原始推断结果
                log.Printf("[DEBUG] ℹ️ 非变长响应构造函数，沿用默认数据参数索引: %d\n", dataParamIdx)
            }
        }
    }

	// 7. 分析参数→字段映射
	paramToFieldMap := engine.analyzeParameterFieldMapping(funcDecl, pkg)

	return &IrisResponseWrapperFunc{
		FuncObj:         pkg.TypesInfo.ObjectOf(funcDecl.Name).(*types.Func),
		IrisContextIdx:  irisContextIdx,
		DataParamIdx:    dataParamIdx,
		JSONCallSite:    jsonCallSite,
		ReturnType:      returnType,
		ParamToFieldMap: paramToFieldMap,
	}
}

// findDataFieldKey 查找Data字段的键名
func (engine *IrisResponseParsingEngine) findDataFieldKey(properties map[string]*APISchema) string {
	// 首先尝试直接匹配"Data"字段
	if _, exists := properties["Data"]; exists {
		return "Data"
	}

	// 然后尝试直接匹配"data"字段
	if _, exists := properties["data"]; exists {
		return "data"
	}

	// 最后通过JSON标签匹配
	for key, schema := range properties {
		if schema.JSONTag == "data" && (schema.Type == "any" || schema.Description == "interface{}") {
			return key
		}
	}

	return ""
}

// isResponseConstructorFunction 检查是否为响应构造函数
func (engine *IrisResponseParsingEngine) isResponseConstructorFunction(funcDecl *ast.FuncDecl, pkg *packages.Package) bool {
	// 检查函数名是否包含Response相关关键词
	funcName := strings.ToLower(funcDecl.Name.Name)
	if !strings.Contains(funcName, "response") {
		return false
	}

	// 检查是否有返回值
	if funcDecl.Type.Results == nil || len(funcDecl.Type.Results.List) == 0 {
		return false
	}

	// 检查返回类型是否为Response相关结构体
	for _, result := range funcDecl.Type.Results.List {
		if resultType := pkg.TypesInfo.TypeOf(result.Type); resultType != nil {
			resultTypeStr := strings.ToLower(resultType.String())
			if strings.Contains(resultTypeStr, "response") {
				log.Printf("[DEBUG] 🎯 响应构造函数返回类型: %s\n", resultType.String())
				return true
			}
		}
	}

	return false
}

// findIrisContextParameter 查找iris.Context参数索引
func (engine *IrisResponseParsingEngine) findIrisContextParameter(funcDecl *ast.FuncDecl, pkg *packages.Package) int {
	paramIdx := 0
	for _, paramList := range funcDecl.Type.Params.List {
		for range paramList.Names {
			if engine.isIrisContextType(paramList.Type, pkg) {
				return paramIdx
			}
			paramIdx++
		}
	}
	return -1
}

// isIrisContextType 检查类型是否为iris.Context
func (engine *IrisResponseParsingEngine) isIrisContextType(expr ast.Expr, pkg *packages.Package) bool {
	if paramType := pkg.TypesInfo.TypeOf(expr); paramType != nil {
		typeStr := paramType.String()
		// Iris Context可能的类型表示
		return strings.Contains(typeStr, "iris.Context") ||
			strings.Contains(typeStr, "github.com/kataras/iris.Context") ||
			strings.Contains(typeStr, "github.com/kataras/iris/context.Context")
	}
	return false
}

// isIrisHandlerFunction 检查是否为Iris Handler
func (engine *IrisResponseParsingEngine) isIrisHandlerFunction(funcDecl *ast.FuncDecl, typeInfo *types.Info) bool {
	log.Printf("[DEBUG] 🔍 检查是否为Handler: %s，参数列表数: %d\n", funcDecl.Name.Name, len(funcDecl.Type.Params.List))

	if funcDecl.Type.Params == nil || len(funcDecl.Type.Params.List) != 1 {
		log.Printf("[DEBUG] 🔍 不是Handler（参数数量!=1）: %s\n", funcDecl.Name.Name)
		return false
	}

	param := funcDecl.Type.Params.List[0]
	if len(param.Names) != 1 {
		log.Printf("[DEBUG] 🔍 不是Handler（参数名数量!=1）: %s\n", funcDecl.Name.Name)
		return false
	}

	if paramType := typeInfo.TypeOf(param.Type); paramType != nil {
		typeStr := paramType.String()
		isHandler := strings.Contains(typeStr, "iris.Context") ||
			strings.Contains(typeStr, "github.com/kataras/iris.Context") ||
			strings.Contains(typeStr, "github.com/kataras/iris/context.Context")
		log.Printf("[DEBUG] 🔍 参数类型: %s，是否Handler: %v\n", typeStr, isHandler)
		return isHandler
	}
	log.Printf("[DEBUG] 🔍 无法获取参数类型: %s\n", funcDecl.Name.Name)
	return false
}

// findJSONCallInFunction 查找函数内的ctx.JSON调用
func (engine *IrisResponseParsingEngine) findJSONCallInFunction(funcDecl *ast.FuncDecl, pkg *packages.Package) *ast.CallExpr {
	if funcDecl.Body == nil {
		return nil
	}

	var jsonCall *ast.CallExpr
	ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			if engine.isIrisJSONCall(callExpr, pkg) {
				jsonCall = callExpr
				return false
			}
		}
		return true
	})

	return jsonCall
}

// isIrisJSONCall 检查是否为iris.Context的JSON调用
func (engine *IrisResponseParsingEngine) isIrisJSONCall(callExpr *ast.CallExpr, pkg *packages.Package) bool {
	if selExpr, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		// 检查方法名是否为JSON
		if selExpr.Sel.Name != "JSON" {
			return false
		}

		// 检查调用对象是否为iris.Context类型
		if ident, ok := selExpr.X.(*ast.Ident); ok {
			if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
				objTypeStr := obj.Type().String()
				return strings.Contains(objTypeStr, "iris.Context") ||
					strings.Contains(objTypeStr, "github.com/kataras/iris.Context") ||
					strings.Contains(objTypeStr, "github.com/kataras/iris/context.Context")
			}
		}
	}
	return false
}

// ====================== Handler分析 ======================

// AnalyzeIrisHandlerComplete 完整分析Iris Handler
func (engine *IrisResponseParsingEngine) AnalyzeIrisHandlerComplete(handlerDecl *ast.FuncDecl, pkg *packages.Package) *IrisHandlerAnalysisResult {
	result := &IrisHandlerAnalysisResult{
		PackageName: pkg.Name,
		PackagePath: pkg.PkgPath,
		HandlerName: handlerDecl.Name.Name,
	}

	// 分析请求参数
	paramAnalyzer := NewIrisRequestParamAnalyzer(engine, pkg)
	result.RequestParams = paramAnalyzer.AnalyzeHandlerParams(handlerDecl)

	// 分析响应
	responseExpr := engine.findLastResponseExpression(handlerDecl, pkg)
	if responseExpr != nil {
		result.Response = engine.analyzeResponseExpression(responseExpr, pkg)
	}

	return result
}

// findLastResponseExpression 查找最后一个响应表达式
func (engine *IrisResponseParsingEngine) findLastResponseExpression(handlerDecl *ast.FuncDecl, pkg *packages.Package) ast.Expr {
	var lastResponseExpr ast.Expr

	if handlerDecl.Body == nil {
		return nil
	}

	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			// 检查是否为ctx.JSON调用
			if engine.isIrisJSONCall(callExpr, pkg) {
				if len(callExpr.Args) >= 1 {
					lastResponseExpr = callExpr.Args[0]
					log.Printf("[DEBUG] 找到ctx.JSON调用，响应表达式类型: %T\n", lastResponseExpr)
				}
			} else if engine.isResponseWrapperCall(callExpr, pkg) {
				// 检查是否为响应封装函数调用
				lastResponseExpr = callExpr
				log.Printf("[DEBUG] 找到响应封装函数调用: %T\n", lastResponseExpr)
			}
		}
		return true
	})

	return lastResponseExpr
}

// isResponseWrapperCall 检查是否为响应封装函数调用
func (engine *IrisResponseParsingEngine) isResponseWrapperCall(callExpr *ast.CallExpr, pkg *packages.Package) bool {
	funcObj := engine.getFunctionObject(callExpr, pkg)
	if funcObj == nil {
		return false
	}

	_, isWrapper := engine.globalMappings.ResponseWrappers[funcObj]
	return isWrapper
}

// analyzeResponseExpression 分析响应表达式
func (engine *IrisResponseParsingEngine) analyzeResponseExpression(expr ast.Expr, pkg *packages.Package) *APISchema {
	log.Printf("[DEBUG] 分析Iris响应表达式: %T\n", expr)

	switch e := expr.(type) {
	case *ast.CallExpr:
		// 函数调用 - 递归解析
		return engine.resolveFunctionCall(e, pkg)
	case *ast.CompositeLit:
		// 字面量表达式
		return engine.resolveCompositeLiteral(e, pkg)
	case *ast.Ident:
		// 标识符
		return engine.resolveIdentifier(e, pkg)
	case *ast.SelectorExpr:
		// 选择器表达式
		return engine.resolveSelectorExpr(e, pkg)
	default:
		// 使用类型信息解析
		if exprType := pkg.TypesInfo.TypeOf(expr); exprType != nil {
			return engine.resolveType(exprType, engine.maxDepth)
		}
		return &APISchema{Type: "unknown", Description: "unsupported expression type"}
	}
}

// resolveFunctionCall 解析函数调用
func (engine *IrisResponseParsingEngine) resolveFunctionCall(callExpr *ast.CallExpr, pkg *packages.Package) *APISchema {
	log.Printf("[DEBUG] 解析函数调用\n")

	// 获取函数对象
	funcObj := engine.getFunctionObject(callExpr, pkg)
	if funcObj == nil {
		log.Printf("[DEBUG] 无法获取函数对象\n")
		return engine.resolveFallbackType(callExpr, pkg)
	}

	log.Printf("[DEBUG] 函数名: %s\n", funcObj.Name())

	// 检查是否为响应封装函数
	if wrapper, ok := engine.globalMappings.ResponseWrappers[funcObj]; ok {
		log.Printf("[DEBUG] 🎯 发现响应封装函数: %s，参数数量: %d，数据参数索引: %d\n", funcObj.Name(), len(callExpr.Args), wrapper.DataParamIdx)
		return engine.analyzeWrapperFunctionArgs(wrapper, callExpr.Args, pkg)
	}

	// 普通函数：分析返回值
	funcDecl := engine.findFunctionDeclaration(funcObj, pkg)
	if funcDecl == nil {
		log.Printf("[DEBUG] 无法找到函数声明\n")
		return engine.resolveFunctionByTypeInfo(callExpr, pkg)
	}

	return engine.analyzeFunctionReturn(funcDecl, callExpr.Args, pkg)
}

// analyzeWrapperFunctionArgs 分析封装函数参数
func (engine *IrisResponseParsingEngine) analyzeWrapperFunctionArgs(wrapper *IrisResponseWrapperFunc, callArgs []ast.Expr, pkg *packages.Package) *APISchema {
	log.Printf("[DEBUG] 分析封装函数参数，参数数量: %d，数据参数索引: %d\n", len(callArgs), wrapper.DataParamIdx)

	// 创建基础响应结构
	baseSchema := &APISchema{
		Type: "object",
		Properties: map[string]*APISchema{
			"request_id": {Type: "string", JSONTag: "request_id"},
			"code":       {Type: "integer", JSONTag: "code"},
			"message":    {Type: "string", JSONTag: "message"},
			"data":       {Type: "any", JSONTag: "data", Description: "interface{}"},
		},
	}

	// 如果封装函数有返回类型，使用返回类型替换基础结构
	if wrapper.ReturnType != nil {
		baseSchema = engine.resolveType(wrapper.ReturnType, engine.maxDepth)
		log.Printf("[DEBUG] 使用封装函数返回类型: %s\n", wrapper.ReturnType.String())
	}

	// 如果有数据参数，解析其具体类型
	if wrapper.DataParamIdx >= 0 && wrapper.DataParamIdx < len(callArgs) {
		dataArg := callArgs[wrapper.DataParamIdx]
		log.Printf("[DEBUG] 📋 分析数据参数[%d]: %T\n", wrapper.DataParamIdx, dataArg)

		// 打印参数的具体内容（用于调试）
		if ident, ok := dataArg.(*ast.Ident); ok {
			log.Printf("[DEBUG] 📋 参数是标识符: %s\n", ident.Name)
		} else if sel, ok := dataArg.(*ast.SelectorExpr); ok {
			log.Printf("[DEBUG] 📋 参数是选择器: %s\n", engine.getExpressionString(sel))
		}

		// 首先尝试使用类型信息直接解析（适用于变量和简单表达式）
		dataType := pkg.TypesInfo.TypeOf(dataArg)
		if dataType != nil {
			log.Printf("[DEBUG] 📋 数据参数类型: %s\n", dataType.String())
			injectedSchema := engine.resolveType(dataType, engine.maxDepth)
			log.Printf("[DEBUG] ✅ 参数类型注入成功: Data字段 interface{} -> %s\n", injectedSchema.Type)
			log.Printf("[DEBUG] ✅ 注入的详细模式: %+v\n", injectedSchema)

			// 找到并替换 Data 字段的类型信息
			if baseSchema.Properties != nil {
				// 查找Data字段（可能是"Data"、"data"或通过JSON标签匹配）
				dataFieldKey := engine.findDataFieldKey(baseSchema.Properties)
				if dataFieldKey != "" {
					baseSchema.Properties[dataFieldKey] = injectedSchema
					log.Printf("[DEBUG] ✅ 替换字段 %s 的类型信息成功\n", dataFieldKey)
				} else {
					// 如果找不到，添加为data字段
					baseSchema.Properties["data"] = injectedSchema
					log.Printf("[DEBUG] ✅ 添加新的data字段成功\n")
				}
			} else {
				log.Printf("[DEBUG] ❌ baseSchema.Properties 为 nil\n")
			}
			return baseSchema
		} else {
			log.Printf("[DEBUG] ❌ 无法获取类型信息，尝试递归解析表达式\n")
			// 如果类型信息不可用，回退到递归解析（适用于复杂表达式如选择器）
			dataSchema := engine.analyzeResponseExpression(dataArg, pkg)

			if baseSchema.Properties != nil {
				// 查找Data字段并替换
				dataFieldKey := engine.findDataFieldKey(baseSchema.Properties)
				if dataFieldKey != "" {
					baseSchema.Properties[dataFieldKey] = dataSchema
					log.Printf("[DEBUG] 递归解析：替换字段 %s 的类型: %s\n", dataFieldKey, dataSchema.Type)
				} else {
					baseSchema.Properties["data"] = dataSchema
					log.Printf("[DEBUG] 递归解析：添加新的data字段: %s\n", dataSchema.Type)
				}
			}
			return baseSchema
		}
	} else {
		log.Printf("[DEBUG] ❌ 数据参数索引无效或超出范围: DataParamIdx=%d, CallArgsLen=%d\n", wrapper.DataParamIdx, len(callArgs))
	}

	// 如果没有数据参数，但有其他参数，尝试解析第一个非Context参数
	if len(callArgs) > 0 {
		for i, arg := range callArgs {
			if i == wrapper.IrisContextIdx {
				continue
			}

			log.Printf("[DEBUG] 分析非Context参数 [%d]，类型: %T\n", i, arg)
			argSchema := engine.analyzeResponseExpression(arg, pkg)

			if argSchema.Type != "unknown" {
				// 如果参数解析成功，将其作为整体响应
				if baseSchema.Properties != nil {
					baseSchema.Properties["data"] = argSchema
					log.Printf("[DEBUG] 将参数作为数据字段: %s\n", argSchema.Type)
				}
				return baseSchema
			}
		}
	}

	log.Printf("[DEBUG] 无法解析封装函数参数，返回默认结构\n")
	return baseSchema
}

// ====================== 请求参数分析 ======================

// IrisRequestParamAnalyzer Iris请求参数分析器
type IrisRequestParamAnalyzer struct {
	engine     *IrisResponseParsingEngine
	typeInfo   *types.Info
	currentPkg *packages.Package
}

// NewIrisRequestParamAnalyzer 创建Iris请求参数分析器
func NewIrisRequestParamAnalyzer(engine *IrisResponseParsingEngine, pkg *packages.Package) *IrisRequestParamAnalyzer {
	return &IrisRequestParamAnalyzer{
		engine:     engine,
		typeInfo:   pkg.TypesInfo,
		currentPkg: pkg,
	}
}

// AnalyzeHandlerParams 分析Handler的请求参数
func (analyzer *IrisRequestParamAnalyzer) AnalyzeHandlerParams(handlerDecl *ast.FuncDecl) []IrisRequestParamInfo {
	var params []IrisRequestParamInfo

	if handlerDecl.Body == nil {
		return params
	}

	log.Printf("[DEBUG] 开始分析Iris Handler请求参数: %s\n", handlerDecl.Name.Name)

	// 遍历函数体，查找参数绑定调用
	ast.Inspect(handlerDecl.Body, func(node ast.Node) bool {
		if callExpr, ok := node.(*ast.CallExpr); ok {
			// 分析不同类型的参数获取
			if param := analyzer.analyzeParamCall(callExpr); param != nil {
				params = append(params, *param)
			}
		}
		return true
	})

	log.Printf("[DEBUG] Handler %s 发现 %d 个请求参数\n", handlerDecl.Name.Name, len(params))
	return params
}

// analyzeParamCall 分析参数调用
func (analyzer *IrisRequestParamAnalyzer) analyzeParamCall(callExpr *ast.CallExpr) *IrisRequestParamInfo {
	if !analyzer.isIrisContextCall(callExpr) {
		return nil
	}

	methodName := analyzer.getMethodName(callExpr)
	switch methodName {
	case "ReadJSON":
		// ctx.ReadJSON(&struct{}) - Body参数
		return analyzer.analyzeReadJSONCall(callExpr)
	case "Params":
		// ctx.Params() - Query参数
		return analyzer.analyzeParamsCall(callExpr)
	case "URLParam", "URLParamDefault":
		// ctx.URLParam("key") - 单个Query参数
		return analyzer.analyzeURLParamCall(callExpr)
	case "PostValue", "PostValueDefault":
		// ctx.PostValue("key") - Form参数
		return analyzer.analyzePostValueCall(callExpr)
	case "GetHeader", "GetHeaders":
		// ctx.GetHeader("key") - Header参数
		return analyzer.analyzeHeaderCall(callExpr)
	}

	return nil
}

// analyzeReadJSONCall 分析ReadJSON调用
func (analyzer *IrisRequestParamAnalyzer) analyzeReadJSONCall(callExpr *ast.CallExpr) *IrisRequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	schema := analyzer.extractStructSchemaFromArg(callExpr.Args[0])
	if schema == nil {
		return nil
	}

	return &IrisRequestParamInfo{
		ParamType:   "body",
		ParamName:   "request_body",
		ParamSchema: schema,
		IsRequired:  true,
		Source:      "ctx.ReadJSON",
	}
}

// analyzeParamsCall 分析Params调用
func (analyzer *IrisRequestParamAnalyzer) analyzeParamsCall(callExpr *ast.CallExpr) *IrisRequestParamInfo {
	// ctx.Params() 返回所有query参数
	return &IrisRequestParamInfo{
		ParamType: "query",
		ParamName: "query_params",
		ParamSchema: &APISchema{
			Type:        "object",
			Description: "All query parameters from ctx.Params()",
		},
		IsRequired: false,
		Source:     "ctx.Params",
	}
}

// analyzeURLParamCall 分析URLParam调用
func (analyzer *IrisRequestParamAnalyzer) analyzeURLParamCall(callExpr *ast.CallExpr) *IrisRequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	paramName := analyzer.extractStringFromExpr(callExpr.Args[0])
	if paramName == "" {
		return nil
	}

	return &IrisRequestParamInfo{
		ParamType: "query",
		ParamName: paramName,
		ParamSchema: &APISchema{
			Type:        "string",
			Description: "Query parameter from ctx.URLParam()",
		},
		IsRequired: false,
		Source:     "ctx.URLParam",
	}
}

// analyzePostValueCall 分析PostValue调用
func (analyzer *IrisRequestParamAnalyzer) analyzePostValueCall(callExpr *ast.CallExpr) *IrisRequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	paramName := analyzer.extractStringFromExpr(callExpr.Args[0])
	if paramName == "" {
		return nil
	}

	return &IrisRequestParamInfo{
		ParamType: "form",
		ParamName: paramName,
		ParamSchema: &APISchema{
			Type:        "string",
			Description: "Form parameter from ctx.PostValue()",
		},
		IsRequired: false,
		Source:     "ctx.PostValue",
	}
}

// analyzeHeaderCall 分析Header调用
func (analyzer *IrisRequestParamAnalyzer) analyzeHeaderCall(callExpr *ast.CallExpr) *IrisRequestParamInfo {
	if len(callExpr.Args) < 1 {
		return nil
	}

	headerName := analyzer.extractStringFromExpr(callExpr.Args[0])
	if headerName == "" {
		return nil
	}

	return &IrisRequestParamInfo{
		ParamType: "header",
		ParamName: headerName,
		ParamSchema: &APISchema{
			Type:        "string",
			Description: "Header value from ctx.GetHeader()",
		},
		IsRequired: false,
		Source:     "ctx.GetHeader",
	}
}

// isIrisContextCall 检查是否为iris.Context的方法调用
func (analyzer *IrisRequestParamAnalyzer) isIrisContextCall(callExpr *ast.CallExpr) bool {
	if selector, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selector.X.(*ast.Ident); ok {
			if obj := analyzer.typeInfo.ObjectOf(ident); obj != nil {
				typeStr := obj.Type().String()
				return strings.Contains(typeStr, "iris.Context") ||
					strings.Contains(typeStr, "github.com/kataras/iris.Context") ||
					strings.Contains(typeStr, "github.com/kataras/iris/context.Context")
			}
		}
	}
	return false
}

// getMethodName 获取方法名
func (analyzer *IrisRequestParamAnalyzer) getMethodName(callExpr *ast.CallExpr) string {
	if selector, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		return selector.Sel.Name
	}
	return ""
}

// extractStringFromExpr 从表达式中提取字符串
func (analyzer *IrisRequestParamAnalyzer) extractStringFromExpr(expr ast.Expr) string {
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		return strings.Trim(lit.Value, `"`)
	}
	return ""
}

// extractStructSchemaFromArg 从参数中提取结构体Schema
func (analyzer *IrisRequestParamAnalyzer) extractStructSchemaFromArg(arg ast.Expr) *APISchema {
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

	// 使用引擎解析类型
	return analyzer.engine.resolveType(argType, analyzer.engine.maxDepth)
}

// ====================== 辅助方法（从原代码复制并适配） ======================

func (engine *IrisResponseParsingEngine) getReturnStructType(funcDecl *ast.FuncDecl, pkg *packages.Package) *types.Named {
	if funcDecl.Type.Results == nil || len(funcDecl.Type.Results.List) == 0 {
		return nil
	}

	returnExpr := funcDecl.Type.Results.List[0].Type
	returnType := pkg.TypesInfo.TypeOf(returnExpr)
	return engine.resolveNamedStruct(returnType)
}

func (engine *IrisResponseParsingEngine) findDataParameter(funcDecl *ast.FuncDecl, contextIdx int) int {
	paramIdx := 0
	for _, paramList := range funcDecl.Type.Params.List {
		for range paramList.Names {
			if paramIdx != contextIdx && contextIdx != -1 {
				return paramIdx
			}
			paramIdx++
		}
	}
	// 如果没有Context参数，返回第一个参数
	if contextIdx == -1 && paramIdx > 0 {
		return 0
	}
	return -1
}

func (engine *IrisResponseParsingEngine) analyzeParameterFieldMapping(funcDecl *ast.FuncDecl, pkg *packages.Package) map[string]int {
	fieldMapping := make(map[string]int)

	if funcDecl.Body == nil {
		return fieldMapping
	}

	ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
		if retStmt, ok := node.(*ast.ReturnStmt); ok && len(retStmt.Results) > 0 {
			if compLit, ok := retStmt.Results[0].(*ast.CompositeLit); ok {
				engine.analyzeStructLiteralMapping(compLit, funcDecl, fieldMapping, pkg)
			}
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

func (engine *IrisResponseParsingEngine) resolveNamedStruct(typ types.Type) *types.Named {
	if ptr, ok := typ.(*types.Pointer); ok {
		typ = ptr.Elem()
	}

	if named, ok := typ.(*types.Named); ok {
		if _, ok := named.Underlying().(*types.Struct); ok {
			return named
		}
	}

	return nil
}

func (engine *IrisResponseParsingEngine) buildStructTagMap(pkg *packages.Package) {
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			if genDecl, ok := node.(*ast.GenDecl); ok && genDecl.Tok == token.TYPE {
				for _, spec := range genDecl.Specs {
					if typeSpec, ok := spec.(*ast.TypeSpec); ok {
						if structType, ok := typeSpec.Type.(*ast.StructType); ok {
							if obj := pkg.TypesInfo.ObjectOf(typeSpec.Name); obj != nil {
								if named, ok := obj.Type().(*types.Named); ok && named != nil {
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

func (engine *IrisResponseParsingEngine) extractStructTags(named *types.Named, structType *ast.StructType) {
	tagMap := make(map[string]string)

	for _, field := range structType.Fields.List {
		if len(field.Names) > 0 && field.Tag != nil {
			fieldName := field.Names[0].Name
			tag := strings.Trim(field.Tag.Value, "`")

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

func (engine *IrisResponseParsingEngine) analyzeStructLiteralMapping(
	compLit *ast.CompositeLit,
	funcDecl *ast.FuncDecl,
	fieldMapping map[string]int,
	pkg *packages.Package) {

	for _, elt := range compLit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			var fieldName string
			if ident, ok := kv.Key.(*ast.Ident); ok {
				fieldName = ident.Name
			}

			if ident, ok := kv.Value.(*ast.Ident); ok {
				if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
					if paramIdx := engine.getParameterIndex(obj, funcDecl); paramIdx != -1 {
						fieldMapping[fieldName] = paramIdx
						log.Printf("[DEBUG] 发现参数映射: %s.%s <- 参数[%d]\n",
							funcDecl.Name.Name, fieldName, paramIdx)
					}
				}
			}
		}
	}
}

func (engine *IrisResponseParsingEngine) getParameterIndex(obj types.Object, funcDecl *ast.FuncDecl) int {
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

func (engine *IrisResponseParsingEngine) getFunctionObject(callExpr *ast.CallExpr, pkg *packages.Package) *types.Func {
	switch fun := callExpr.Fun.(type) {
	case *ast.Ident:
		if obj := pkg.TypesInfo.ObjectOf(fun); obj != nil {
			if funcObj, ok := obj.(*types.Func); ok {
				return funcObj
			}
		}
	case *ast.SelectorExpr:
		if obj := pkg.TypesInfo.ObjectOf(fun.Sel); obj != nil {
			if funcObj, ok := obj.(*types.Func); ok {
				return funcObj
			}
		}
	}
	return nil
}

func (engine *IrisResponseParsingEngine) findFunctionDeclaration(funcObj *types.Func, targetPkg *packages.Package) *ast.FuncDecl {
	// 首先在目标包中查找
	for _, file := range targetPkg.Syntax {
		for _, decl := range file.Decls {
			if funcDecl, ok := decl.(*ast.FuncDecl); ok {
				if obj := targetPkg.TypesInfo.ObjectOf(funcDecl.Name); obj == funcObj {
					if funcDecl.Body != nil {
						return funcDecl
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
						if funcDecl.Body != nil {
							return funcDecl
						}
					}
				}
			}
		}
	}

	return nil
}

func (engine *IrisResponseParsingEngine) resolveFunctionByTypeInfo(callExpr *ast.CallExpr, pkg *packages.Package) *APISchema {
	returnType := pkg.TypesInfo.TypeOf(callExpr)
	if returnType != nil {
		return engine.resolveType(returnType, engine.maxDepth)
	}
	return &APISchema{Type: "unknown", Description: "unable to resolve function"}
}

func (engine *IrisResponseParsingEngine) analyzeFunctionReturn(funcDecl *ast.FuncDecl, callArgs []ast.Expr, pkg *packages.Package) *APISchema {
	log.Printf("[DEBUG] 分析函数 %s 的返回语句\n", funcDecl.Name.Name)

	if funcDecl.Body == nil {
		return &APISchema{Type: "unknown", Description: "no function body"}
	}

	// 查找return语句
	var returnExpr ast.Expr
	ast.Inspect(funcDecl.Body, func(node ast.Node) bool {
		if retStmt, ok := node.(*ast.ReturnStmt); ok && len(retStmt.Results) > 0 {
			returnExpr = retStmt.Results[0]
			return false
		}
		return true
	})

	if returnExpr == nil {
		return &APISchema{Type: "unknown", Description: "no return statement"}
	}

	// 递归解析返回表达式
	return engine.analyzeResponseExpression(returnExpr, pkg)
}

func (engine *IrisResponseParsingEngine) resolveCompositeLiteral(compLit *ast.CompositeLit, pkg *packages.Package) *APISchema {
	structType := pkg.TypesInfo.TypeOf(compLit)
	if structType != nil {
		return engine.resolveType(structType, engine.maxDepth)
	}
	return &APISchema{Type: "object", Description: "composite literal"}
}

func (engine *IrisResponseParsingEngine) resolveIdentifier(ident *ast.Ident, pkg *packages.Package) *APISchema {
	if obj := pkg.TypesInfo.ObjectOf(ident); obj != nil {
		return engine.resolveType(obj.Type(), engine.maxDepth)
	}
	return &APISchema{Type: "unknown", Description: "unresolved identifier"}
}

func (engine *IrisResponseParsingEngine) resolveSelectorExpr(selExpr *ast.SelectorExpr, pkg *packages.Package) *APISchema {
	log.Printf("[DEBUG] 解析选择器表达式: %s.%s\n", engine.getExpressionString(selExpr.X), selExpr.Sel.Name)

	// 首先尝试使用类型信息直接解析
	if exprType := pkg.TypesInfo.TypeOf(selExpr); exprType != nil {
		log.Printf("[DEBUG] 选择器类型信息: %s\n", exprType.String())
		return engine.resolveType(exprType, engine.maxDepth)
	}

	// 如果类型信息不可用，尝试递归解析
	return engine.resolveSelectorExprRecursive(selExpr, pkg)
}

// resolveSelectorExprRecursive 递归解析选择器表达式
func (engine *IrisResponseParsingEngine) resolveSelectorExprRecursive(selExpr *ast.SelectorExpr, pkg *packages.Package) *APISchema {
	log.Printf("[DEBUG] 递归解析选择器: %s.%s\n", engine.getExpressionString(selExpr.X), selExpr.Sel.Name)

	// 先解析基础表达式
	var baseSchema *APISchema

	switch x := selExpr.X.(type) {
	case *ast.Ident:
		// 变量标识符，例如 mrlResult
		log.Printf("[DEBUG] 解析变量标识符: %s\n", x.Name)
		baseSchema = engine.resolveIdentifier(x, pkg)
	case *ast.SelectorExpr:
		// 嵌套选择器，例如 a.b.c 中的 a.b
		log.Printf("[DEBUG] 解析嵌套选择器\n")
		baseSchema = engine.resolveSelectorExprRecursive(x, pkg)
	case *ast.CallExpr:
		// 函数调用结果的字段访问
		log.Printf("[DEBUG] 解析函数调用结果的字段访问\n")
		baseSchema = engine.resolveFunctionCall(x, pkg)
	default:
		log.Printf("[DEBUG] 基础表达式类型: %T\n", x)
		baseSchema = engine.analyzeResponseExpression(selExpr.X, pkg)
	}

	if baseSchema == nil {
		log.Printf("[DEBUG] 无法解析基础表达式\n")
		return &APISchema{Type: "unknown", Description: "cannot resolve base expression"}
	}

	// 从基础模式中查找字段
	return engine.extractFieldFromSchema(baseSchema, selExpr.Sel.Name)
}

// extractFieldFromSchema 从模式中提取指定字段
func (engine *IrisResponseParsingEngine) extractFieldFromSchema(schema *APISchema, fieldName string) *APISchema {
	log.Printf("[DEBUG] 从模式中提取字段: %s，模式类型: %s\n", fieldName, schema.Type)

	if schema.Properties == nil {
		log.Printf("[DEBUG] 模式没有属性\n")
		return &APISchema{Type: "unknown", Description: fmt.Sprintf("field %s not found in schema", fieldName)}
	}

	// 尝试精确匹配字段名
	if field, exists := schema.Properties[fieldName]; exists {
		log.Printf("[DEBUG] 找到精确匹配字段: %s\n", fieldName)
		return field
	}

	// 尝试不区分大小写匹配
	lowerFieldName := strings.ToLower(fieldName)
	for key, field := range schema.Properties {
		if strings.ToLower(key) == lowerFieldName {
			log.Printf("[DEBUG] 找到大小写不敏感匹配字段: %s -> %s\n", fieldName, key)
			return field
		}
	}

	// 尝试通过JSON标签匹配
	for _, field := range schema.Properties {
		if field.JSONTag != "" {
			jsonName := strings.Split(field.JSONTag, ",")[0] // 取JSON标签的第一部分
			if jsonName == fieldName || strings.ToLower(jsonName) == lowerFieldName {
				log.Printf("[DEBUG] 通过JSON标签找到匹配字段: %s -> %s\n", fieldName, jsonName)
				return field
			}
		}
	}

	log.Printf("[DEBUG] 字段未找到: %s\n", fieldName)
	return &APISchema{Type: "unknown", Description: fmt.Sprintf("field %s not found", fieldName)}
}

// getExpressionString 获取表达式的字符串表示（用于调试）
func (engine *IrisResponseParsingEngine) getExpressionString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return engine.getExpressionString(e.X) + "." + e.Sel.Name
	case *ast.CallExpr:
		if ident, ok := e.Fun.(*ast.Ident); ok {
			return ident.Name + "()"
		} else if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			return engine.getExpressionString(sel.X) + "." + sel.Sel.Name + "()"
		}
		return "call()"
	default:
		return fmt.Sprintf("<%T>", expr)
	}
}

func (engine *IrisResponseParsingEngine) resolveFallbackType(expr ast.Expr, pkg *packages.Package) *APISchema {
	if exprType := pkg.TypesInfo.TypeOf(expr); exprType != nil {
		return engine.resolveType(exprType, engine.maxDepth)
	}
	return &APISchema{Type: "unknown", Description: "fallback resolution failed"}
}

// resolveType 递归解析类型
func (engine *IrisResponseParsingEngine) resolveType(typ types.Type, depth int) *APISchema {
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

	// 处理命名类型
	if named, ok := typ.(*types.Named); ok {
		return engine.resolveNamedType(named, depth)
	}

	// 处理结构体类型
	if structType, ok := typ.(*types.Struct); ok {
		return engine.resolveStructType(structType, depth, nil)
	}

	return &APISchema{Type: typ.String(), Description: "unhandled type"}
}

func (engine *IrisResponseParsingEngine) resolveNamedType(named *types.Named, depth int) *APISchema {
	obj := named.Obj()
	if obj == nil {
		return &APISchema{Type: named.String()}
	}

	underlying := named.Underlying()
	if structType, ok := underlying.(*types.Struct); ok {
		schema := engine.resolveStructType(structType, depth-1, named)
		schema.Type = obj.Name()
		return schema
	}

	underlyingSchema := engine.resolveType(underlying, depth-1)
	return &APISchema{
		Type:        obj.Name(),
		Description: fmt.Sprintf("alias for %s", underlyingSchema.Type),
		Properties:  underlyingSchema.Properties,
		Items:       underlyingSchema.Items,
	}
}

func (engine *IrisResponseParsingEngine) resolveStructType(structType *types.Struct, depth int, named *types.Named) *APISchema {
	properties := make(map[string]*APISchema)

	for i := 0; i < structType.NumFields(); i++ {
		field := structType.Field(i)
		tag := structType.Tag(i)

		fieldSchema := engine.resolveType(field.Type(), depth)

		jsonTag := engine.extractJSONTag(tag)
		if jsonTag == "-" {
			continue
		}

		if jsonTag == "" {
			jsonTag = field.Name()
		}

		fieldSchema.JSONTag = jsonTag

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

func (engine *IrisResponseParsingEngine) extractJSONTag(tag string) string {
	if tag == "" {
		return ""
	}

	structTag := reflect.StructTag(tag)
	jsonTag := structTag.Get("json")

	if jsonTag == "" {
		return ""
	}

	if idx := strings.Index(jsonTag, ","); idx != -1 {
		jsonTag = jsonTag[:idx]
	}

	return jsonTag
}

func (engine *IrisResponseParsingEngine) mapBasicType(kind types.BasicKind) string {
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

// ====================== Iris Handler分析器主类 ======================

// IrisHandlerAnalyzer Iris Handler分析器
type IrisHandlerAnalyzer struct {
	pkgs                  []*packages.Package
	responseParsingEngine *IrisResponseParsingEngine
}

// NewIrisHandlerAnalyzer 创建新的Iris分析器
func NewIrisHandlerAnalyzer(dir string) (*IrisHandlerAnalyzer, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedDeps,
		Tests: false,
		Dir:   dir,
		Env:   append(os.Environ(), "GOFLAGS=-mod=vendor"),
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("加载包失败: %w", err)
	}

	// 创建响应解析引擎
	engine := NewIrisResponseParsingEngine(pkgs)

	return &IrisHandlerAnalyzer{
		pkgs:                  pkgs,
		responseParsingEngine: engine,
	}, nil
}

// Analyze 分析所有Iris Handler
func (a *IrisHandlerAnalyzer) Analyze() map[string]*IrisHandlerAnalysisResult {
	results := make(map[string]*IrisHandlerAnalysisResult)

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

				// 跳过没有函数体的Handler
				if funcDecl.Body == nil {
					return true
				}

				if a.isIrisHandler(funcDecl, pkg.TypesInfo) {
					// 使用完整Handler分析方法
					result := a.responseParsingEngine.AnalyzeIrisHandlerComplete(funcDecl, pkg)

					// 输出分析结果
					if jsonData, err := json.MarshalIndent(result, "", "  "); err == nil {
						log.Printf("📋 Iris Handler分析结果:\n%s\n\n", string(jsonData))
					}

					results[result.PackagePath+"."+result.HandlerName] = result
				}
				return true
			})
		}
	}

	return results
}

// isIrisHandler 检查是否是Iris Handler
func (a *IrisHandlerAnalyzer) isIrisHandler(funcDecl *ast.FuncDecl, info *types.Info) bool {
	if len(funcDecl.Type.Params.List) != 1 {
		return false
	}

	param := funcDecl.Type.Params.List[0]
	if paramType := info.TypeOf(param.Type); paramType != nil {
		typeStr := paramType.String()
		return strings.Contains(typeStr, "iris.Context") ||
			strings.Contains(typeStr, "github.com/kataras/iris.Context") ||
			strings.Contains(typeStr, "github.com/kataras/iris/context.Context")
	}
	return false
}
