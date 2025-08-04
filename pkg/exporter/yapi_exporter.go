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

// YAPIInterface YAPI接口定义
type YAPIInterface struct {
	ID          int                    `json:"_id"`
	Title       string                 `json:"title"`
	Path        string                 `json:"path"`
	Method      string                 `json:"method"`
	ProjectID   int                    `json:"project_id"`
	CatID       int                    `json:"catid"`
	Status      string                 `json:"status"`
	ReqQuery    []YAPIQueryParam       `json:"req_query"`
	ReqHeaders  []YAPIHeader           `json:"req_headers"`
	ReqBodyType string                 `json:"req_body_type"`
	ReqBodyForm []YAPIFormParam        `json:"req_body_form"`
	ReqBodyOther string                 `json:"req_body_other"`
	ResBody     string                 `json:"res_body"`
	ResBodyType string                 `json:"res_body_type"`
	Desc        string                 `json:"desc"`
	Markdown    string                 `json:"markdown"`
	AddTime     int64                  `json:"add_time"`
	UpTime      int64                  `json:"up_time"`
	Tag         []string               `json:"tag"`
	APIOpened   bool                   `json:"api_opened"`
	Index       int                    `json:"index"`
	Username    string                 `json:"username"`
	UID         int                    `json:"uid"`
}

// YAPIQueryParam YAPI查询参数
type YAPIQueryParam struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Desc     string `json:"desc"`
	Required string `json:"required"`
}

// YAPIHeader YAPI请求头
type YAPIHeader struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Desc     string `json:"desc"`
	Required string `json:"required"`
}

// YAPIFormParam YAPI表单参数
type YAPIFormParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Desc     string `json:"desc"`
	Required string `json:"required"`
	Value    string `json:"value"`
}

// YAPICategory YAPI分类
type YAPICategory struct {
	ID       int    `json:"_id"`
	Name     string `json:"name"`
	Desc     string `json:"desc"`
	UID      int    `json:"uid"`
	AddTime  int64  `json:"add_time"`
	UpTime   int64  `json:"up_time"`
	Index    int    `json:"index"`
	Username string `json:"username"`
}

// YAPIProject YAPI项目结构
type YAPIProject struct {
	Info       YAPIProjectInfo `json:"info"`
	Interfaces []YAPIInterface `json:"interfaces"`
	Categories []YAPICategory  `json:"categories"`
}

