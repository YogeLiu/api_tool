// 文件位置: pkg/analyzer/internal_types.go
package analyzer

import (
	"go/ast"
	"go/types"
)

// AnalysisTask 表示分析队列中的一个任务
type AnalysisTask struct {
	RouterObject    types.Object          // 路由器对象 (如 *gin.Engine 或 gin.RouterGroup)
	AccumulatedPath string                // 当前累积的路径前缀
	Parent          *AnalysisTask         // 父任务，用于循环检测
	TriggeringFunc  *ast.FuncDecl         // 触发此任务的函数声明
	VisitedObjects  map[types.Object]bool // 用于循环检测的已访问对象集合
}

// HasCycle 检查当前任务是否会导致循环
func (task *AnalysisTask) HasCycle(obj types.Object) bool {
	if task.VisitedObjects == nil {
		task.VisitedObjects = make(map[types.Object]bool)
	}

	if task.VisitedObjects[obj] {
		return true
	}

	// 向上检查父任务链
	current := task.Parent
	for current != nil {
		if current.RouterObject == obj {
			return true
		}
		current = current.Parent
	}

	return false
}

// AddVisited 将对象添加到已访问集合
func (task *AnalysisTask) AddVisited(obj types.Object) {
	if task.VisitedObjects == nil {
		task.VisitedObjects = make(map[types.Object]bool)
	}
	task.VisitedObjects[obj] = true
}
