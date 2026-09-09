package contextx

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Manager собирает контекст из всех провайдеров.
// Просто существует — не болтает.
type Manager struct {
	mu        sync.RWMutex
	providers map[string]Provider
	order     []string // для детерминизма
}

func NewManager() *Manager {
	return &Manager{providers: make(map[string]Provider)}
}

func (m *Manager) Register(p Provider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providers[p.Name()] = p
}

func (m *Manager) List() []Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Provider, 0, len(m.providers))
	for _, p := range m.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Priority() < out[j].Priority() })
	return out
}

// Build собирает итоговый системный контекст.
// Каждый провайдер отдаёт строку; пустые пропускаются.
func (m *Manager) Build(ctx context.Context, req *Request) string {
	m.mu.RLock()
	provs := make([]Provider, 0, len(m.providers))
	for _, p := range m.providers {
		provs = append(provs, p)
	}
	m.mu.RUnlock()
	sort.Slice(provs, func(i, j int) bool { return provs[i].Priority() < provs[j].Priority() })

	var parts []string
	for _, p := range provs {
		if !p.Enabled() {
			continue
		}
		s, err := p.Provide(ctx, req)
		if err != nil || strings.TrimSpace(s) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(s))
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// Window — простая обрезка по токенам (оценка 4 символа/токен).
// Держит последние N токенов + всегда system.
func Window(turns []Turn, maxTokens int, estimator func(string) int) []Turn {
	if maxTokens <= 0 {
		return turns
	}
	total := 0
	// идём с конца
	var kept []Turn
	for i := len(turns) - 1; i >= 0; i-- {
		t := estimator(turns[i].Text)
		if total+t > maxTokens {
			break
		}
		total += t
		kept = append(kept, turns[i])
	}
	// reverse
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return kept
}