// YAPIProjectInfo YAPI项目信息
type YAPIProjectInfo struct {
	ID          int    `json:"_id"`
	Name        string `json:"name"`
	Desc        string `json:"desc"`
	BasePath    string `json:"basepath"`
	ProjectType string `json:"project_type"`
	UID         int    `json:"uid"`
	GroupID     int    `json:"group_id"`
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	AddTime     int64  `json:"add_time"`
	UpTime      int64  `json:"up_time"`
	Env         []struct {
		Name   string `json:"name"`
		Domain string `json:"domain"`
		Header []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"header"`
	} `json:"env"`
	Tag []string `json:"tag"`
}

// YAPIExporter YAPI格式导出器
type YAPIExporter struct {
	projectName string
	projectID   int
	basePath    string
	outputDir   string
}

// NewYAPIExporter 创建YAPI导出器
func NewYAPIExporter(projectName string, basePath string, outputDir string) *YAPIExporter {
	return &YAPIExporter{
		projectName: projectName,
		projectID:   1, // 默认项目ID
		basePath:    basePath,
		outputDir:   outputDir,
	}
}

// Export 导出API信息为YAPI格式
func (e *YAPIExporter) Export(apiInfo *models.APIInfo) error {
	// 创建YAPI项目结构
	yapiProject := e.convertToYAPIProject(apiInfo)

	// 确保输出目录存在
	if err := e.ensureOutputDir(); err != nil {
		return fmt.Errorf("创建输出目录失败: %v", err)
	}

	// 生成JSON文件
	jsonData, err := json.MarshalIndent(yapiProject, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON序列化失败: %v", err)
	}

	// 保存到文件
	filename := fmt.Sprintf("%s_yapi_export_%d.json", 
		e.sanitizeFilename(e.projectName), 
		time.Now().Unix())
	
	filepath := filepath.Join(e.outputDir, filename)
	
	if err := os.WriteFile(filepath, jsonData, 0644); err != nil {
		return fmt.Errorf("保存文件失败: %v", err)
	}

	fmt.Printf("✅ YAPI格式导出成功: %s\n", filepath)
	fmt.Printf("📊 导出统计: %d个接口, %d个分类\n", 
		len(yapiProject.Interfaces), len(yapiProject.Categories))
	
	return nil
}

// convertToYAPIProject 转换API信息为YAPI项目格式
func (e *YAPIExporter) convertToYAPIProject(apiInfo *models.APIInfo) *YAPIProject {
	now := time.Now().Unix()
	
	// 创建项目信息
	projectInfo := YAPIProjectInfo{
		ID:          e.projectID,
		Name:        e.projectName,
		Desc:        fmt.Sprintf("通过api-tool自动生成的API文档 (生成时间: %s)", time.Now().Format("2006-01-02 15:04:05")),
		BasePath:    e.basePath,
		ProjectType: "private",
		UID:         1,
		GroupID:     1,
		Icon:        "code-o",
		Color:       "cyan",
		AddTime:     now,
		UpTime:      now,
		Tag:         []string{"api-tool", "auto-generated"},
		Env: []struct {
			Name   string `json:"name"`
			Domain string `json:"domain"`
			Header []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"header"`
		}{
			{
				Name:   "local",
				Domain: "http://localhost:8080",
				Header: []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				}{
					{Name: "Content-Type", Value: "application/json"},
				},
			},
		},
	}

	// 根据包路径创建分类
	categories := e.createCategories(apiInfo.Routes)
	
	// 转换接口
	interfaces := e.convertInterfaces(apiInfo.Routes, categories)

	return &YAPIProject{
		Info:       projectInfo,
		Interfaces: interfaces,
		Categories: categories,
	}
}

// createCategories 根据包路径创建分类
func (e *YAPIExporter) createCategories(routes []models.RouteInfo) []YAPICategory {
	categoryMap := make(map[string]bool)
	var categories []YAPICategory
	
	now := time.Now().Unix()
	catID := 1

	// 收集所有包路径
	for _, route := range routes {
		if !categoryMap[route.PackagePath] {
			categoryMap[route.PackagePath] = true
			
			// 从包路径提取友好的分类名
			categoryName := e.extractCategoryName(route.PackagePath)
			
			categories = append(categories, YAPICategory{
				ID:       catID,
				Name:     categoryName,
				Desc:     fmt.Sprintf("包路径: %s", route.PackagePath),
				UID:      1,
				AddTime:  now,
				UpTime:   now,
				Index:    catID - 1,
				Username: "api-tool",
			})
			catID++
		}
	}

	return categories
}

// extractCategoryName 从包路径提取分类名
func (e *YAPIExporter) extractCategoryName(packagePath string) string {
	parts := strings.Split(packagePath, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1] // 使用最后一部分作为分类名
	}
	return "default"
}

// getCategoryID 获取包路径对应的分类ID
func (e *YAPIExporter) getCategoryID(packagePath string, categories []YAPICategory) int {
	targetName := e.extractCategoryName(packagePath)
	for _, cat := range categories {
		if cat.Name == targetName {
			return cat.ID
		}
	}
	return 1 // 默认分类ID
}

