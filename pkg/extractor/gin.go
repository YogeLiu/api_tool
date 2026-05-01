// Package extractor 负责从 Gin 项目的 AST 中识别所有路由注册点。
//
// 核心模型：
//   - 根路由器 root：来自 gin.New()/gin.Default() 的赋值左值。
//   - 路由分组函数 group func：函数签名中至少有一个 *gin.Engine 或
//     *gin.RouterGroup 形参；通过 InitRouter(r *gin.Engine) 这种调用
//     传播路由器对象。
//   - 路径累积：通过对路由器对象的 .Group("/x") 调用产生子路由器对象，
//     每段路径与父路径拼接后传递给后续注册。
package extractor

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Route 一条已识别的 HTTP 路由注册。
type Route struct {
	Method      string            // GET / POST / ...
	Path        string            // 累积后的完整路径（带前导 /）
	HandlerArg  ast.Expr          // .GET("/x", h) 中的 h 表达式
	Pkg         *packages.Package // 注册点所在包，用于 TypesInfo 查询
	RegisterPos token.Pos         // 注册调用位置，便于排序/去重
}

// groupFunc 一个接收路由器形参的函数。
type groupFunc struct {
	Decl     *ast.FuncDecl
	Pkg      *packages.Package
	ParamIdx int          // 路由器形参在参数列表中的索引（按单一变量计）
	ParamObj types.Object // 形参对应的 Object，用作子作用域路由器
	Key      string       // pkgPath + "." + funcName
}

var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true,
	"PATCH": true, "HEAD": true, "OPTIONS": true, "Any": true,
}

// FindRoutes 扫描所有包，返回去重后的路由列表。
func FindRoutes(pkgs []*packages.Package) []Route {
	groups := indexGroupFuncs(pkgs)
	roots := findRootEngines(pkgs)

	seen := make(map[string]bool)
	var routes []Route

	visit := func(routerObj types.Object, parentPath string) {
		w := &walker{
			pkgs:    pkgs,
			groups:  groups,
			seen:    seen,
			visited: make(map[string]bool),
		}
		w.walk(routerObj, parentPath)
		routes = append(routes, w.routes...)
	}

	for _, r := range roots {
		visit(r, "")
	}

	return routes
}

// indexGroupFuncs 构造路由分组函数索引。
func indexGroupFuncs(pkgs []*packages.Package) map[string]*groupFunc {
	out := make(map[string]*groupFunc)
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || fn.Type.Params == nil {
					continue
				}
				idx := -1
				var paramObj types.Object
				running := 0
				for _, field := range fn.Type.Params.List {
					if isGinRouterType(pkg.TypesInfo.TypeOf(field.Type)) {
						if len(field.Names) > 0 {
							paramObj = pkg.TypesInfo.ObjectOf(field.Names[0])
						}
						idx = running
						break
					}
					if len(field.Names) == 0 {
						running++
					} else {
						running += len(field.Names)
					}
				}
				if idx == -1 || paramObj == nil {
					continue
				}
				key := pkg.PkgPath + "." + fn.Name.Name
				out[key] = &groupFunc{
					Decl:     fn,
					Pkg:      pkg,
					ParamIdx: idx,
					ParamObj: paramObj,
					Key:      key,
				}
			}
		}
	}
	return out
}

// findRootEngines 找到形如 `x := gin.New()` 或 `x := gin.Default()` 的左值对象。
func findRootEngines(pkgs []*packages.Package) []types.Object {
	var roots []types.Object
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
					return true
				}
				call, ok := assign.Rhs[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "gin" {
					return true
				}
				if sel.Sel.Name != "New" && sel.Sel.Name != "Default" {
					return true
				}
				lhs, ok := assign.Lhs[0].(*ast.Ident)
				if !ok {
					return true
				}
				if obj := pkg.TypesInfo.ObjectOf(lhs); obj != nil {
					roots = append(roots, obj)
				}
				return true
			})
		}
	}
	return roots
}

type walker struct {
	pkgs    []*packages.Package
	groups  map[string]*groupFunc
	seen    map[string]bool // 全局路由去重 key: METHOD path handler-pos
	visited map[string]bool // 当前递归链上的 group func，防环
	routes  []Route
}

// walk 在所有包/某个函数体内查找对 routerObj 的引用，识别 Group/HTTP/转发调用。
func (w *walker) walk(routerObj types.Object, parentPath string) {
	if routerObj == nil {
		return
	}
	// 路由器对象作用域：要么是包级（root），要么是某个函数形参/局部变量。
	scope := routerObj.Parent()
	if scope == nil {
		return
	}

	for _, pkg := range w.pkgs {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				w.handleCall(call, pkg, routerObj, parentPath)
				return true
			})
		}
	}
}

