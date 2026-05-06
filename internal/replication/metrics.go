package replication

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Metrics struct {
	mu           sync.Mutex
	counters     map[string]int64
	durationSums map[string]int64
}

func NewMetrics() *Metrics {
	return &Metrics{
		counters:     make(map[string]int64),
		durationSums: make(map[string]int64),
	}
}

func (m *Metrics) Observe(action, status string, durationMS int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := fmt.Sprintf("%s:%s", action, status)
	m.counters[key]++
	m.durationSums[action] += durationMS
}

func (m *Metrics) RenderPrometheus() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var lines []string
	lines = append(lines,
		"# HELP replicon_command_runs_total Total number of command runs by action and status.",
		"# TYPE replicon_command_runs_total counter",
	)

	keys := make([]string, 0, len(m.counters))
	for key := range m.counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.SplitN(key, ":", 2)
		lines = append(lines, fmt.Sprintf(`replicon_command_runs_total{action=%q,status=%q} %d`, parts[0], parts[1], m.counters[key]))
	}

	lines = append(lines,
		"# HELP replicon_command_duration_ms_total Sum of command durations in milliseconds by action.",
		"# TYPE replicon_command_duration_ms_total counter",
	)

	actions := make([]string, 0, len(m.durationSums))
	for action := range m.durationSums {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	for _, action := range actions {
		lines = append(lines, fmt.Sprintf(`replicon_command_duration_ms_total{action=%q} %d`, action, m.durationSums[action]))
	}

	lines = append(lines, "")
	return strings.Join(lines, "\n")
}
