// 文件位置: pkg/models/errors.go
package models

import "fmt"

// ParseError 表示项目解析过程中的错误
type ParseError struct {
	Path   string
	Reason string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("解析项目 '%s' 失败: %s", e.Path, e.Reason)
}

// AnalysisError 表示分析过程中的错误
type AnalysisError struct {
	Context string
	Reason  string
}

func (e *AnalysisError) Error() string {
	return fmt.Sprintf("分析失败 [%s]: %s", e.Context, e.Reason)
}

// ExtractorError 表示框架提取器的错误
type ExtractorError struct {
	Framework string
	Operation string
	Reason    string
}

func (e *ExtractorError) Error() string {
	return fmt.Sprintf("框架 '%s' 提取器在 '%s' 操作中失败: %s", e.Framework, e.Operation, e.Reason)
}
