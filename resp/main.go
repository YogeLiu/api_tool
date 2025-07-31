package main

import (
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

const (
	StructLiteralResponse = iota
	StructResponse
	MapLiteralResponse
	FunctionCallResponse
	VariableResponse
	SelectorResponse
	UnknownResponse
)

// API 响应结构定义
type APIResponse struct {
	ResponseType string                 // 响应类型
	DataRealType string                 // 实际数据类型
	Fields       map[string]FieldSchema // 字段结构
}

// 字段结构定义
type FieldSchema struct {
	Type      string                 // 字段类型
	JSONTag   string                 // json tag
	IsPointer bool                   // 是否是指针
	IsArray   bool                   // 是否是切片
	Children  map[string]FieldSchema // 嵌套结构
}

// 封装方法信息
type WrapperMethod struct {
	PackagePath string        // 包路径
	RecvType    string        // 接收器类型
	MethodName  string        // 方法名
	FuncDecl    *ast.FuncDecl // AST 节点
}

// 响应分析器
type ResponseAnalyzer struct {
	pkg          *packages.Package
	fset         *token.FileSet
	wrapperCache map[string]*WrapperMethod // 方法缓存
}

// 创建新的响应分析器
func NewResponseAnalyzer(pkg *packages.Package, fset *token.FileSet) *ResponseAnalyzer {
	return &ResponseAnalyzer{
		pkg:          pkg,
		fset:         fset,
		wrapperCache: make(map[string]*WrapperMethod),
	}
}

// 分析响应表达式（入口函数）
func (ra *ResponseAnalyzer) AnalyzeResponse(expr ast.Expr) *APIResponse {
	// 首先尝试直接解析
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

// 递归深度解析响应表达式
func (ra *ResponseAnalyzer) AnalyzeResponseRecursively(expr ast.Expr) *APIResponse {
	result := ra.AnalyzeResponse(expr)
	if result == nil {
		return nil
	}

	// 如果是函数调用，检查是否是封装方法
	if callExpr, ok := expr.(*ast.CallExpr); ok {
		if wrapper := ra.findWrapperMethod(callExpr); wrapper != nil {
			// 展开封装方法
			return ra.expandWrapperMethod(callExpr, wrapper)
		}
	}

	return result
}

// 查找是否是封装方法
func (ra *ResponseAnalyzer) findWrapperMethod(callExpr *ast.CallExpr) *WrapperMethod {
	// 检查是否是方法调用
	sel, ok := callExpr.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	// 获取方法名
	methodName := sel.Sel.Name

	// 构建缓存键
	cacheKey := fmt.Sprintf("%s.%s", ra.pkg.PkgPath, methodName)
	if wrapper, exists := ra.wrapperCache[cacheKey]; exists {
		return wrapper
	}

	// 在当前包中查找方法
	for _, file := range ra.pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			funcDecl, ok := n.(*ast.FuncDecl)
			if !ok || funcDecl.Name.Name != methodName {
				return true
			}

			// 检查是否是方法（有接收器）
			if funcDecl.Recv == nil || len(funcDecl.Recv.List) == 0 {
				return true
			}

			// 检查接收器类型
			recvType := types.ExprString(funcDecl.Recv.List[0].Type)
			if !strings.Contains(recvType, "Context") && !strings.Contains(recvType, "*gin.Context") {
				return true
			}

			// 检查方法体是否包含 ctx.JSON 调用
			if ra.hasJSONCallInBody(funcDecl) {
				wrapper := &WrapperMethod{
					PackagePath: ra.pkg.PkgPath,
					RecvType:    recvType,
					MethodName:  methodName,
					FuncDecl:    funcDecl,
				}
				ra.wrapperCache[cacheKey] = wrapper
				return false // 找到后停止
			}
			return true
		})
	}

	return nil
}

// 检查方法体是否包含 ctx.JSON 调用
func (ra *ResponseAnalyzer) hasJSONCallInBody(funcDecl *ast.FuncDecl) bool {
	if funcDecl.Body == nil {
		return false
	}

	hasJSONCall := false
	ast.Inspect(funcDecl.Body, func(n ast.Node) bool {
		if hasJSONCall {
			return false // 找到后停止
		}

		callExpr, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		if ra.isJSONCall(callExpr) {
			hasJSONCall = true
			return false
		}
		return true
	})

	return hasJSONCall
}

// 检查是否是 ctx.JSON 调用
func (ra *ResponseAnalyzer) isJSONCall(callExpr *ast.CallExpr) bool {
	selector, ok := callExpr.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	// 检查方法名
	if selector.Sel.Name != "JSON" {
		return false
	}

	// 检查接收器是否是 Context 类型
	if ident, ok := selector.X.(*ast.Ident); ok {
		if obj := ra.pkg.TypesInfo.ObjectOf(ident); obj != nil {
			if named, ok := obj.Type().(*types.Named); ok {
				return named.Obj().Name() == "Context"
			}
		}
	}
	return false
}

