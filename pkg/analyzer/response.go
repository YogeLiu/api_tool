package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/YogeLiu/api-tool/pkg/models"
	"golang.org/x/tools/go/packages"
)

// wrapperIndex 仍然保留，用作 buildWrapperIndex 的产物——但当前实现里
// 它只是个占位（所有响应函数都用统一的 resolveCallExpr 路径解析）。
// 保留是为了 Analyze 的 API 形状稳定，将来若加缓存可直接挂在这里。
type wrapperIndex struct{}

func buildWrapperIndex(_ []*packages.Package) wrapperIndex { return wrapperIndex{} }

// analyzeResponse 启发式定位 handler 的响应表达式。
// 取按源码位置最后出现的响应来源：
//  1. c.JSON(status, X) 的 X
//  2. 同函数体里直接调用的形如 ResponseOK(c, data) / APIResponseOK(c, data) 的函数调用
//
// 二者按 token.Pos 取大者；这样 early-return 的错误分支不会覆盖成功分支。
func analyzeResponse(fn *ast.FuncDecl, pkg *packages.Package, _ wrapperIndex) *models.APISchema {
	if fn.Body == nil {
		return nil
	}
	var (
		lastExpr ast.Expr
		lastPos  token.Pos
	)
	consider := func(expr ast.Expr, pos token.Pos) {
		if pos >= lastPos {
			lastExpr = expr
			lastPos = pos
		}
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isCtxJSONCall(call, pkg.TypesInfo) {
			if len(call.Args) >= 2 {
				consider(call.Args[1], call.Pos())
			}
			return true
		}
		// 直接以语句形式调用、且首参是 *gin.Context 的函数（典型 void wrapper）
		if isCtxFirstArgCall(call, pkg.TypesInfo) {
			consider(call, call.Pos())
		}
		return true
	})

	if lastExpr == nil {
		return nil
	}
	return resolveExpr(lastExpr, pkg, newVisitSet())
}

// visitSet 防止函数递归展开时陷入自环。
type visitSet map[*ast.FuncDecl]bool

func newVisitSet() visitSet { return make(visitSet) }

// resolveExpr 递归解析一个响应表达式 → APISchema。
func resolveExpr(expr ast.Expr, pkg *packages.Package, visited visitSet) *models.APISchema {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return resolveCompositeLit(e, pkg, nil, visited)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return resolveExpr(e.X, pkg, visited)
		}
	case *ast.CallExpr:
		return resolveCallExpr(e, pkg, nil, visited)
	case *ast.Ident:
		if t := pkg.TypesInfo.TypeOf(e); t != nil {
			return resolveType(t)
		}
	}
	if t := pkg.TypesInfo.TypeOf(expr); t != nil {
		return resolveType(t)
	}
	return &models.APISchema{Type: "unknown"}
}

// resolveCallExpr 处理形如 ResponseOK(c, data) / APIResponseOK(c, data) 的调用。
// outer 是调用 call 的上层 callSite（可能为 nil，比如 handler 直接调 wrapper）。
//  1. 解析到目标 FuncDecl + 所在包；找不到 → 用返回类型兜底
//  2. 若函数有 return <expr>：解析 expr，把内部形参反查为外层实参
//  3. 若函数无返回值但内部 c.JSON(_, X)：递归展开 X
//  4. 否则用返回类型兜底
func resolveCallExpr(call *ast.CallExpr, callerPkg *packages.Package, outer *callSite, visited visitSet) *models.APISchema {
	fnDecl, fnPkg := lookupFuncDecl(call, callerPkg)
	if fnDecl == nil || fnPkg == nil {
		return fallbackByReturnType(call, callerPkg)
	}
	if visited[fnDecl] {
		return fallbackByReturnType(call, callerPkg)
	}
	visited[fnDecl] = true
	defer delete(visited, fnDecl)

	site := &callSite{fn: fnDecl, call: call, pkg: callerPkg, parent: outer}

	if ret := findReturnExpr(fnDecl); ret != nil {
		return resolveReturnExpr(ret, fnPkg, site, visited)
	}
	if expr := findCtxJSONArg(fnDecl.Body, fnPkg); expr != nil {
		return resolveReturnExpr(expr, fnPkg, site, visited)
	}
	return fallbackByReturnType(call, callerPkg)
}

