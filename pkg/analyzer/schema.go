package analyzer

import (
	"fmt"
	"go/types"
	"reflect"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/models"
)

const maxSchemaDepth = 10

// resolveType 把 go/types 类型递归翻译为 models.APISchema。
// 已访问的命名类型用 visited 防止结构体自环。
func resolveType(t types.Type) *models.APISchema {
	return resolveTypeAt(t, maxSchemaDepth, map[*types.Named]bool{})
}

func resolveTypeAt(t types.Type, depth int, visited map[*types.Named]bool) *models.APISchema {
	if t == nil {
		return &models.APISchema{Type: "unknown"}
	}
	if depth <= 0 {
		return &models.APISchema{Type: "object", Description: "max depth"}
	}

	switch x := t.(type) {
	case *types.Pointer:
		return resolveTypeAt(x.Elem(), depth, visited)
	case *types.Basic:
		return &models.APISchema{Type: basicKind(x.Kind())}
	case *types.Slice:
		return &models.APISchema{Type: "array", Items: resolveTypeAt(x.Elem(), depth-1, visited)}
	case *types.Array:
		return &models.APISchema{Type: "array", Items: resolveTypeAt(x.Elem(), depth-1, visited)}
	case *types.Map:
		k := resolveTypeAt(x.Key(), depth-1, visited)
		v := resolveTypeAt(x.Elem(), depth-1, visited)
		return &models.APISchema{
			Type:        fmt.Sprintf("map[%s]%s", k.Type, v.Type),
			Description: "map",
			Items:       v,
		}
	case *types.Interface:
		if x.Empty() {
			return &models.APISchema{Type: "any", Description: "interface{}"}
		}
		return &models.APISchema{Type: "interface"}
	case *types.Named:
		if visited[x] {
			return &models.APISchema{Type: x.Obj().Name(), Description: "recursive"}
		}
		visited[x] = true
		defer delete(visited, x)
		if s, ok := x.Underlying().(*types.Struct); ok {
			schema := resolveStruct(s, depth-1, visited)
			schema.Type = x.Obj().Name()
			return schema
		}
		inner := resolveTypeAt(x.Underlying(), depth-1, visited)
		inner.Description = "alias for " + inner.Type
		inner.Type = x.Obj().Name()
		return inner
	case *types.Struct:
		return resolveStruct(x, depth, visited)
	}
	return &models.APISchema{Type: t.String(), Description: "unhandled"}
}

func resolveStruct(s *types.Struct, depth int, visited map[*types.Named]bool) *models.APISchema {
	props := make(map[string]*models.APISchema)
	for i := 0; i < s.NumFields(); i++ {
		f := s.Field(i)
		if !f.Exported() {
			continue
		}
		jsonTag := jsonTagFrom(s.Tag(i))
		if jsonTag == "-" {
			continue
		}
		key := f.Name()
		if jsonTag != "" {
			key = jsonTag
		}
		field := resolveTypeAt(f.Type(), depth, visited)
		field.JSONTag = jsonTag
		if jsonTag == "" {
			field.JSONTag = f.Name()
		}
		props[key] = field
	}
	return &models.APISchema{Type: "object", Properties: props}
}

func jsonTagFrom(structTag string) string {
	if structTag == "" {
		return ""
	}
	tag := reflect.StructTag(structTag).Get("json")
	if tag == "" {
		return ""
	}
	if i := strings.Index(tag, ","); i != -1 {
		tag = tag[:i]
	}
	return tag
}

func basicKind(k types.BasicKind) string {
	switch k {
	case types.Bool:
		return "boolean"
	case types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr:
		return "integer"
	case types.Float32, types.Float64:
		return "number"
	case types.Complex64, types.Complex128:
		return "complex"
	case types.String:
		return "string"
	case types.UnsafePointer:
		return "pointer"
	default:
		return "unknown"
	}
}
