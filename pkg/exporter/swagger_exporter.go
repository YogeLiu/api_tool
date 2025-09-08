package exporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YogeLiu/api-tool/pkg/models"
)

// SwaggerInfo Swagger文档信息
type SwaggerInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Contact     *struct {
		Name  string `json:"name,omitempty"`
		Email string `json:"email,omitempty"`
		URL   string `json:"url,omitempty"`
	} `json:"contact,omitempty"`
}

// SwaggerServer 服务器信息
type SwaggerServer struct {
	URL         string `json:"url"`
	Description string `json:"description"`
}

// SwaggerTag 标签信息
type SwaggerTag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SwaggerParameter 参数信息
type SwaggerParameter struct {
	Name        string                 `json:"name"`
	In          string                 `json:"in"` // query, header, path, cookie
	Description string                 `json:"description,omitempty"`
	Required    bool                   `json:"required,omitempty"`
	Schema      map[string]interface{} `json:"schema,omitempty"`
}

// SwaggerRequestBody 请求体
type SwaggerRequestBody struct {
	Description string                      `json:"description,omitempty"`
	Content     map[string]SwaggerMediaType `json:"content"`
	Required    bool                        `json:"required,omitempty"`
}

// SwaggerMediaType 媒体类型
type SwaggerMediaType struct {
	Schema map[string]interface{} `json:"schema"`
}

// SwaggerResponse 响应信息
type SwaggerResponse struct {
	Description string                      `json:"description"`
	Content     map[string]SwaggerMediaType `json:"content,omitempty"`
}

// SwaggerOperation 操作信息
type SwaggerOperation struct {
	Tags        []string                   `json:"tags,omitempty"`
	Summary     string                     `json:"summary,omitempty"`
	Description string                     `json:"description,omitempty"`
	OperationID string                     `json:"operationId,omitempty"`
	Parameters  []SwaggerParameter         `json:"parameters,omitempty"`
	RequestBody *SwaggerRequestBody        `json:"requestBody,omitempty"`
	Responses   map[string]SwaggerResponse `json:"responses"`
}

// SwaggerPath 路径信息
type SwaggerPath struct {
	Get    *SwaggerOperation `json:"get,omitempty"`
	Post   *SwaggerOperation `json:"post,omitempty"`
	Put    *SwaggerOperation `json:"put,omitempty"`
	Delete *SwaggerOperation `json:"delete,omitempty"`
	Patch  *SwaggerOperation `json:"patch,omitempty"`
}

// SwaggerDoc Swagger文档结构
type SwaggerDoc struct {
	OpenAPI    string                 `json:"openapi"`
	Info       SwaggerInfo            `json:"info"`
	Servers    []SwaggerServer        `json:"servers,omitempty"`
	Tags       []SwaggerTag           `json:"tags,omitempty"`
	Paths      map[string]SwaggerPath `json:"paths"`
	Components map[string]interface{} `json:"components,omitempty"`
}

// SwaggerExporter Swagger格式导出器
type SwaggerExporter struct {
	projectName  string
	version      string
	baseURL      string
	outputDir    string
	successOnly  bool
	schemas      map[string]interface{} // 收集的schema定义
	commonPrefix []string               // 所有路由的共同前缀
}

// NewSwaggerExporter 创建Swagger导出器
func NewSwaggerExporter(projectName, version, baseURL, outputDir string, successOnly bool) *SwaggerExporter {
	if version == "" {
		version = "1.0.0"
	}
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	return &SwaggerExporter{
		projectName:  projectName,
		version:      version,
		baseURL:      baseURL,
		outputDir:    outputDir,
		successOnly:  successOnly,
		schemas:      make(map[string]interface{}),
		commonPrefix: nil,
	}
}

