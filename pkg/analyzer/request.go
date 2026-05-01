package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/models"
	"golang.org/x/tools/go/packages"
)

// requestRule 描述一种 gin.Context 上的取参 / 绑定方法。
type requestRule struct {
	method    string                                 // c.<method>(...)
	paramKind string                                 // query / body / path / form
	required  bool                                   // 默认必需性
	build     func(call *ast.CallExpr) *models.APISchema
	nameFromArg int // 取第几个 string 参数作为 paramName；-1 表示用结构体绑定
}

// 简单字符串 query 类参数
func stringSchema() *models.APISchema { return &models.APISchema{Type: "string"} }
func stringArraySchema() *models.APISchema {
	return &models.APISchema{Type: "array", Items: &models.APISchema{Type: "string"}}
}

// extractRequestParams 扫描 handler 函数体，识别请求参数提取调用。
func extractRequestParams(fn *ast.FuncDecl, pkg *packages.Package) []models.RequestParamInfo {
	if fn.Body == nil {
		return nil
	}
	rules := map[string]requestRule{
		"Query":           {"c.Query", "query", false, func(*ast.CallExpr) *models.APISchema { return stringSchema() }, 0},
		"DefaultQuery":    {"c.DefaultQuery", "query", false, func(*ast.CallExpr) *models.APISchema { return stringSchema() }, 0},
		"QueryArray":      {"c.QueryArray", "query", false, func(*ast.CallExpr) *models.APISchema { return stringArraySchema() }, 0},
		"QueryMap":        {"c.QueryMap", "query", false, func(*ast.CallExpr) *models.APISchema { return &models.APISchema{Type: "object"} }, 0},
		"Param":           {"c.Param", "path", true, func(*ast.CallExpr) *models.APISchema { return stringSchema() }, 0},
		"PostForm":        {"c.PostForm", "form", false, func(*ast.CallExpr) *models.APISchema { return stringSchema() }, 0},
		"DefaultPostForm": {"c.DefaultPostForm", "form", false, func(*ast.CallExpr) *models.APISchema { return stringSchema() }, 0},
		"GetHeader":       {"c.GetHeader", "header", false, func(*ast.CallExpr) *models.APISchema { return stringSchema() }, 0},
	}
	bindings := map[string]struct {
		method, kind string
	}{
		"Bind":            {"c.Bind", "body"},
		"ShouldBind":      {"c.ShouldBind", "body"},
		"ShouldBindJSON":  {"c.ShouldBindJSON", "body"},
		"BindJSON":        {"c.BindJSON", "body"},
		"ShouldBindXML":   {"c.ShouldBindXML", "body"},
		"ShouldBindYAML":  {"c.ShouldBindYAML", "body"},
		"ShouldBindQuery": {"c.ShouldBindQuery", "query"},
		"ShouldBindUri":   {"c.ShouldBindUri", "path"},
		"BindUri":         {"c.BindUri", "path"},
		"ShouldBindHeader": {"c.ShouldBindHeader", "header"},
	}

	var out []models.RequestParamInfo
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := ginContextMethod(call, pkg.TypesInfo)
		if !ok {
			return true
		}
		if rule, ok := rules[method]; ok {
			name := stringLit(call, rule.nameFromArg)
			if name == "" {
				return true
			}
			out = append(out, models.RequestParamInfo{
				ParamType:   rule.paramKind,
				ParamName:   name,
				ParamSchema: rule.build(call),
				IsRequired:  rule.required,
				Source:      rule.method,
			})
			return true
		}
		if b, ok := bindings[method]; ok && len(call.Args) > 0 {
			schema := bindArgSchema(call.Args[0], pkg.TypesInfo)
			if schema != nil {
				out = append(out, models.RequestParamInfo{
					ParamType:   b.kind,
					ParamName:   defaultBindName(b.kind),
					ParamSchema: schema,
					IsRequired:  true,
					Source:      b.method,
				})
			}
		}
		return true
	})
	return out
}

func defaultBindName(kind string) string {
	switch kind {
	case "body":
		return "request_body"
	case "query":
		return "query_struct"
	case "path":
		return "uri_params"
	case "header":
		return "header_struct"
	default:
		return "params"
	}
}

// ginContextMethod 检查 call 是否形如 ginCtx.Method(...)，返回方法名。
func ginContextMethod(call *ast.CallExpr, info *types.Info) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	obj := info.ObjectOf(ident)
	if obj == nil {
		return "", false
	}
	t := obj.Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", false
	}
	if named.Obj().Pkg().Path() != "github.com/gin-gonic/gin" || named.Obj().Name() != "Context" {
		return "", false
	}
	return sel.Sel.Name, true
}

func stringLit(call *ast.CallExpr, idx int) string {
	if idx < 0 || idx >= len(call.Args) {
		return ""
	}
	lit, ok := call.Args[idx].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, `"`+"`")
}

// bindArgSchema 解析 c.Bind*(arg) 中 arg 的类型 → APISchema。
func bindArgSchema(arg ast.Expr, info *types.Info) *models.APISchema {
	if u, ok := arg.(*ast.UnaryExpr); ok && u.Op == token.AND {
		arg = u.X
	}
	t := info.TypeOf(arg)
	if t == nil {
		return nil
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	return resolveType(t)
}