// resolveReturnExpr 解析 site.fn 函数体内的一个响应表达式。
func resolveReturnExpr(expr ast.Expr, fnPkg *packages.Package, site *callSite, visited visitSet) *models.APISchema {
	switch e := expr.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return resolveReturnExpr(e.X, fnPkg, site, visited)
		}
	case *ast.CompositeLit:
		return resolveCompositeLit(e, fnPkg, site, visited)
	case *ast.CallExpr:
		// 嵌套调用：在 fnPkg 视角解析下一层，并把当前 site 作为它的 outer
		return resolveCallExpr(e, fnPkg, site, visited)
	case *ast.Ident:
		// 标识符：先查是否为形参，能反查到就用反查得到的类型
		if t := resolveIdentType(e.Name, site.fn, fnPkg, site); t != nil {
			return resolveType(t)
		}
		if t := fnPkg.TypesInfo.TypeOf(e); t != nil {
			return resolveType(t)
		}
	}
	if t := fnPkg.TypesInfo.TypeOf(expr); t != nil {
		return resolveType(t)
	}
	return &models.APISchema{Type: "unknown"}
}

// callSite 记录一次"上层调用"的现场，用于把内部形参替换为实参类型。
// parent 指向再上一层调用——这样多层 wrapper（APIResponseOK→ResponseOK）
// 中最内层 fn 的形参 ident 也能一路反查到最外层 handler 提供的实参类型。
type callSite struct {
	fn     *ast.FuncDecl     // 当前正在解析其内部表达式的函数
	call   *ast.CallExpr     // 调用 fn 的 CallExpr
	pkg    *packages.Package // call 所在的包（即调用方的 TypesInfo 来源）
	parent *callSite
}

// resolveIdentType 把"在 fn 内"看到的标识符解析为真实类型，
// 必要时沿 callSite 链回溯到调用方的实参。
// 返回 nil 表示无法解析。
func resolveIdentType(name string, fnDecl *ast.FuncDecl, fnPkg *packages.Package, site *callSite) types.Type {
	if fnDecl == nil {
		return nil
	}
	pi := paramIndex(fnDecl, name)
	if pi < 0 {
		return nil
	}
	// 当前层无 site：拿不到实参，返回形参声明类型
	if site == nil || site.call == nil || pi >= len(site.call.Args) {
		// fall back to the formal parameter type
		for _, field := range fnDecl.Type.Params.List {
			for _, n := range field.Names {
				if n.Name == name {
					return fnPkg.TypesInfo.TypeOf(field.Type)
				}
			}
		}
		return nil
	}
	arg := site.call.Args[pi]
	// 实参本身又是一个形参 ident？再往上找
	if id, ok := arg.(*ast.Ident); ok && site.parent != nil {
		if t := resolveIdentType(id.Name, site.fn, site.pkg, site.parent); t != nil {
			return t
		}
	}
	return site.pkg.TypesInfo.TypeOf(arg)
}

// resolveCompositeLit 解析 gin.H{...} 或命名结构体字面量。
// site 非空时，字面量中是形参标识符的字段会被替换为
// 沿 callSite 链回溯得到的真实类型。
func resolveCompositeLit(c *ast.CompositeLit, pkg *packages.Package,
	site *callSite, visited visitSet) *models.APISchema {

	t := pkg.TypesInfo.TypeOf(c)

	// gin.H 或任何 map 字面量
	if isGinH(t) || isMapLiteralType(t) {
		props := map[string]*models.APISchema{}
		for _, elt := range c.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key := stringKey(kv.Key)
			if key == "" {
				continue
			}
			s := resolveLitValue(kv.Value, pkg, site, visited)
			s.JSONTag = key
			props[key] = s
		}
		return &models.APISchema{Type: "object", Properties: props}
	}

	// 命名结构体：先按类型解析全部字段，再用字面量中的具体值类型替换 any 字段
	if t == nil {
		return &models.APISchema{Type: "object"}
	}
	schema := resolveType(t)
	if schema.Properties == nil {
		return schema
	}
	for _, elt := range c.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		id, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		field := findField(schema.Properties, id.Name)
		if field == nil || !isAnyType(field) {
			continue
		}
		refined := resolveLitValue(kv.Value, pkg, site, visited)
		refined.JSONTag = field.JSONTag
		setField(schema.Properties, id.Name, refined)
	}
	return schema
}