// convertInterfaces 转换接口信息
func (e *YAPIExporter) convertInterfaces(routes []models.RouteInfo, categories []YAPICategory) []YAPIInterface {
	var interfaces []YAPIInterface
	now := time.Now().Unix()

	for i, route := range routes {
		yapiInterface := YAPIInterface{
			ID:          i + 1,
			Title:       e.generateInterfaceTitle(route),
			Path:        route.Path,
			Method:      strings.ToUpper(route.Method),
			ProjectID:   e.projectID,
			CatID:       e.getCategoryID(route.PackagePath, categories),
			Status:      "done",
			ReqQuery:    e.convertQueryParams(route.RequestParams),
			ReqHeaders:  e.getDefaultHeaders(),
			ReqBodyType: e.getRequestBodyType(route.RequestParams),
			ReqBodyForm: e.convertFormParams(route.RequestParams),
			ReqBodyOther: e.convertRequestBodyOther(route.RequestParams),
			ResBody:     e.convertResponseBody(route.ResponseSchema),
			ResBodyType: "json",
			Desc:        e.generateDescription(route),
			Markdown:    e.generateMarkdown(route),
			AddTime:     now,
			UpTime:      now,
			Tag:         []string{route.PackageName},
			APIOpened:   false,
			Index:       i,
			Username:    "api-tool",
			UID:         1,
		}

		interfaces = append(interfaces, yapiInterface)
	}

	return interfaces
}

// generateInterfaceTitle 生成接口标题
func (e *YAPIExporter) generateInterfaceTitle(route models.RouteInfo) string {
	return fmt.Sprintf("%s %s", strings.ToUpper(route.Method), route.Path)
}

// convertQueryParams 转换查询参数
func (e *YAPIExporter) convertQueryParams(requestParams []models.RequestParamInfo) []YAPIQueryParam {
	var queryParams []YAPIQueryParam

	for _, param := range requestParams {
		if param.ParamType == "query" {
			required := "0"
			if param.IsRequired {
				required = "1"
			}

			queryParams = append(queryParams, YAPIQueryParam{
				Name:     param.ParamName,
				Value:    "",
				Desc:     e.generateParamDescription(param),
				Required: required,
			})
		}
	}

	return queryParams
}

// getDefaultHeaders 获取默认请求头
func (e *YAPIExporter) getDefaultHeaders() []YAPIHeader {
	return []YAPIHeader{
		{
			Name:     "Content-Type",
			Value:    "application/json",
			Desc:     "请求内容类型",
			Required: "1",
		},
	}
}

// getRequestBodyType 获取请求体类型
func (e *YAPIExporter) getRequestBodyType(requestParams []models.RequestParamInfo) string {
	for _, param := range requestParams {
		if param.ParamType == "body" {
			return "json"
		}
	}
	return "none"
}

// convertFormParams 转换表单参数
func (e *YAPIExporter) convertFormParams(requestParams []models.RequestParamInfo) []YAPIFormParam {
	var formParams []YAPIFormParam

	for _, param := range requestParams {
		if param.ParamType == "form" {
			required := "0"
			if param.IsRequired {
				required = "1"
			}

			formParams = append(formParams, YAPIFormParam{
				Name:     param.ParamName,
				Type:     e.convertSchemaTypeToYAPIType(param.ParamSchema),
				Desc:     e.generateParamDescription(param),
				Required: required,
				Value:    "",
			})
		}
	}

	return formParams
}

// convertRequestBodyOther 转换请求体其他格式
func (e *YAPIExporter) convertRequestBodyOther(requestParams []models.RequestParamInfo) string {
	for _, param := range requestParams {
		if param.ParamType == "body" && param.ParamSchema != nil {
			// 生成JSON Schema
			schema := e.convertAPISchemaToJSONSchema(param.ParamSchema)
			jsonData, _ := json.MarshalIndent(schema, "", "  ")
			return string(jsonData)
		}
	}
	return ""
}

// convertResponseBody 转换响应体
func (e *YAPIExporter) convertResponseBody(responseSchema *models.APISchema) string {
	if responseSchema == nil {
		// 返回默认响应格式
		defaultResponse := map[string]interface{}{
			"code":       0,
			"message":    "success",
			"data":       nil,
			"request_id": "uuid",
		}
		jsonData, _ := json.MarshalIndent(defaultResponse, "", "  ")
		return string(jsonData)
	}

	// 转换响应Schema
	schema := e.convertAPISchemaToJSONSchema(responseSchema)
	jsonData, _ := json.MarshalIndent(schema, "", "  ")
	return string(jsonData)
}

