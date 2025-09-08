package helper

import (
	"go/ast"

	"golang.org/x/tools/go/packages"
)

// ResponseEngine 响应解析引擎通用接口
type ResponseEngine interface {
	// AnalyzeHandler 分析Handler函数，返回统一的分析结果
	AnalyzeHandler(handlerDecl *ast.FuncDecl, pkg *packages.Package) *CommonHandlerAnalysisResult
}

// CommonHandlerAnalysisResult 通用的Handler分析结果
type CommonHandlerAnalysisResult struct {
	PackageName   string                   `json:"package_name"`
	PackagePath   string                   `json:"package_path"`
	FunctionName  string                   `json:"function_name"`
	RequestParams []CommonRequestParamInfo `json:"request_params,omitempty"`
	Response      *CommonAPISchema         `json:"response,omitempty"`
}

// CommonRequestParamInfo 通用的请求参数信息
type CommonRequestParamInfo struct {
	Name        string           `json:"name"`
	Type        string           `json:"type"`
	Source      string           `json:"source"` // 来源方法
	Required    bool             `json:"required"`
	Description string           `json:"description"`
	Schema      *CommonAPISchema `json:"schema,omitempty"`
}

// CommonAPISchema 通用的API Schema
type CommonAPISchema struct {
	Type        string                      `json:"type"`
	JSONTag     string                      `json:"json_tag,omitempty"`
	Description string                      `json:"description,omitempty"`
	Properties  map[string]*CommonAPISchema `json:"properties,omitempty"`
	Items       *CommonAPISchema            `json:"items,omitempty"`
}