// resolveLitValue 解析字面量中的一个 value。
// 遇到 site.fn 的形参标识符时沿 callSite 链回溯到实参类型。
func resolveLitValue(expr ast.Expr, pkg *packages.Package, site *callSite, visited visitSet) *models.APISchema {
	if id, ok := expr.(*ast.Ident); ok && site != nil {
		if t := resolveIdentType(id.Name, site.fn, pkg, site); t != nil {
			return resolveType(t)
		}
	}
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return resolveCompositeLit(e, pkg, site, visited)
	case *ast.CallExpr:
		return resolveCallExpr(e, pkg, site, visited)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return resolveLitValue(e.X, pkg, site, visited)
		}
	}
	if t := pkg.TypesInfo.TypeOf(expr); t != nil {
		return resolveType(t)
	}
	return &models.APISchema{Type: "any"}
}

// fallbackByReturnType 用函数返回类型兜底；若有 any 字段且能猜到对应实参，则注入。
func fallbackByReturnType(call *ast.CallExpr, callerPkg *packages.Package) *models.APISchema {
	t := callerPkg.TypesInfo.TypeOf(call)
	if t == nil {
		return &models.APISchema{Type: "unknown"}
	}
	schema := resolveType(t)
	if schema == nil || schema.Properties == nil {
		return schema
	}
	// 尝试把第一个 interface{} 实参注入到名为 data/Data 的 any 字段
	for i, arg := range call.Args {
		argT := callerPkg.TypesInfo.TypeOf(arg)
		if argT == nil {
			continue
		}
		// 跳过 *gin.Context / context.Context
		if isGinContextType(argT) || isContextContext(argT) {
			continue
		}
		_ = i
		injected := resolveType(argT)
		replaceAnyField(schema, injected)
		break
	}
	return schema
}

// findReturnExpr 找到函数体内第一个 return 的第一个返回值表达式。
func findReturnExpr(fn *ast.FuncDecl) ast.Expr {
	if fn.Body == nil {
		return nil
	}
	var found ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) > 0 {
			found = r.Results[0]
			return false
		}
		return true
	})
	return found
}

// lookupFuncDecl 把一个 CallExpr 解析为它指向的 FuncDecl + 所在包。
func lookupFuncDecl(call *ast.CallExpr, pkg *packages.Package) (*ast.FuncDecl, *packages.Package) {
	var obj types.Object
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		obj = pkg.TypesInfo.ObjectOf(fun)
	case *ast.SelectorExpr:
		obj = pkg.TypesInfo.ObjectOf(fun.Sel)
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return nil, nil
	}
	// 从全局包池里找声明（pkg 自己 import 的可能不带 Syntax，要查根集合）
	target := findPackageByPath(pkg, fn.Pkg().Path())
	if target == nil {
		return nil, nil
	}
	for _, file := range target.Syntax {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if target.TypesInfo.ObjectOf(fd.Name) == fn {
				return fd, target
			}
		}
	}
	return nil, nil
}

// findPackageByPath 在 pkg 的依赖图（包括它自己）里找指定 PkgPath 的包。
// packages.Load 加载根包时，被引用的同模块包通常也在 syntax 里。
func findPackageByPath(pkg *packages.Package, path string) *packages.Package {
	if pkg.PkgPath == path {
		return pkg
	}
	if dep, ok := pkg.Imports[path]; ok && len(dep.Syntax) > 0 {
		return dep
	}
	// 沿依赖图 BFS 一层（多数情况下足够）
	for _, dep := range pkg.Imports {
		if dep.PkgPath == path && len(dep.Syntax) > 0 {
			return dep
		}
	}
	return nil
}

func isCtxJSONCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "JSON" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	obj := info.ObjectOf(ident)
	if obj == nil {
		return false
	}
	return isGinContextType(obj.Type())
}

