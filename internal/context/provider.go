// Package contextx — система контекста notiongate.
// Каждый провайдер живёт отдельно, просто существует и отдаёт свой кусок
// контекста. Менеджер собирает их молча.
package contextx

import (
	"context"
)

// Provider отдаёт кусок контекста для текущего запроса.
// Имя — ключ для включения/выключения, Приоритет — порядок вставки.
type Provider interface {
	Name() string
	Priority() int // меньше = выше
	Provide(ctx context.Context, req *Request) (string, error)
	Enabled() bool
}

// Request — то, что видит провайдер.
type Request struct {
	UserKey string
	Model   string
	System  []string
	Turns   []Turn
	Tools   []ToolRef
}

type Turn struct {
	Role string
	Text string
}

type ToolRef struct {
	Name string
	Desc string
}
