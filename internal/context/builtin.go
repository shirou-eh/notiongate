package contextx

var DefaultManager = NewManager()

func init() {
	// Все провайдеры включены — но каждый САМ проверяет нужен ли он.
	// System — всегда если есть system, Time/Workspace/Tools — только если в контексте есть триггеры.
	DefaultManager.Register(&SystemProvider{EnabledFlag: true})
	DefaultManager.Register(&TimeProvider{EnabledFlag: true})
	DefaultManager.Register(&WorkspaceProvider{EnabledFlag: true})
	DefaultManager.Register(&ToolsHintProvider{EnabledFlag: true})
}
