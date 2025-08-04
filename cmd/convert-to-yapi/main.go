// 文件位置: cmd/convert-to-yapi/main.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/YogeLiu/api-tool/pkg/exporter"
	"github.com/YogeLiu/api-tool/pkg/models"
)

func main() {
	inputFile := flag.String("input", "api_output.json", "输入的API JSON文件路径")
	outputDir := flag.String("output", "./yapi_exports", "输出目录")
	projectName := flag.String("project", "API Documentation", "项目名称")
	successOnly := flag.Bool("success-only", true, "仅提取成功响应（忽略错误响应）")
	flag.Parse()

	log.Printf("正在读取文件: %s", *inputFile)

	// 读取输入文件
	inputData, err := os.ReadFile(*inputFile)
	if err != nil {
		log.Fatalf("读取文件失败: %v", err)
	}

	// 解析JSON
	var rawAPIInfo map[string]interface{}
	if err := json.Unmarshal(inputData, &rawAPIInfo); err != nil {
		log.Fatalf("JSON解析失败: %v", err)
	}

	// 转换为APIInfo格式
	apiInfo := convertToAPIInfo(rawAPIInfo, *successOnly)

	log.Printf("找到 %d 个API接口", len(apiInfo.Routes))

	// 创建YAPI导出器
	yapiExporter := exporter.NewYAPIExporter(*projectName, "", *outputDir)

	// 导出YAPI格式
	if err := yapiExporter.Export(apiInfo); err != nil {
		log.Fatalf("YAPI导出失败: %v", err)
	}

	fmt.Println("✅ 转换完成！")
	if *successOnly {
		fmt.Println("📝 注意: 仅提取了成功响应字段，已过滤错误响应")
	}
}

// convertToAPIInfo 将原始JSON转换为APIInfo格式
func convertToAPIInfo(rawData map[string]interface{}, successOnly bool) *models.APIInfo {
	routes := []models.RouteInfo{}

	if routesData, ok := rawData["routes"].([]interface{}); ok {
		for _, routeData := range routesData {
			if routeMap, ok := routeData.(map[string]interface{}); ok {
				route := convertRoute(routeMap, successOnly)
				routes = append(routes, route)
			}
		}
	}

	return &models.APIInfo{
		Routes: routes,
	}
}

// convertRoute 转换单个路由
func convertRoute(routeMap map[string]interface{}, successOnly bool) models.RouteInfo {
	route := models.RouteInfo{
		PackageName: getString(routeMap, "package_name"),
		PackagePath: getString(routeMap, "package_path"),
		Method:      getString(routeMap, "method"),
		Path:        getString(routeMap, "path"),
		Handler:     getString(routeMap, "handler"),
		Request:     models.RequestInfo{},
		Response:    models.ResponseInfo{},
	}

	// 转换请求参数
	if requestParams, ok := routeMap["request_params"].([]interface{}); ok {
		route.RequestParams = convertRequestParams(requestParams)
	}

	// 转换响应结构
	if responseSchema, ok := routeMap["response_schema"].(map[string]interface{}); ok {
		route.ResponseSchema = convertResponseSchema(responseSchema, successOnly)
	}

	return route
}

// convertRequestParams 转换请求参数
func convertRequestParams(paramsData []interface{}) []models.RequestParamInfo {
	var params []models.RequestParamInfo

	for _, paramData := range paramsData {
		if paramMap, ok := paramData.(map[string]interface{}); ok {
			param := models.RequestParamInfo{
				ParamType:   getString(paramMap, "param_type"),
				ParamName:   getString(paramMap, "param_name"),
				IsRequired:  getBool(paramMap, "is_required"),
				Source:      getString(paramMap, "source"),
				ParamSchema: convertAPISchema(getMap(paramMap, "param_schema")),
			}
			params = append(params, param)
		}
	}

	return params
}

// convertResponseSchema 转换响应结构
func convertResponseSchema(schemaMap map[string]interface{}, successOnly bool) *models.APISchema {
	if successOnly {
		// 只提取data字段
		if properties, ok := schemaMap["properties"].(map[string]interface{}); ok {
			if dataField, ok := properties["data"].(map[string]interface{}); ok {
				return convertAPISchema(dataField)
			}
		}
	}

	return convertAPISchema(schemaMap)
}

// convertAPISchema 转换API Schema
func convertAPISchema(schemaMap map[string]interface{}) *models.APISchema {
	if schemaMap == nil {
		return nil
	}

	schema := &models.APISchema{
		Type:        getString(schemaMap, "type"),
		Description: getString(schemaMap, "description"),
		JSONTag:     getString(schemaMap, "json_tag"),
	}

	// 转换properties
	if properties, ok := schemaMap["properties"].(map[string]interface{}); ok {
		schema.Properties = make(map[string]*models.APISchema)
		for key, propData := range properties {
			if propMap, ok := propData.(map[string]interface{}); ok {
				schema.Properties[key] = convertAPISchema(propMap)
			}
		}
	}

	// 转换items
	if items, ok := schemaMap["items"].(map[string]interface{}); ok {
		schema.Items = convertAPISchema(items)
	}

	return schema
}

// 辅助函数
func getString(m map[string]interface{}, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

func getBool(m map[string]interface{}, key string) bool {
	if val, ok := m[key].(bool); ok {
		return val
	}
	return false
}

func getMap(m map[string]interface{}, key string) map[string]interface{} {
	if val, ok := m[key].(map[string]interface{}); ok {
		return val
	}
	return nil
}