// 展开封装方法
func (ra *ResponseAnalyzer) expandWrapperMethod(callExpr *ast.CallExpr, wrapper *WrapperMethod) *APIResponse {
	// 获取封装方法的返回类型
	returnType := ra.pkg.TypesInfo.TypeOf(callExpr)
	if returnType == nil {
		return ra.AnalyzeResponse(callExpr)
	}

	// 解析基础字段
	baseFields := ra.parseTypeFields(returnType)

	// 分析参数
	for i, arg := range callExpr.Args {
		argResponse := ra.AnalyzeResponse(arg)
		if argResponse == nil {
			continue
		}

		// 尝试智能合并：如果存在 Data 字段，则用参数替换
		if dataField, exists := baseFields["Data"]; exists {
			dataField.Type = argResponse.DataRealType
			dataField.Children = argResponse.Fields
			baseFields["Data"] = dataField
		} else {
			// 否则直接合并
			for k, v := range argResponse.Fields {
				prefixedKey := fmt.Sprintf("arg%d_%s", i, k)
				baseFields[prefixedKey] = v
			}
		}
	}

	return &APIResponse{
		ResponseType: "wrapped-" + wrapper.MethodName,
		DataRealType: returnType.String(),
		Fields:       baseFields,
	}
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

// 分析结构体响应
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

// 分析结构体字面量响应
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

// Gin Handler 分析器
type GinHandlerAnalyzer struct {
	pkgs []*packages.Package
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
		Dir:   dir,
		Env:   append(os.Environ(), "GOFLAGS=-mod=vendor"),
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("加载包失败: %w", err)
	}

	return &GinHandlerAnalyzer{pkgs: pkgs}, nil
}

// 分析所有 Gin Handler
func (a *GinHandlerAnalyzer) Analyze() {
	for _, pkg := range a.pkgs {
		if pkg.Types == nil {
			continue
		}

		for _, file := range pkg.Syntax {
			fset := pkg.Fset
			// fileName := pkg.CompiledGoFiles[i]

			// // 跳过测试文件
			// if strings.HasSuffix(fileName, "_test.go") {
			// 	continue
			// }

			// // 跳过非 handlers 目录（可选）
			// if !strings.Contains(fileName, "/handlers/") && !strings.Contains(fileName, "\\handlers\\") {
			// 	continue
			// }

			// fmt.Printf("\n📁 分析文件: %s\n", fileName)

			ast.Inspect(file, func(n ast.Node) bool {
				funcDecl, ok := n.(*ast.FuncDecl)
				if !ok || funcDecl.Recv == nil {
					return true
				}

				if a.isGinHandler(funcDecl, pkg.TypesInfo) {
					fmt.Printf("  🔍 发现 Handler: %s\n", getFuncSignature(funcDecl))

					// 分析响应
					analyzer := NewResponseAnalyzer(pkg, fset)
					if resp := a.analyzeHandlerResponse(funcDecl, analyzer); resp != nil {
						printResponseSchema(resp, 4)
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

// 获取函数签名
func getFuncSignature(funcDecl *ast.FuncDecl) string {
	receiver := ""
	if funcDecl.Recv != nil && len(funcDecl.Recv.List) > 0 {
		receiver = types.ExprString(funcDecl.Recv.List[0].Type) + "."
	}
	return receiver + funcDecl.Name.Name
}

// 分析 Handler 响应
func (a *GinHandlerAnalyzer) analyzeHandlerResponse(funcDecl *ast.FuncDecl, analyzer *ResponseAnalyzer) *APIResponse {
	for _, stmt := range funcDecl.Body.List {
		exprStmt, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}

		callExpr, ok := exprStmt.X.(*ast.CallExpr)
		if !ok {
			continue
		}

		if !a.isJSONCall(callExpr, analyzer.pkg.TypesInfo) {
			continue
		}

		if len(callExpr.Args) < 2 {
			continue
		}

		// 获取第二个参数（响应数据）并递归深度解析
		dataExpr := callExpr.Args[1]
		return analyzer.AnalyzeResponseRecursively(dataExpr)
	}
	return nil
}

// 检查是否是 c.JSON 调用
func (a *GinHandlerAnalyzer) isJSONCall(callExpr *ast.CallExpr, info *types.Info) bool {
	if selector, ok := callExpr.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selector.X.(*ast.Ident); ok {
			if obj := info.ObjectOf(ident); obj != nil {
				if named, ok := obj.Type().(*types.Named); ok {
					return named.Obj().Name() == "Context"
				}
			}
		}
		return selector.Sel.Name == "JSON"
	}
	return false
}

// 打印响应结构
func printResponseSchema(resp *APIResponse, indent int) {
	prefix := strings.Repeat("  ", indent-2)
	// 为不同响应类型添加说明
	typeDescription := resp.ResponseType
	switch resp.ResponseType {
	case "map-literal":
		typeDescription = "map-literal (⚠️ 非标准响应，建议使用包装函数)"
	case "wrapped-success":
		typeDescription = "wrapped-success (✅ 标准成功响应)"
	case "wrapped-error":
		typeDescription = "wrapped-error (❌ 标准错误响应)"
	}
	fmt.Printf("%s📌 响应类型: %s\n", prefix, typeDescription)
	fmt.Printf("%s  实际数据类型: %s\n", prefix, resp.DataRealType)
	if len(resp.Fields) > 0 {
		fmt.Printf("%s  字段结构:\n", prefix)
		printFieldSchema(resp.Fields, indent)
	}
}

// 递归打印字段结构
func printFieldSchema(fields map[string]FieldSchema, indent int) {
	prefix := strings.Repeat("  ", indent)
	for name, schema := range fields {
		typeStr := schema.Type
		if schema.IsPointer {
			typeStr = "*" + typeStr
		}
		if schema.IsArray {
			typeStr = "[]" + typeStr
		}
		fmt.Printf("%s- %s (%s) → json: %q\n", prefix, name, typeStr, schema.JSONTag)
		if len(schema.Children) > 0 {
			printFieldSchema(schema.Children, indent+2)
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
