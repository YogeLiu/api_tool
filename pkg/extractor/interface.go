// 文件位置: pkg/extractor/interface.go
package extractor

import (
	"go/ast"
	"go/types"

	"github.com/YogeLiu/api-tool/pkg/models"

	"golang.org/x/tools/go/packages"
)

// TypeResolver 定义了一个函数签名，分析器将实现此函数并将其作为回调传递，
// 用于将 Go 底层类型解析为我们自定义的 FieldInfo 模型。
type TypeResolver func(typ types.Type) *models.FieldInfo

// Extractor 定义了从特定Web框架中提取API信息的通用方法。
type Extractor interface {
	// FindRootRouters 在已加载的包中查找并返回所有根路由对象的 `types.Object`。
	FindRootRouters(pkgs []*packages.Package) []types.Object

	// IsRouteGroupCall 判断一个调用表达式是否为路由分组（如 .Group()）。
	// 返回值: isGroup 表示是否为分组调用，pathSegment 表示分组的路径段
	IsRouteGroupCall(callExpr *ast.CallExpr, typeInfo *types.Info) (isGroup bool, pathSegment string)

	// IsHTTPMethodCall 判断一个调用表达式是否为 HTTP 方法注册。
	// 返回值: isHTTP 表示是否为HTTP方法调用，httpMethod 表示HTTP方法名，pathSegment 表示路径段
	IsHTTPMethodCall(callExpr *ast.CallExpr, typeInfo *types.Info) (isHTTP bool, httpMethod, pathSegment string)

	// ExtractRequest 使用 TypeResolver 回调来提取 Handler 函数中的请求信息。
	ExtractRequest(handlerDecl *ast.FuncDecl, typeInfo *types.Info, resolver TypeResolver) models.RequestInfo

	// ExtractResponse 使用 TypeResolver 回调来提取 Handler 函数中的响应信息。
	ExtractResponse(handlerDecl *ast.FuncDecl, typeInfo *types.Info, resolver TypeResolver) models.ResponseInfo

	// GetFrameworkName 返回当前提取器支持的框架名称
	GetFrameworkName() string
}
