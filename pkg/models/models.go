// 文件位置: pkg/models/models.go
package models

import (
	"go/ast"

	"golang.org/x/tools/go/packages"
)

// APIInfo 代表整个API的结构化信息
type APIInfo struct {
	Routes []RouteInfo `json:"routes"`
}

// RouteInfo 代表单个API路由的信息
type RouteInfo struct {
	Method   string       `json:"method"`   // HTTP方法 (GET, POST, PUT, DELETE等)
	Path     string       `json:"path"`     // 路由路径
	Handler  string       `json:"handler"`  // 处理函数名称
	Request  RequestInfo  `json:"request"`  // 请求信息
	Response ResponseInfo `json:"response"` // 响应信息
}

// RequestInfo 代表API请求的信息
type RequestInfo struct {
	Params []FieldInfo `json:"params,omitempty"` // 路径参数
	Query  []FieldInfo `json:"query,omitempty"`  // 查询参数
	Body   *FieldInfo  `json:"body,omitempty"`   // 请求体
}

// ResponseInfo 代表API响应的信息
type ResponseInfo struct {
	Body *FieldInfo `json:"body,omitempty"` // 响应体
}

// FieldInfo 代表数据字段的结构化信息
type FieldInfo struct {
	Name    string      `json:"name"`             // 字段名称
	JsonTag string      `json:"json_tag"`         // JSON标签
	Type    string      `json:"type"`             // 字段类型
	Fields  []FieldInfo `json:"fields,omitempty"` // 嵌套字段（用于结构体）
	Items   *FieldInfo  `json:"items,omitempty"`  // 数组/切片元素类型
}

// RouterGroupFunction 代表路由分组函数的信息
type RouterGroupFunction struct {
	PackagePath    string            `json:"package_path"`     // 包路径
	FunctionName   string            `json:"function_name"`    // 函数名称
	FuncDecl       *ast.FuncDecl     `json:"-"`                // 函数声明（不序列化）
	Package        *packages.Package `json:"-"`                // 所属包（不序列化）
	RouterParamIdx int               `json:"router_param_idx"` // 路由器参数在参数列表中的索引
	UniqueKey      string            `json:"unique_key"`       // 唯一标识 (packagePath+functionName)
}