// Export 导出API信息为Swagger格式
func (e *SwaggerExporter) Export(apiInfo *models.APIInfo) error {
	// 创建Swagger文档结构
	swaggerDoc := e.convertToSwaggerDoc(apiInfo)

	// 确保输出目录存在
	if err := e.ensureOutputDir(); err != nil {
		return fmt.Errorf("创建输出目录失败: %v", err)
	}

	// 生成JSON文件
	jsonData, err := json.MarshalIndent(swaggerDoc, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %v", err)
	}

	// 保存到文件
	filename := fmt.Sprintf("%s_swagger_%d.json",
		e.sanitizeFilename(e.projectName),
		time.Now().Unix())

	filepath := filepath.Join(e.outputDir, filename)

	if err := os.WriteFile(filepath, jsonData, 0644); err != nil {
		return fmt.Errorf("保存文件失败: %v", err)
	}

	fmt.Printf("✅ Swagger格式导出成功: %s\n", filepath)
	fmt.Printf("📊 导出统计: %d个接口, %d个标签\n",
		len(swaggerDoc.Paths), len(swaggerDoc.Tags))

	if e.successOnly {
		fmt.Println("📝 注意: 仅包含成功响应，已过滤错误响应")
	}

	return nil
}

// convertToSwaggerDoc 转换API信息为Swagger文档格式
func (e *SwaggerExporter) convertToSwaggerDoc(apiInfo *models.APIInfo) *SwaggerDoc {
	// 创建文档信息
	info := SwaggerInfo{
		Title:   e.projectName,
		Version: e.version,
	}

	if e.successOnly {
		info.Description = "通过 api-tool 自动生成的API文档 (仅成功响应，已过滤错误响应)\n生成时间: " + time.Now().Format("2006-01-02 15:04:05")
	} else {
		info.Description = "通过 api-tool 自动生成的API文档\n生成时间: " + time.Now().Format("2006-01-02 15:04:05")
	}

	// 创建服务器信息
	servers := []SwaggerServer{
		{
			URL:         e.baseURL,
			Description: "开发服务器",
		},
	}

	// 收集标签
	tags := e.createTags(apiInfo.Routes)

	// 转换路径
	paths := e.convertPaths(apiInfo.Routes)

	// 添加默认的错误schema
	e.schemas["Error"] = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"code": map[string]interface{}{
				"type": "integer",
			},
			"message": map[string]interface{}{
				"type": "string",
			},
			"request_id": map[string]interface{}{
				"type": "string",
			},
		},
	}

	return &SwaggerDoc{
		OpenAPI: "3.0.3",
		Info:    info,
		Servers: servers,
		Tags:    tags,
		Paths:   paths,
		Components: map[string]interface{}{
			"schemas": e.schemas,
		},
	}
}

// createTags 创建标签
func (e *SwaggerExporter) createTags(routes []models.RouteInfo) []SwaggerTag {
	// 首先计算所有路由的共同前缀
	e.commonPrefix = e.findCommonPrefix(routes)

	tagMap := make(map[string][]string) // tagName -> 对应的路径列表
	var tags []SwaggerTag

	// 基于路径进行智能分组
	for _, route := range routes {
		tagName := e.extractTagFromPath(route.Path)
		if _, exists := tagMap[tagName]; !exists {
			tagMap[tagName] = []string{}
		}
		// 收集该标签下的路径示例
		if len(tagMap[tagName]) < 3 { // 最多记录3个路径作为示例
			tagMap[tagName] = append(tagMap[tagName], route.Path)
		}
	}

	// 创建标签
	for tagName, paths := range tagMap {
		description := e.generateTagDescription(tagName, paths)
		tags = append(tags, SwaggerTag{
			Name:        tagName,
			Description: description,
		})
	}

	return tags
}

// findCommonPrefix 找出所有路由的共同前缀
func (e *SwaggerExporter) findCommonPrefix(routes []models.RouteInfo) []string {
	if len(routes) == 0 {
		return nil
	}

	// 获取第一个路由的路径作为基准
	firstPath := strings.TrimPrefix(routes[0].Path, "/")
	if firstPath == "" {
		return nil
	}

	commonParts := strings.Split(firstPath, "/")

	// 与其他路由比较，找出共同前缀
	for _, route := range routes[1:] {
		path := strings.TrimPrefix(route.Path, "/")
		if path == "" {
			return nil
		}

		parts := strings.Split(path, "/")

		// 比较并更新共同前缀
		minLen := len(commonParts)
		if len(parts) < minLen {
			minLen = len(parts)
		}

		newCommonParts := make([]string, 0)
		for i := 0; i < minLen; i++ {
			if commonParts[i] == parts[i] {
				newCommonParts = append(newCommonParts, commonParts[i])
			} else {
				break
			}
		}
		commonParts = newCommonParts

		// 如果没有共同前缀，直接返回
		if len(commonParts) == 0 {
			return nil
		}
	}

	return commonParts
}