// convertAPISchemaToJSONSchema 转换APISchema为JSON Schema
func (e *YAPIExporter) convertAPISchemaToJSONSchema(apiSchema *models.APISchema) interface{} {
	if apiSchema == nil {
		return nil
	}

	switch apiSchema.Type {
	case "object":
		obj := make(map[string]interface{})
		if apiSchema.Properties != nil {
			for key, prop := range apiSchema.Properties {
				// 使用JSON标签作为键名，如果没有则使用字段名
				jsonKey := key
				if prop.JSONTag != "" && prop.JSONTag != "-" {
					jsonKey = prop.JSONTag
				}
				obj[jsonKey] = e.convertAPISchemaToJSONSchema(prop)
			}
		}
		return obj

	case "array":
		if apiSchema.Items != nil {
			return []interface{}{e.convertAPISchemaToJSONSchema(apiSchema.Items)}
		}
		return []interface{}{}

	case "string":
		return "string"
	case "integer":
		return 0
	case "number":
		return 0.0
	case "boolean":
		return false
	case "any":
		return nil
	default:
		return apiSchema.Type
	}
}

// convertSchemaTypeToYAPIType 转换Schema类型为YAPI类型
func (e *YAPIExporter) convertSchemaTypeToYAPIType(schema *models.APISchema) string {
	if schema == nil {
		return "text"
	}

	switch schema.Type {
	case "string":
		return "text"
	case "integer", "number":
		return "text"
	case "boolean":
		return "text"
	case "array":
		return "text"
	case "object":
		return "text"
	default:
		return "text"
	}
}

// generateParamDescription 生成参数描述
func (e *YAPIExporter) generateParamDescription(param models.RequestParamInfo) string {
	desc := fmt.Sprintf("来源: %s", param.Source)
	if param.ParamSchema != nil && param.ParamSchema.Description != "" {
		desc += fmt.Sprintf(", %s", param.ParamSchema.Description)
	}
	return desc
}

// generateDescription 生成接口描述
func (e *YAPIExporter) generateDescription(route models.RouteInfo) string {
	return fmt.Sprintf("Handler: %s\n包路径: %s\n生成时间: %s", 
		route.Handler, 
		route.PackagePath,
		time.Now().Format("2006-01-02 15:04:05"))
}

// generateMarkdown 生成Markdown文档
func (e *YAPIExporter) generateMarkdown(route models.RouteInfo) string {
	markdown := fmt.Sprintf("# %s %s\n\n", strings.ToUpper(route.Method), route.Path)
	markdown += fmt.Sprintf("**Handler**: `%s`\n\n", route.Handler)
	markdown += fmt.Sprintf("**包路径**: `%s`\n\n", route.PackagePath)
	
	if len(route.RequestParams) > 0 {
		markdown += "## 请求参数\n\n"
		markdown += "| 参数名 | 类型 | 位置 | 必需 | 描述 |\n"
		markdown += "|--------|------|------|------|------|\n"
		
		for _, param := range route.RequestParams {
			required := "否"
			if param.IsRequired {
				required = "是"
			}
			paramType := "string"
			if param.ParamSchema != nil {
				paramType = param.ParamSchema.Type
			}
			
			markdown += fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
				param.ParamName,
				paramType,
				param.ParamType,
				required,
				param.Source)
		}
		markdown += "\n"
	}
	
	markdown += fmt.Sprintf("**生成时间**: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	
	return markdown
}

// ensureOutputDir 确保输出目录存在
func (e *YAPIExporter) ensureOutputDir() error {
	if e.outputDir == "" {
		e.outputDir = "./yapi_exports"
	}
	
	return os.MkdirAll(e.outputDir, 0755)
}

// sanitizeFilename 清理文件名
func (e *YAPIExporter) sanitizeFilename(filename string) string {
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