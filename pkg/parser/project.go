// 文件位置: pkg/parser/project.go
package parser

import (
	"go/ast"

	"golang.org/x/tools/go/packages"
)

// FullType 表示完整的类型信息，包括包路径和类型名称
type FullType struct {
	PackagePath string // 包的完整路径
	TypeName    string // 类型名称
}

// Project 代表一个已解析的Go项目
type Project struct {
	// Packages 包含所有已加载的Go包
	Packages []*packages.Package

	// TypeRegistry 是全局类型注册表，键为FullType，值为对应的AST类型规范
	TypeRegistry map[FullType]*ast.TypeSpec

	// PackageInfo 提供对包信息的快速访问
	PackageInfo map[string]*packages.Package
}

// NewProject 创建一个新的Project实例
func NewProject(pkgs []*packages.Package) *Project {
	project := &Project{
		Packages:     pkgs,
		TypeRegistry: make(map[FullType]*ast.TypeSpec),
		PackageInfo:  make(map[string]*packages.Package),
	}

	// 构建包信息映射
	for _, pkg := range pkgs {
		project.PackageInfo[pkg.PkgPath] = pkg
	}

	// 构建类型注册表
	project.buildTypeRegistry()

	return project
}

// buildTypeRegistry 构建全局类型注册表
func (p *Project) buildTypeRegistry() {
	for _, pkg := range p.Packages {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if genDecl, ok := decl.(*ast.GenDecl); ok {
					for _, spec := range genDecl.Specs {
						if typeSpec, ok := spec.(*ast.TypeSpec); ok {
							fullType := FullType{
								PackagePath: pkg.PkgPath,
								TypeName:    typeSpec.Name.Name,
							}
							p.TypeRegistry[fullType] = typeSpec
						}
					}
				}
			}
		}
	}
}

// GetTypeSpec 根据完整类型信息获取类型规范
func (p *Project) GetTypeSpec(fullType FullType) *ast.TypeSpec {
	return p.TypeRegistry[fullType]
}

// GetPackage 根据包路径获取包信息
func (p *Project) GetPackage(pkgPath string) *packages.Package {
	return p.PackageInfo[pkgPath]
}