// isVersionNumber 判断是否为版本号
func (e *SwaggerExporter) isVersionNumber(part string) bool {
	// 检查是否匹配 v1, v2, v3 等格式
	if len(part) >= 2 && strings.ToLower(part[0:1]) == "v" {
		// 检查后面是否为数字
		for _, char := range part[1:] {
			if char < '0' || char > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// toCamelCase 将字符串转换为驼峰格式（首字母大写）
func (e *SwaggerExporter) toCamelCase(s string) string {
	if s == "" {
		return "Default"
	}

	// 处理下划线分隔的情况，如 health_group -> HealthGroup
	parts := strings.Split(s, "_")
	var result strings.Builder

	for _, part := range parts {
		if part == "" {
			continue
		}
		// 首字母大写，其余小写
		result.WriteString(strings.ToUpper(part[:1]) + strings.ToLower(part[1:]))
	}

	if result.Len() == 0 {
		return "Default"
	}

	return result.String()
}

// extractTagFromPath 从路径中提取标签名称
func (e *SwaggerExporter) extractTagFromPath(path string) string {
	// 去除开头的斜杠
	path = strings.TrimPrefix(path, "/")

	// 按斜杠分割路径
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return "Default"
	}

	var categoryIndex int

	// 如果有共同前缀，跳过共同前缀部分
	if len(e.commonPrefix) > 0 {
		// 检查当前路径是否包含共同前缀
		hasCommonPrefix := len(parts) >= len(e.commonPrefix)
		for i := 0; i < len(e.commonPrefix) && hasCommonPrefix; i++ {
			if parts[i] != e.commonPrefix[i] {
				hasCommonPrefix = false
				break
			}
		}

		if hasCommonPrefix {
			// 跳过共同前缀，如果共同前缀的下一个是版本号，再跳过一个
			categoryIndex = len(e.commonPrefix)
			if categoryIndex < len(parts) && e.isVersionNumber(parts[categoryIndex]) {
				categoryIndex++
			}
		}
	}

	// 如果没有共同前缀，从第一个开始
	if categoryIndex == 0 {
		// 如果第一个是版本号，使用第二个
		if len(parts) > 1 && e.isVersionNumber(parts[0]) {
			categoryIndex = 1
		}
	}

	// 确保索引有效
	if categoryIndex >= len(parts) {
		return "Default"
	}

	// 获取分类名称并转换为驼峰格式
	categoryName := parts[categoryIndex]
	return e.toCamelCase(categoryName)
}

// generateTagDescription 生成标签描述
func (e *SwaggerExporter) generateTagDescription(tagName string, paths []string) string {
	switch tagName {
	case "Member":
		return "会员相关接口 - 包括会员信息、会员类型、会员验证等功能"
	case "Order":
		return "订单相关接口 - 包括订单创建、查询、状态管理等功能"
	case "Payment":
		return "支付相关接口 - 包括支付状态、支付方式、支付结果等功能"
	case "Free":
		return "免费服务接口 - 包括免费会员、协议、费率等功能"
	case "Address":
		return "地址管理接口 - 包括地址创建、修改、查询等功能"
	case "Entrust":
		return "委托管理接口 - 包括委托创建、检查、终止等功能"
	case "Rights":
		return "权益管理接口 - 包括权益检查、申领等功能"
	case "Test":
		return "测试接口 - 用于内部测试和调试"
	default:
		// 自动生成描述
		if len(paths) > 0 {
			return fmt.Sprintf("%s模块接口 - 示例路径: %s", tagName, strings.Join(paths, ", "))
		}
		return fmt.Sprintf("%s模块相关接口", tagName)
	}
}

// convertPaths 转换路径
func (e *SwaggerExporter) convertPaths(routes []models.RouteInfo) map[string]SwaggerPath {
	paths := make(map[string]SwaggerPath)

	for _, route := range routes {
		path := route.Path
		method := strings.ToLower(route.Method)

		// 获取或创建路径
		swaggerPath, exists := paths[path]
		if !exists {
			swaggerPath = SwaggerPath{}
		}

		// 创建操作
		operation := e.convertOperation(route)

		// 添加操作到对应的HTTP方法
		switch method {
		case "get":
			swaggerPath.Get = operation
		case "post":
			swaggerPath.Post = operation
		case "put":
			swaggerPath.Put = operation
		case "delete":
			swaggerPath.Delete = operation
		case "patch":
			swaggerPath.Patch = operation
		}

		paths[path] = swaggerPath
	}

	return paths
}

// convertOperation 转换操作
func (e *SwaggerExporter) convertOperation(route models.RouteInfo) *SwaggerOperation {
	operation := &SwaggerOperation{
		Tags:        []string{e.extractTagFromPath(route.Path)},
		Summary:     fmt.Sprintf("%s %s", strings.ToUpper(route.Method), route.Path),
		Description: fmt.Sprintf("Handler: %s\n包路径: %s", route.Handler, route.PackagePath),
		OperationID: e.generateOperationID(route),
		Responses:   make(map[string]SwaggerResponse),
	}

	// 转换参数
	operation.Parameters = e.convertParameters(route.RequestParams)

	// 转换请求体
	operation.RequestBody = e.convertRequestBody(route.RequestParams)

	// 转换响应
	operation.Responses = e.convertResponses(route.ResponseSchema)

	return operation
}

// generateOperationID 生成操作ID
func (e *SwaggerExporter) generateOperationID(route models.RouteInfo) string {
	return fmt.Sprintf("%s_%s_%s",
		strings.ToLower(route.Method),
		route.PackageName,
		route.Handler)
}

// convertParameters 转换参数
func (e *SwaggerExporter) convertParameters(requestParams []models.RequestParamInfo) []SwaggerParameter {
	var parameters []SwaggerParameter

	for _, param := range requestParams {
		if param.ParamType == "query" || param.ParamType == "path" {
			swaggerParam := SwaggerParameter{
				Name:        param.ParamName,
				In:          param.ParamType,
				Description: fmt.Sprintf("来源: %s", param.Source),
				Required:    param.IsRequired,
				Schema:      e.convertSchemaToSwagger(param.ParamSchema),
			}
			parameters = append(parameters, swaggerParam)
		}
	}

	return parameters
}

// convertRequestBody 转换请求体
func (e *SwaggerExporter) convertRequestBody(requestParams []models.RequestParamInfo) *SwaggerRequestBody {
	for _, param := range requestParams {
		if param.ParamType == "body" {
			// 为请求体生成更好的schema名称
			schemaName := "RequestBody"
			if param.ParamName != "" && param.ParamName != "request_body" {
				schemaName = param.ParamName
			}

			return &SwaggerRequestBody{
				Description: fmt.Sprintf("请求体 (来源: %s)", param.Source),
				Content: map[string]SwaggerMediaType{
					"application/json": {
						Schema: e.convertSchemaToSwaggerWithName(param.ParamSchema, schemaName),
					},
				},
				Required: param.IsRequired,
			}
		}
	}
	return nil
}

// convertResponses 转换响应
func (e *SwaggerExporter) convertResponses(responseSchema *models.APISchema) map[string]SwaggerResponse {
	responses := make(map[string]SwaggerResponse)

	if responseSchema != nil {
		var schema map[string]interface{}

		if e.successOnly {
			// 只显示成功响应的data字段
			schema = e.extractSuccessDataSchema(responseSchema)
		} else {
			// 显示完整响应
			schema = e.convertSchemaToSwagger(responseSchema)
		}

		responses["200"] = SwaggerResponse{
			Description: "成功响应",
			Content: map[string]SwaggerMediaType{
				"application/json": {
					Schema: schema,
				},
			},
		}
	} else {
		// 默认响应
		responses["200"] = SwaggerResponse{
			Description: "成功响应",
			Content: map[string]SwaggerMediaType{
				"application/json": {
					Schema: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"code": map[string]interface{}{
								"type": "integer",
							},
							"message": map[string]interface{}{
								"type": "string",
							},
							"data": map[string]interface{}{},
							"request_id": map[string]interface{}{
								"type": "string",
							},
						},
					},
				},
			},
		}
	}

	// 添加错误响应（如果不是仅成功模式）
	if !e.successOnly {
		responses["400"] = SwaggerResponse{
			Description: "请求错误",
			Content: map[string]SwaggerMediaType{
				"application/json": {
					Schema: map[string]interface{}{
						"$ref": "#/components/schemas/Error",
					},
				},
			},
		}
		responses["500"] = SwaggerResponse{
			Description: "服务器错误",
			Content: map[string]SwaggerMediaType{
				"application/json": {
					Schema: map[string]interface{}{
						"$ref": "#/components/schemas/Error",
					},
				},
			},
		}
	}

	return responses
}