// isCtxFirstArgCall 判定一个调用的首个实参是否为 *gin.Context（typical wrapper 调用）。
func isCtxFirstArgCall(call *ast.CallExpr, info *types.Info) bool {
	if len(call.Args) == 0 {
		return false
	}
	// 跳过形如 c.X(...) 的方法调用——那是 c 自己的方法，不是把 c 作为参数传给 wrapper
	if _, ok := call.Fun.(*ast.SelectorExpr); ok {
		// 仍允许 pkg.Func(c, ...)：判断 X 是否为包标识符
		sel := call.Fun.(*ast.SelectorExpr)
		if id, ok := sel.X.(*ast.Ident); ok {
			obj := info.ObjectOf(id)
			if _, isPkg := obj.(*types.PkgName); !isPkg {
				return false
			}
		}
	}
	t := info.TypeOf(call.Args[0])
	return isGinContextType(t)
}

func findCtxJSONArg(body *ast.BlockStmt, pkg *packages.Package) ast.Expr {
	var found ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if !isCtxJSONCall(call, pkg.TypesInfo) {
			return true
		}
		if len(call.Args) >= 2 {
			found = call.Args[1]
		}
		return false
	})
	return found
}

func paramIndex(fn *ast.FuncDecl, name string) int {
	if fn.Type.Params == nil {
		return -1
	}
	idx := 0
	for _, field := range fn.Type.Params.List {
		for _, n := range field.Names {
			if n.Name == name {
				return idx
			}
			idx++
		}
		if len(field.Names) == 0 {
			idx++
		}
	}
	return -1
}

// findField 按字面量里的 Go 字段名查找 schema 字段。
// schema 的 key 通常是 JSON tag（小写），而 name 来自 ast 的字段标识符（大写），
// 所以做大小写不敏感与 JSONTag 双重匹配。
func findField(props map[string]*models.APISchema, name string) *models.APISchema {
	if v, ok := props[name]; ok {
		return v
	}
	for k, v := range props {
		if equalFold(k, name) || equalFold(v.JSONTag, name) {
			return v
		}
	}
	return nil
}

func setField(props map[string]*models.APISchema, name string, val *models.APISchema) {
	if _, ok := props[name]; ok {
		props[name] = val
		return
	}
	for k, v := range props {
		if equalFold(k, name) || equalFold(v.JSONTag, name) {
			props[k] = val
			return
		}
	}
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func replaceAnyField(schema, injected *models.APISchema) {
	if schema == nil || schema.Properties == nil || injected == nil {
		return
	}
	for _, key := range []string{"data", "Data"} {
		if v, ok := schema.Properties[key]; ok && isAnyType(v) {
			injected.JSONTag = v.JSONTag
			if injected.JSONTag == "" {
				injected.JSONTag = key
			}
			schema.Properties[key] = injected
			return
		}
	}
	for k, v := range schema.Properties {
		if isAnyType(v) {
			injected.JSONTag = v.JSONTag
			if injected.JSONTag == "" {
				injected.JSONTag = k
			}
			schema.Properties[k] = injected
			return
		}
	}
}

func isAnyType(s *models.APISchema) bool {
	return s != nil && (s.Type == "any" || s.Type == "interface")
}

func stringKey(expr ast.Expr) string {
	switch k := expr.(type) {
	case *ast.BasicLit:
		if k.Kind == token.STRING {
			return trimQuote(k.Value)
		}
	case *ast.Ident:
		return k.Name
	}
	return ""
}

func trimQuote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '`') {
		return s[1 : len(s)-1]
	}
	return s
}

func isGinContextType(t types.Type) bool {
	if t == nil {
		return false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == "github.com/gin-gonic/gin" && named.Obj().Name() == "Context"
}

func isContextContext(t types.Type) bool {
	if t == nil {
		return false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == "context" && named.Obj().Name() == "Context"
}

func isGinH(t types.Type) bool {
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == "github.com/gin-gonic/gin" && named.Obj().Name() == "H"
}

func isMapLiteralType(t types.Type) bool {
	if t == nil {
		return false
	}
	if named, ok := t.(*types.Named); ok {
		t = named.Underlying()
	}
	_, ok := t.(*types.Map)
	return ok
}
