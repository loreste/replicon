package replication

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type CommandResult struct {
	Action     string         `json:"action"`
	Cluster    string         `json:"cluster,omitempty"`
	Mode       string         `json:"mode,omitempty"`
	Status     string         `json:"status"`
	Summary    string         `json:"summary"`
	Error      string         `json:"error,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at"`
	DurationMS int64          `json:"duration_ms"`
	Details    map[string]any `json:"details,omitempty"`
}

func NewCommandResult(action string, cfg Config) CommandResult {
	return CommandResult{
		Action:    action,
		Cluster:   cfg.ClusterName,
		Mode:      cfg.Mode,
		Status:    "ok",
		StartedAt: time.Now().UTC(),
	}
}

func (r *CommandResult) Finalize() {
	r.FinishedAt = time.Now().UTC()
	r.DurationMS = r.FinishedAt.Sub(r.StartedAt).Milliseconds()
	if r.DurationMS < 0 {
		r.DurationMS = 0
	}
}

func (r CommandResult) Text() string {
	if strings.TrimSpace(r.Summary) != "" {
		return r.Summary
	}
	if r.Status == "ok" {
		return fmt.Sprintf("%s completed successfully", r.Action)
	}
	if r.Error != "" {
		return r.Error
	}
	return fmt.Sprintf("%s failed", r.Action)
}

func (r CommandResult) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func (r CommandResult) Sanitized() CommandResult {
	r.Details = sanitizeMap(r.Details)
	r.Error = sanitizeString(r.Error)
	r.Summary = sanitizeString(r.Summary)
	return r
}

func sanitizeMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}

	out := make(map[string]any, len(value))
	for key, raw := range value {
		lowerKey := strings.ToLower(key)
		if strings.Contains(lowerKey, "dsn") || strings.Contains(lowerKey, "password") {
			out[key] = "[redacted]"
			continue
		}
		out[key] = sanitizeValue(raw)
	}
	return out
}

func sanitizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeMap(typed)
	case string:
		return sanitizeString(typed)
	case []any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, sanitizeValue(item))
		}
		return items
	default:
		return value
	}
}

func sanitizeString(value string) string {
	value = strings.ReplaceAll(value, "postgres://", "[redacted-dsn]://")
	if strings.Contains(strings.ToLower(value), "password=") {
		return redactPasswordFragment(value)
	}
	return value
}

func redactPasswordFragment(value string) string {
	parts := strings.Split(value, "password=")
	if len(parts) < 2 {
		return value
	}
	tail := parts[1]
	end := strings.IndexAny(tail, " \n\t'\"")
	if end == -1 {
		return parts[0] + "password=[redacted]"
	}
	return parts[0] + "password=[redacted]" + tail[end:]
}
