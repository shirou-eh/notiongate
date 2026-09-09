package contextx

var DefaultManager = NewManager()

func init() {
	DefaultManager.Register(&SystemProvider{EnabledFlag: true})
	DefaultManager.Register(&TimeProvider{EnabledFlag: true})
	DefaultManager.Register(&WorkspaceProvider{EnabledFlag: true})
	DefaultManager.Register(&ToolsHintProvider{EnabledFlag: true})
}
