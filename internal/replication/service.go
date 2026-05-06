package replication

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Service struct {
	auditPath string
	metrics   *Metrics
	mu        sync.Mutex
}

func NewService(auditPath string) *Service {
	return &Service{
		auditPath: auditPath,
		metrics:   NewMetrics(),
	}
}

func (s *Service) Metrics() *Metrics {
	return s.metrics
}

func (s *Service) ValidateConfigFile(path string) (CommandResult, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		result := CommandResult{
			Action:    "validate",
			Status:    "error",
			Summary:   "Configuration validation failed",
			Error:     err.Error(),
			StartedAt: time.Now().UTC(),
		}
		result.Finalize()
		s.record(result)
		return result, err
	}

	result := NewCommandResult("validate", cfg)
	err = ValidateConfig(cfg)
	if err != nil {
		result.Status = "error"
		result.Summary = fmt.Sprintf("Configuration is invalid for cluster %q", cfg.ClusterName)
		result.Error = err.Error()
		result.Details = map[string]any{
			"config_path": path,
		}
		result.Finalize()
		s.record(result)
		return result, err
	}

	result.Summary = fmt.Sprintf("Configuration is valid for cluster %q", cfg.ClusterName)
	result.Details = map[string]any{
		"config_path": path,
		"mode":        cfg.Mode,
	}
	result.Finalize()
	s.record(result)
	return result, nil
}

func (s *Service) VerifyConfigFile(path string) (CommandResult, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		result := CommandResult{
			Action:    "verify",
			Status:    "error",
			Summary:   "Replication verification failed",
			Error:     err.Error(),
			StartedAt: time.Now().UTC(),
		}
		result.Finalize()
		s.record(result)
		return result, err
	}
	if err := ValidateConfig(cfg); err != nil {
		result := NewCommandResult("verify", cfg)
		result.Status = "error"
		result.Summary = "Replication verification failed"
		result.Error = err.Error()
		result.Finalize()
		s.record(result)
		return result, err
	}

	result, err := VerifyReplication(cfg)
	s.record(result)
	return result, err
}

func (s *Service) ProbeConfigFile(path string) (CommandResult, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		result := CommandResult{
			Action:    "probe",
			Status:    "error",
			Summary:   "Replication probe failed",
			Error:     err.Error(),
			StartedAt: time.Now().UTC(),
		}
		result.Finalize()
		s.record(result)
		return result, err
	}
	if err := ValidateConfig(cfg); err != nil {
		result := NewCommandResult("probe", cfg)
		result.Status = "error"
		result.Summary = "Replication probe failed"
		result.Error = err.Error()
		result.Finalize()
		s.record(result)
		return result, err
	}

	result, err := ProbeReplication(cfg)
	s.record(result)
	return result, err
}

func (s *Service) PromoteConfigFile(path string, opts OperationOptions) (CommandResult, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		result := CommandResult{
			Action:    "promote",
			Status:    "error",
			Summary:   "Promotion failed",
			Error:     err.Error(),
			StartedAt: time.Now().UTC(),
		}
		result.Finalize()
		s.record(result)
		return result, err
	}
	if err := ValidateConfig(cfg); err != nil {
		result := NewCommandResult("promote", cfg)
		result.Status = "error"
		result.Summary = "Promotion failed"
		result.Error = err.Error()
		result.Finalize()
		s.record(result)
		return result, err
	}
	result, err := PromoteStandby(cfg, opts)
	s.record(result)
	return result, err
}

func (s *Service) RejoinConfigFile(path string, opts OperationOptions) (CommandResult, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		result := CommandResult{
			Action:    "rejoin",
			Status:    "error",
			Summary:   "Rejoin failed",
			Error:     err.Error(),
			StartedAt: time.Now().UTC(),
		}
		result.Finalize()
		s.record(result)
		return result, err
	}
	if err := ValidateConfig(cfg); err != nil {
		result := NewCommandResult("rejoin", cfg)
		result.Status = "error"
		result.Summary = "Rejoin failed"
		result.Error = err.Error()
		result.Finalize()
		s.record(result)
		return result, err
	}
	result, err := RejoinOldPrimary(cfg, opts)
	s.record(result)
	return result, err
}

func (s *Service) Readiness(path string) (CommandResult, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		result := CommandResult{
			Action:    "readyz",
			Status:    "error",
			Summary:   "Service is not ready",
			Error:     err.Error(),
			StartedAt: time.Now().UTC(),
		}
		result.Finalize()
		return result, err
	}

	result := NewCommandResult("readyz", cfg)
	if err := ValidateConfig(cfg); err != nil {
		result.Status = "error"
		result.Summary = "Service is not ready"
		result.Error = err.Error()
		result.Finalize()
		return result, err
	}

	result.Summary = "Service is ready"
	result.Details = map[string]any{
		"cluster": cfg.ClusterName,
		"mode":    cfg.Mode,
	}
	result.Finalize()
	return result, nil
}

func (s *Service) History(limit int) ([]CommandResult, error) {
	if s.auditPath == "" {
		return nil, fmt.Errorf("audit log path is not configured")
	}
	if limit <= 0 {
		limit = 20
	}

	f, err := os.Open(s.auditPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	results, err := decodeTailAuditLog(f, limit)
	if err != nil {
		return nil, err
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].StartedAt.Before(results[j].StartedAt)
	})
	return results, nil
}

func (s *Service) record(result CommandResult) {
	s.metrics.Observe(result.Action, result.Status, result.DurationMS)
	if s.auditPath == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.auditPath), 0o755); err != nil {
		log.Printf("replicon: failed to create audit directory: %v", err)
		return
	}
	f, err := os.OpenFile(s.auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("replicon: failed to open audit log: %v", err)
		return
	}
	defer f.Close()

	payload, err := json.Marshal(result)
	if err != nil {
		log.Printf("replicon: failed to marshal audit entry: %v", err)
		return
	}

	if _, err := f.Write(append(payload, '\n')); err != nil {
		log.Printf("replicon: failed to write audit entry: %v", err)
	}
}

// decodeTailAuditLog reads a JSONL audit log and returns the last limit
// entries. It uses a ring buffer so that only limit entries are held in memory
// regardless of file size.
func decodeTailAuditLog(r io.Reader, limit int) ([]CommandResult, error) {
	scanner := bufio.NewScanner(r)
	ring := make([]CommandResult, 0, limit)
	pos := 0
	full := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var result CommandResult
		if err := json.Unmarshal(line, &result); err != nil {
			return nil, err
		}
		if !full {
			ring = append(ring, result)
			if len(ring) == limit {
				full = true
			}
		} else {
			ring[pos] = result
		}
		pos = (pos + 1) % limit
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !full {
		return ring, nil
	}
	// Unwind the ring buffer into order.
	out := make([]CommandResult, limit)
	for i := range limit {
		out[i] = ring[(pos+i)%limit]
	}
	return out, nil
}

// decodeAuditLog reads all entries from a JSONL audit log. Used by tests.
func decodeAuditLog(r io.Reader) ([]CommandResult, error) {
	scanner := bufio.NewScanner(r)
	results := make([]CommandResult, 0)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var result CommandResult
		if err := json.Unmarshal(line, &result); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return results, nil
}
