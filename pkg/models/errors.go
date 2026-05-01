package models

import "fmt"

type ParseError struct {
	Path   string
	Reason string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("解析项目 %q 失败: %s", e.Path, e.Reason)
}

type AnalysisError struct {
	Context string
	Reason  string
}

func (e *AnalysisError) Error() string {
	return fmt.Sprintf("分析失败 [%s]: %s", e.Context, e.Reason)
}
