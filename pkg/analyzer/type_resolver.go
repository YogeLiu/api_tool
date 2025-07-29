// 文件位置: pkg/analyzer/type_resolver.go
package analyzer

import (
	"go/types"
	"reflect"
	"strings"

	"github.com/YogeLiu/api-tool/pkg/models"
	"github.com/YogeLiu/api-tool/pkg/parser"
)

// resolveType 实现TypeResolver接口，将types.Type转换为models.FieldInfo
func (a *Analyzer) resolveType(typ types.Type) *models.FieldInfo {
	return a.resolveTypeRecursive(typ, make(map[string]bool))
}

// resolveTypeRecursive 递归解析类型，防止无限递归
func (a *Analyzer) resolveTypeRecursive(typ types.Type, visited map[string]bool) *models.FieldInfo {
	if typ == nil {
		return &models.FieldInfo{Type: "unknown"}
	}

	// 处理指针类型
	if ptr, ok := typ.(*types.Pointer); ok {
		return a.resolveTypeRecursive(ptr.Elem(), visited)
	}

	// 处理基本类型
	if basic, ok := typ.(*types.Basic); ok {
		return &models.FieldInfo{
			Type: basic.Name(),
		}
	}

	// 处理切片类型
	if slice, ok := typ.(*types.Slice); ok {
		elemInfo := a.resolveTypeRecursive(slice.Elem(), visited)
		return &models.FieldInfo{
			Type:  "[]",
			Items: elemInfo,
		}
	}

	// 处理数组类型
	if array, ok := typ.(*types.Array); ok {
		elemInfo := a.resolveTypeRecursive(array.Elem(), visited)
		return &models.FieldInfo{
			Type:  "array",
			Items: elemInfo,
		}
	}

	// 处理Map类型
	if mapType, ok := typ.(*types.Map); ok {
		keyInfo := a.resolveTypeRecursive(mapType.Key(), visited)
		valueInfo := a.resolveTypeRecursive(mapType.Elem(), visited)
		return &models.FieldInfo{
			Type: "map[" + keyInfo.Type + "]" + valueInfo.Type,
		}
	}

	// 处理接口类型
	if iface, ok := typ.(*types.Interface); ok {
		if iface.Empty() {
			return &models.FieldInfo{Type: "interface{}"}
		}
		return &models.FieldInfo{Type: "interface"}
	}

	// 处理命名类型（结构体、自定义类型等）
	if named, ok := typ.(*types.Named); ok {
		return a.resolveNamedType(named, visited)
	}

	// 处理结构体类型
	if structType, ok := typ.(*types.Struct); ok {
		return a.resolveStructType(structType, visited)
	}

	return &models.FieldInfo{Type: typ.String()}
}

// resolveNamedType 解析命名类型
func (a *Analyzer) resolveNamedType(named *types.Named, visited map[string]bool) *models.FieldInfo {
	obj := named.Obj()
	if obj == nil {
		return &models.FieldInfo{Type: named.String()}
	}

	// 生成类型的唯一标识符
	typeName := obj.Name()
	pkgPath := ""
	if obj.Pkg() != nil {
		pkgPath = obj.Pkg().Path()
	}
	typeKey := pkgPath + "." + typeName

	// 检查是否已经访问过，防止循环引用
	if visited[typeKey] {
		return &models.FieldInfo{
			Type: typeName,
		}
	}
	visited[typeKey] = true

	// 查找类型定义
	fullType := parser.FullType{
		PackagePath: pkgPath,
		TypeName:    typeName,
	}

	typeSpec := a.project.GetTypeSpec(fullType)
	if typeSpec == nil {
		// 如果找不到类型定义，直接返回类型名
		return &models.FieldInfo{Type: typeName}
	}

	// 根据底层类型进行解析
	underlying := named.Underlying()
	if structType, ok := underlying.(*types.Struct); ok {
		// 是结构体类型，解析字段
		fieldInfo := a.resolveStructType(structType, visited)
		fieldInfo.Type = typeName // 使用命名类型的名称
		return fieldInfo
	}

	// 其他命名类型（如type alias）
	underlyingInfo := a.resolveTypeRecursive(underlying, visited)
	return &models.FieldInfo{
		Type:  typeName,
		Items: underlyingInfo,
	}
}

// resolveStructType 解析结构体类型
func (a *Analyzer) resolveStructType(structType *types.Struct, visited map[string]bool) *models.FieldInfo {
	var fields []models.FieldInfo

	for i := 0; i < structType.NumFields(); i++ {
		field := structType.Field(i)
		tag := structType.Tag(i)

		// 解析字段类型
		fieldType := a.resolveTypeRecursive(field.Type(), visited)

		// 提取JSON标签
		jsonTag := a.extractJSONTag(tag)

		// 跳过被忽略的字段
		if jsonTag == "-" {
			continue
		}

		fieldInfo := models.FieldInfo{
			Name:    field.Name(),
			JsonTag: jsonTag,
			Type:    fieldType.Type,
			Fields:  fieldType.Fields,
			Items:   fieldType.Items,
		}

		fields = append(fields, fieldInfo)
	}

	return &models.FieldInfo{
		Type:   "struct",
		Fields: fields,
	}
}

// extractJSONTag 从结构体标签中提取JSON标签
func (a *Analyzer) extractJSONTag(tag string) string {
	if tag == "" {
		return ""
	}

	// 解析结构体标签
	structTag := reflect.StructTag(tag)
	jsonTag := structTag.Get("json")

	if jsonTag == "" {
		return ""
	}

	// 处理JSON标签选项（如omitempty）
	parts := strings.Split(jsonTag, ",")
	if len(parts) > 0 {
		return parts[0]
	}

	return jsonTag
}

// isBuiltinType 检查是否为Go内置类型
// func (a *Analyzer) isBuiltinType(typeName string) bool {
// 	builtins := map[string]bool{
// 		"bool":       true,
// 		"byte":       true,
// 		"complex128": true,
// 		"complex64":  true,
// 		"error":      true,
// 		"float32":    true,
// 		"float64":    true,
// 		"int":        true,
// 		"int16":      true,
// 		"int32":      true,
// 		"int64":      true,
// 		"int8":       true,
// 		"rune":       true,
// 		"string":     true,
// 		"uint":       true,
// 		"uint16":     true,
// 		"uint32":     true,
// 		"uint64":     true,
// 		"uint8":      true,
// 		"uintptr":    true,
// 	}

// 	return builtins[typeName]
// }