// extractSuccessDataSchema 提取成功响应的data字段
func (e *SwaggerExporter) extractSuccessDataSchema(responseSchema *models.APISchema) map[string]interface{} {
	if responseSchema != nil && responseSchema.Type == "object" && responseSchema.Properties != nil {
		if dataField, exists := responseSchema.Properties["data"]; exists {
			// 创建包含data字段的成功响应
			return map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"code": map[string]interface{}{
						"type":    "integer",
						"example": 0,
					},
					"message": map[string]interface{}{
						"type":    "string",
						"example": "success",
					},
					"data": e.convertSchemaToSwaggerWithName(dataField, "ResponseData"),
					"request_id": map[string]interface{}{
						"type":    "string",
						"example": "uuid",
					},
				},
			}
		}
	}

	// 默认成功响应
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"code": map[string]interface{}{
				"type":    "integer",
				"example": 0,
			},
			"message": map[string]interface{}{
				"type":    "string",
				"example": "success",
			},
			"data": map[string]interface{}{},
			"request_id": map[string]interface{}{
				"type":    "string",
				"example": "uuid",
			},
		},
	}
}

// convertSchemaToSwagger 转换APISchema为Swagger Schema
func (e *SwaggerExporter) convertSchemaToSwagger(apiSchema *models.APISchema) map[string]interface{} {
	return e.convertSchemaToSwaggerWithName(apiSchema, "")
}