func (w *walker) handleCall(call *ast.CallExpr, pkg *packages.Package, routerObj types.Object, parentPath string) {
	// 形如 router.X(...)
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		ident, ok := sel.X.(*ast.Ident)
		if ok && pkg.TypesInfo.ObjectOf(ident) == routerObj {
			method := sel.Sel.Name
			if method == "Group" {
				w.handleGroupCall(call, pkg, parentPath)
				return
			}
			if httpMethods[method] && method != "Any" {
				w.handleHTTPCall(call, pkg, method, parentPath)
				return
			}
		}
	}

	// 形如 InitRouter(router) — 转发到分组函数
	for i, arg := range call.Args {
		argIdent, ok := arg.(*ast.Ident)
		if !ok {
			continue
		}
		if pkg.TypesInfo.ObjectOf(argIdent) != routerObj {
			continue
		}
		gf := w.resolveGroupFunc(call, pkg)
		if gf == nil || gf.ParamIdx != i {
			continue
		}
		if w.visited[gf.Key] {
			continue
		}
		w.visited[gf.Key] = true
		// 在被调用函数体内继续查找对 ParamObj 的引用
		w.walkInFunc(gf, parentPath)
		delete(w.visited, gf.Key)
	}
}

// handleGroupCall 处理 router.Group("/x") -> 找到赋值左值并递归。
func (w *walker) handleGroupCall(call *ast.CallExpr, pkg *packages.Package, parentPath string) {
	seg := stringArg(call, 0)
	newPath := joinPath(parentPath, seg)
	groupObj := findGroupAssignTarget(pkg, call)
	if groupObj == nil {
		return
	}
	// 递归：以新对象为 router、以累积路径继续。
	sub := &walker{
		pkgs: w.pkgs, groups: w.groups, seen: w.seen,
		visited: w.visited,
	}
	sub.walk(groupObj, newPath)
	w.routes = append(w.routes, sub.routes...)
}

// handleHTTPCall 处理 router.GET("/x", h) -> 输出 Route。
func (w *walker) handleHTTPCall(call *ast.CallExpr, pkg *packages.Package, method, parentPath string) {
	if len(call.Args) < 2 {
		return
	}
	seg := stringArg(call, 0)
	if seg == "" {
		return
	}
	full := joinPath(parentPath, seg)
	handlerArg := call.Args[len(call.Args)-1]

	// 注册位置作为去重 key 的一部分（同一注册点不会重复出现）
	key := method + " " + full + " " + pkg.Fset.Position(call.Pos()).String()
	if w.seen[key] {
		return
	}
	w.seen[key] = true

	w.routes = append(w.routes, Route{
		Method:      method,
		Path:        full,
		HandlerArg:  handlerArg,
		Pkg:         pkg,
		RegisterPos: call.Pos(),
	})
}

// walkInFunc 在 gf 的函数体内查找对 gf.ParamObj 的引用。
func (w *walker) walkInFunc(gf *groupFunc, parentPath string) {
	ast.Inspect(gf.Decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		w.handleCall(call, gf.Pkg, gf.ParamObj, parentPath)
		return true
	})
}

// resolveGroupFunc 把 InitRouter(...) 这样的调用解析为已索引的 groupFunc。
func (w *walker) resolveGroupFunc(call *ast.CallExpr, pkg *packages.Package) *groupFunc {
	var obj types.Object
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		obj = pkg.TypesInfo.ObjectOf(fun)
	case *ast.SelectorExpr:
		obj = pkg.TypesInfo.ObjectOf(fun.Sel)
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return nil
	}
	return w.groups[fn.Pkg().Path()+"."+fn.Name()]
}

// findGroupAssignTarget 在某个赋值（含链式调用）中定位 .Group(...) 调用对应的左值对象。
func findGroupAssignTarget(pkg *packages.Package, target *ast.CallExpr) types.Object {
	var found types.Object
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			if found != nil {
				return false
			}
			switch s := n.(type) {
			case *ast.AssignStmt:
				for i, rhs := range s.Rhs {
					if i >= len(s.Lhs) {
						break
					}
					if !exprContainsCall(rhs, target) {
						continue
					}
					if id, ok := s.Lhs[i].(*ast.Ident); ok {
						if obj := pkg.TypesInfo.ObjectOf(id); obj != nil {
							found = obj
							return false
						}
					}
				}
			case *ast.ValueSpec:
				for i, v := range s.Values {
					if i >= len(s.Names) {
						break
					}
					if !exprContainsCall(v, target) {
						continue
					}
					if obj := pkg.TypesInfo.ObjectOf(s.Names[i]); obj != nil {
						found = obj
						return false
					}
				}
			}
			return true
		})
		if found != nil {
			return found
		}
	}
	return nil
}

// exprContainsCall 判断 outer 的求值是否最终会调用到 target（处理链式调用）。
func exprContainsCall(outer ast.Expr, target *ast.CallExpr) bool {
	if outer == target {
		return true
	}
	if call, ok := outer.(*ast.CallExpr); ok {
		if call == target {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			return exprContainsCall(sel.X, target)
		}
	}
	return false
}

func isGinRouterType(t types.Type) bool {
	if t == nil {
		return false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	if obj.Pkg().Path() != "github.com/gin-gonic/gin" {
		return false
	}
	return obj.Name() == "Engine" || obj.Name() == "RouterGroup" || obj.Name() == "IRouter" || obj.Name() == "IRoutes"
}

func stringArg(call *ast.CallExpr, idx int) string {
	if len(call.Args) <= idx {
		return ""
	}
	lit, ok := call.Args[idx].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, `"`+"`")
}

func joinPath(base, seg string) string {
	if seg == "" {
		return base
	}
	if !strings.HasPrefix(seg, "/") {
		seg = "/" + seg
	}
	if base == "" {
		return seg
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return strings.TrimRight(base, "/") + seg
}
