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
	PackageName string       `json:"package_name"` // 包名
	PackagePath string       `json:"package_path"` // 包路径
	Method      string       `json:"method"`       // HTTP方法 (GET, POST, PUT, DELETE等)
	Path        string       `json:"path"`         // 路由路径
	Handler     string       `json:"handler"`      // 处理函数名称
	Request     RequestInfo  `json:"request"`      // 请求信息
	Response    ResponseInfo `json:"response"`     // 响应信息
	
	// 集成func_body解析结果
	RequestParams []RequestParamInfo `json:"request_params,omitempty"` // 详细请求参数信息（来自func_body解析）
	ResponseSchema *APISchema        `json:"response_schema,omitempty"` // 详细响应结构信息（来自func_body解析）
}

// RequestInfo 代表API请求的信息
type RequestInfo struct {
	Params []FieldInfo `json:"params,omitempty"` // 路径参数
	Query  []FieldInfo `json:"query,omitempty"`  // 查询参数
	Body   *FieldInfo  `json:"body,omitempty"`   // 请求体
}

// ResponseInfo 代表API响应的信息
type ResponseInfo struct {
	Responses   map[string]*ResponseDetail `json:"responses,omitempty"` // 状态码到响应的映射
	DefaultResp *ResponseDetail            `json:"default,omitempty"`   // 默认响应
	Body        *FieldInfo                 `json:"body,omitempty"`      // 保持兼容性的简单响应体
}

// ResponseDetail 代表具体的响应详情
type ResponseDetail struct {
	StatusCode  int           `json:"status_code"`           // HTTP状态码
	Schema      *FieldInfo    `json:"schema,omitempty"`      // 响应数据结构
	Description string        `json:"description,omitempty"` // 响应描述
	Examples    []interface{} `json:"examples,omitempty"`    // 示例数据
	Condition   string        `json:"condition,omitempty"`   // 触发条件
	CallSite    *CallSiteInfo `json:"call_site,omitempty"`   // 调用点信息
}

// CallSiteInfo 记录响应调用的位置信息
type CallSiteInfo struct {
	LineNumber int            `json:"line_number"`           // 代码行号
	Method     string         `json:"method"`                // JSON/IndentedJSON等
	IsInBranch bool           `json:"is_in_branch"`          // 是否在条件分支中
	BranchInfo *BranchContext `json:"branch_info,omitempty"` // 分支上下文信息
}

// BranchContext 分支上下文信息
type BranchContext struct {
	Type        string  `json:"type"`                  // if/switch/defer/normal
	Condition   string  `json:"condition,omitempty"`   // 分支条件描述
	IsErrorPath bool    `json:"is_error_path"`         // 是否为错误处理分支
	Probability float64 `json:"probability,omitempty"` // 分支执行概率
}

// DirectJSONCall 直接的JSON调用信息
type DirectJSONCall struct {
	CallExpr     *ast.CallExpr  `json:"-"`            // AST调用表达式
	ContextName  string         `json:"context_name"` // Context参数名
	Method       string         `json:"method"`       // JSON/IndentedJSON/SecureJSON
	StatusCode   ast.Expr       `json:"-"`            // HTTP状态码表达式
	ResponseData ast.Expr       `json:"-"`            // 响应数据表达式
	LineNumber   int            `json:"line_number"`  // 代码行号
	IsInBranch   bool           `json:"is_in_branch"` // 是否在条件分支中
	BranchInfo   *BranchContext `json:"branch_info"`  // 分支上下文信息
}

// CallChain 调用链信息
type CallChain struct {
	Calls       []FunctionCall  `json:"calls"`        // 调用链
	FinalJSON   *DirectJSONCall `json:"final_json"`   // 最终的JSON调用
	MaxDepth    int             `json:"max_depth"`    // 最大递归深度
	Visited     map[string]bool `json:"-"`            // 已访问函数集合
	TraceResult string          `json:"trace_result"` // 追踪结果状态
}

// FunctionCall 函数调用信息
type FunctionCall struct {
	FuncName    string         `json:"func_name"`    // 函数名
	PackagePath string         `json:"package_path"` // 包路径
	Arguments   []ArgumentInfo `json:"arguments"`    // 参数信息
	CallSite    *ast.CallExpr  `json:"-"`            // 调用点
	Definition  *ast.FuncDecl  `json:"-"`            // 函数定义
	IsExternal  bool           `json:"is_external"`  // 是否为外部包函数
}

// ArgumentInfo 参数信息
type ArgumentInfo struct {
	Name       string   `json:"name"`  // 参数名
	Type       string   `json:"type"`  // 参数类型
	Expression ast.Expr `json:"-"`     // 参数表达式
	Value      string   `json:"value"` // 参数值描述
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

// ResponseFunction 代表响应封装函数的信息
type ResponseFunction struct {
	PackagePath     string            `json:"package_path"`      // 包路径
	FunctionName    string            `json:"function_name"`     // 函数名称
	FuncDecl        *ast.FuncDecl     `json:"-"`                 // 函数声明（不序列化）
	Package         *packages.Package `json:"-"`                 // 所属包（不序列化）
	ContextParamIdx int               `json:"context_param_idx"` // gin.Context参数在参数列表中的索引
	DataParamIdx    int               `json:"data_param_idx"`    // 业务数据参数索引 (-1表示没有)
	JSONCallSite    *ast.CallExpr     `json:"-"`                 // 内部JSON调用的位置
	BaseResponse    *FieldInfo        `json:"base_response"`     // 基础响应结构
	DataFieldPath   string            `json:"data_field_path"`   // 业务数据在基础响应中的字段路径 (如"Data")
	UniqueKey       string            `json:"unique_key"`        // 唯一标识 (packagePath+functionName)
	IsSuccessFunc   bool              `json:"is_success_func"`   // 是否为成功响应函数
}

// ResponseFunctionAnalysis 响应函数分析结果
type ResponseFunctionAnalysis struct {
	Functions           map[string]*ResponseFunction `json:"functions"`             // 所有响应函数的映射
	SuccessFunctions    []string                     `json:"success_functions"`     // 成功响应函数列表
	ErrorFunctions      []string                     `json:"error_functions"`       // 错误响应函数列表
	DirectJSONFunctions []string                     `json:"direct_json_functions"` // 直接调用JSON的函数列表
}

// ============ func_body解析结果类型定义 ============

// RequestParamInfo 请求参数信息（来自func_body解析）
type RequestParamInfo struct {
	ParamType   string     `json:"param_type"`   // "query", "body", "path"
	ParamName   string     `json:"param_name"`   // 参数名称
	ParamSchema *APISchema `json:"param_schema"` // 参数结构
	IsRequired  bool       `json:"is_required"`  // 是否必需
	Source      string     `json:"source"`       // 来源方法: "c.Query", "c.ShouldBindJSON", etc.
}

// APISchema API结构定义（来自func_body解析）
type APISchema struct {
	Type        string                `json:"type"`
	Properties  map[string]*APISchema `json:"properties,omitempty"`
	Items       *APISchema            `json:"items,omitempty"`
	Description string                `json:"description,omitempty"`
	JSONTag     string                `json:"json_tag,omitempty"`
}