// convertSchemaToSwaggerWithName 转换APISchema为Swagger Schema，支持命名
func (e *SwaggerExporter) convertSchemaToSwaggerWithName(apiSchema *models.APISchema, suggestedName string) map[string]interface{} {
	if apiSchema == nil {
		return map[string]interface{}{
			"type": "object",
		}
	}

	// 对于简单类型，直接返回
	switch apiSchema.Type {
	case "string":
		return map[string]interface{}{
			"type":    "string",
			"example": "string",
		}
	case "integer":
		return map[string]interface{}{
			"type":    "integer",
			"example": 0,
		}
	case "number":
		return map[string]interface{}{
			"type":    "number",
			"example": 0.0,
		}
	case "boolean":
		return map[string]interface{}{
			"type":    "boolean",
			"example": false,
		}
	case "any", "unknown":
		return map[string]interface{}{
			"type": "object",
		}
	}

	// 对于有properties的复杂类型，提取为组件（不管type是什么）
	if apiSchema.Properties != nil && len(apiSchema.Properties) > 0 {
		// 生成schema名称
		schemaName := e.generateSchemaName(apiSchema, suggestedName)

		// 检查是否已经定义过
		if _, exists := e.schemas[schemaName]; !exists {
			// 创建schema定义
			schema := map[string]interface{}{
				"type": "object",
			}

			if apiSchema.Description != "" {
				schema["description"] = apiSchema.Description
			}

			properties := make(map[string]interface{})
			for key, prop := range apiSchema.Properties {
				// 使用JSON标签作为键名，如果没有则使用字段名
				jsonKey := key
				if prop.JSONTag != "" && prop.JSONTag != "-" {
					jsonKey = prop.JSONTag
				}
				properties[jsonKey] = e.convertSchemaToSwaggerWithName(prop, key)
			}
			schema["properties"] = properties

			// 添加到schemas集合
			e.schemas[schemaName] = schema
		}

		// 返回引用
		return map[string]interface{}{
			"$ref": "#/components/schemas/" + schemaName,
		}
	}

	if apiSchema.Type == "array" {
		schema := map[string]interface{}{
			"type": "array",
		}

		if apiSchema.Items != nil {
			schema["items"] = e.convertSchemaToSwaggerWithName(apiSchema.Items, suggestedName+"Item")
		}

		return schema
	}

	// 其他情况：对于自定义类型名（如 MemberListDTO），视为object
	standardTypes := []string{"string", "integer", "number", "boolean", "array", "object"}
	isStandardType := false
	for _, t := range standardTypes {
		if apiSchema.Type == t {
			isStandardType = true
			break
		}
	}

	if !isStandardType && apiSchema.Type != "" {
		// 自定义类型名，视为object
		schema := map[string]interface{}{
			"type": "object",
		}
		if apiSchema.Description != "" {
			schema["description"] = apiSchema.Description
		} else {
			schema["description"] = "自定义类型: " + apiSchema.Type
		}
		return schema
	}

	// 标准类型但未匹配到的情况
	schema := map[string]interface{}{
		"type": apiSchema.Type,
	}
	if apiSchema.Type == "" {
		schema["type"] = "object"
	}

	if apiSchema.Description != "" {
		schema["description"] = apiSchema.Description
	}

	return schema
}

