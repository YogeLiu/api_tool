package models

// APIInfo 整个项目的 API 信息汇总。
type APIInfo struct {
	APINumber int         `json:"api_number,omitempty"`
	Routes    []RouteInfo `json:"routes"`
}

// RouteInfo 单个 API 路由。
type RouteInfo struct {
	PackageName      string `json:"package_name"`
	PackagePath      string `json:"package_path"`
	Method           string `json:"method"`
	Path             string `json:"path"`
	Handler          string `json:"handler"`
	HandlerStartLine int    `json:"handler_start_line"`
	HandlerEndLine   int    `json:"handler_end_line"`

	RequestParams  []RequestParamInfo `json:"request_params,omitempty"`
	ResponseSchema *APISchema         `json:"response_schema,omitempty"`
}

// RequestParamInfo Handler 中识别到的一个请求参数来源。
type RequestParamInfo struct {
	ParamType   string     `json:"param_type"` // query / body / path / form
	ParamName   string     `json:"param_name"`
	ParamSchema *APISchema `json:"param_schema"`
	IsRequired  bool       `json:"is_required"`
	Source      string     `json:"source"` // 调用来源，如 c.Query / c.ShouldBindJSON
}

// APISchema 描述请求或响应的数据结构。
type APISchema struct {
	Type        string                `json:"type"`
	Properties  map[string]*APISchema `json:"properties,omitempty"`
	Items       *APISchema            `json:"items,omitempty"`
	Description string                `json:"description,omitempty"`
	JSONTag     string                `json:"json_tag,omitempty"`
}