// generateSchemaName 生成schema名称
func (e *SwaggerExporter) generateSchemaName(apiSchema *models.APISchema, suggestedName string) string {
	// 尝试从类型名称生成（优先使用自定义类型名）
	standardTypes := []string{"object", "string", "integer", "number", "boolean", "array"}
	isStandardType := false
	for _, t := range standardTypes {
		if apiSchema.Type == t {
			isStandardType = true
			break
		}
	}

	if !isStandardType && apiSchema.Type != "" {
		// 自定义类型名，直接使用
		typeName := e.cleanSchemaName(apiSchema.Type)
		if typeName != "" {
			return typeName
		}
	}

	// 如果有建议的名称，使用它
	if suggestedName != "" {
		// 清理名称，确保符合OpenAPI规范
		schemaName := e.cleanSchemaName(suggestedName)
		if schemaName != "" {
			return schemaName
		}
	}

	// 尝试从标准类型名称生成
	if apiSchema.Type != "" && apiSchema.Type != "object" {
		typeName := e.cleanSchemaName(apiSchema.Type)
		if typeName != "" && typeName != "Object" {
			return typeName
		}
	}

	// 基于属性生成名称
	if apiSchema.Properties != nil && len(apiSchema.Properties) > 0 {
		var keyNames []string
		for key := range apiSchema.Properties {
			if len(keyNames) < 3 { // 只取前3个属性名
				keyNames = append(keyNames, key)
			}
		}
		if len(keyNames) > 0 {
			baseName := strings.Join(keyNames, "")
			return e.cleanSchemaName(baseName) + "Schema"
		}
	}

	// 默认名称
	return "ObjectSchema"
}

// cleanSchemaName 清理schema名称
func (e *SwaggerExporter) cleanSchemaName(name string) string {
	// 移除路径分隔符
	name = strings.ReplaceAll(name, "/", "")
	name = strings.ReplaceAll(name, ".", "")
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, "_", "")

	// 确保首字母大写
	if len(name) > 0 {
		name = strings.ToUpper(name[:1]) + name[1:]
	}

	return name
}

// ensureOutputDir 确保输出目录存在
func (e *SwaggerExporter) ensureOutputDir() error {
	if e.outputDir == "" {
		e.outputDir = "./swagger_exports"
	}

	return os.MkdirAll(e.outputDir, 0755)
}

// sanitizeFilename 清理文件名
func (e *SwaggerExporter) sanitizeFilename(filename string) string {
	// 替换非法字符
	filename = strings.ReplaceAll(filename, "/", "_")
	filename = strings.ReplaceAll(filename, "\\", "_")
	filename = strings.ReplaceAll(filename, ":", "_")
	filename = strings.ReplaceAll(filename, "*", "_")
	filename = strings.ReplaceAll(filename, "?", "_")
	filename = strings.ReplaceAll(filename, "\"", "_")
	filename = strings.ReplaceAll(filename, "<", "_")
	filename = strings.ReplaceAll(filename, ">", "_")
	filename = strings.ReplaceAll(filename, "|", "_")

	return filename
}
