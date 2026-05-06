package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"replicon/internal/replication"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return fmt.Errorf("missing command")
	}

	switch args[0] {
	case "init":
		return runInit(args[1:], stdout)
	case "validate":
		return runValidate(args[1:], stdout)
	case "plan":
		return runPlan(args[1:], stdout)
	case "render":
		return runRender(args[1:], stdout)
	case "verify":
		return runVerify(args[1:], stdout)
	case "probe":
		return runProbe(args[1:], stdout)
	case "promote":
		return runPromote(args[1:], stdout)
	case "rejoin":
		return runRejoin(args[1:], stdout)
	case "preflight":
		return runPreflight(args[1:], stdout)
	case "watch":
		return runWatch(args[1:], stdout)
	case "ddl-setup":
		return runDDLSetup(args[1:], stdout)
	case "ddl-sync":
		return runDDLSync(args[1:], stdout)
	case "conflicts":
		return runConflicts(args[1:], stdout)
	case "history":
		return runHistory(args[1:], stdout)
	case "serve":
		return runServe(args[1:], stdout)
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "replicon %s (commit %s, built %s)\n", version, commit, date)
		return nil
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runInit(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	mode := fs.String("mode", "master-slave", "replication mode: master-slave or master-master")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	sample, err := replication.SampleConfig(*mode)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, sample)
	return nil
}

func runValidate(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	output := fs.String("output", "text", "output format: text or json")
	auditLog := fs.String("audit-log", "", "path to JSONL audit log")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	service := replication.NewService(*auditLog)
	result, err := service.ValidateConfigFile(*configPath)
	return writeResult(stdout, result, *output, err)
}

func runPlan(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := replication.LoadConfig(*configPath)
	if err != nil {
		return err
	}

	if err := replication.ValidateConfig(cfg); err != nil {
		return err
	}

	fmt.Fprint(stdout, replication.RenderPlan(cfg))
	return nil
}

func runRender(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	target := fs.String("target", "", "render target: primary or standby")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := replication.LoadConfig(*configPath)
	if err != nil {
		return err
	}

	if err := replication.ValidateConfig(cfg); err != nil {
		return err
	}

	output, err := replication.RenderTarget(cfg, *target)
	if err != nil {
		return err
	}

	fmt.Fprint(stdout, output)
	return nil
}

func runVerify(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	output := fs.String("output", "text", "output format: text or json")
	auditLog := fs.String("audit-log", "", "path to JSONL audit log")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	service := replication.NewService(*auditLog)
	result, err := service.VerifyConfigFile(*configPath)
	return writeResult(stdout, result, *output, err)
}

func runProbe(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	output := fs.String("output", "text", "output format: text or json")
	auditLog := fs.String("audit-log", "", "path to JSONL audit log")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	service := replication.NewService(*auditLog)
	result, err := service.ProbeConfigFile(*configPath)
	return writeResult(stdout, result, *output, err)
}

func runServe(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	listenAddr := fs.String("listen", ":8080", "HTTP listen address")
	auditLog := fs.String("audit-log", "var/audit/replicon.jsonl", "path to JSONL audit log")
	apiKeyEnv := fs.String("api-key-env", "REPLICON_API_KEY", "environment variable containing the admin API key")
	tlsCert := fs.String("tls-cert", "", "path to TLS certificate PEM file")
	tlsKey := fs.String("tls-key", "", "path to TLS private key PEM file")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*configPath) == "" {
		return fmt.Errorf("config path is required")
	}
	if strings.TrimSpace(*tlsCert) == "" || strings.TrimSpace(*tlsKey) == "" {
		return fmt.Errorf("tls-cert and tls-key are required")
	}
	apiKey := strings.TrimSpace(os.Getenv(*apiKeyEnv))
	if apiKey == "" {
		return fmt.Errorf("api key environment variable %q is not set", *apiKeyEnv)
	}

	service := replication.NewService(*auditLog)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		result, err := service.Readiness(*configPath)
		statusCode := http.StatusOK
		if err != nil {
			statusCode = http.StatusServiceUnavailable
		}
		if writeErr := writeJSONResponse(w, statusCode, result.Sanitized()); writeErr != nil {
			http.Error(w, writeErr.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, service.Metrics().RenderPrometheus())
	})
	mux.HandleFunc("/api/v1/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := 20
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			var parsed int
			if _, err := fmt.Sscanf(raw, "%d", &parsed); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		results, err := service.History(limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sanitized := make([]replication.CommandResult, 0, len(results))
		for _, result := range results {
			sanitized = append(sanitized, result.Sanitized())
		}
		if writeErr := writeJSONResponse(w, http.StatusOK, sanitized); writeErr != nil {
			http.Error(w, writeErr.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/api/v1/validate", commandHandler(service.ValidateConfigFile, *configPath))
	mux.HandleFunc("/api/v1/verify", commandHandler(service.VerifyConfigFile, *configPath))
	mux.HandleFunc("/api/v1/probe", commandHandler(service.ProbeConfigFile, *configPath))
	mux.HandleFunc("/api/v1/promote", operationHandler(func() (replication.CommandResult, error) {
		cfg, err := replication.LoadConfig(*configPath)
		if err == nil {
			if _, err = replication.CheckSSHConnectivity(cfg, 0); err != nil {
				result := replication.CommandResult{
					Action:    "promote",
					Status:    "error",
					Summary:   "Preflight check failed",
					Error:     err.Error(),
					StartedAt: time.Now().UTC(),
				}
				result.Finalize()
				return result, err
			}
		}
		return service.PromoteConfigFile(*configPath, replication.OperationOptions{Execute: true})
	}))
	mux.HandleFunc("/api/v1/rejoin", operationHandler(func() (replication.CommandResult, error) {
		cfg, err := replication.LoadConfig(*configPath)
		if err == nil {
			if _, err = replication.CheckSSHConnectivity(cfg, 0); err != nil {
				result := replication.CommandResult{
					Action:    "rejoin",
					Status:    "error",
					Summary:   "Preflight check failed",
					Error:     err.Error(),
					StartedAt: time.Now().UTC(),
				}
				result.Finalize()
				return result, err
			}
		}
		return service.RejoinConfigFile(*configPath, replication.OperationOptions{Execute: true})
	}))

	fmt.Fprintf(stdout, "replicon API listening on %s\n", *listenAddr)
	server := &http.Server{
		Addr:         *listenAddr,
		Handler:      withAPIKeyAuth(apiKey, mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServeTLS(*tlsCert, *tlsKey)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		fmt.Fprintf(stdout, "received %s, shutting down\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("graceful shutdown failed: %w", err)
		}
		fmt.Fprintln(stdout, "shutdown complete")
		return nil
	}
}

func runPreflight(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	output := fs.String("output", "text", "output format: text or json")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := replication.LoadConfig(*configPath)
	if err != nil {
		return err
	}

	results, checkErr := replication.CheckSSHConnectivity(cfg, 0)

	switch strings.ToLower(strings.TrimSpace(*output)) {
	case "json":
		payload, marshalErr := json.MarshalIndent(results, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		_, _ = fmt.Fprintln(stdout, string(payload))
	case "", "text":
		for _, pr := range results {
			status := "OK"
			if !pr.Reachable {
				status = "FAIL"
			}
			_, _ = fmt.Fprintf(stdout, "  %s (%s): %s", pr.Node, pr.Host, status)
			if pr.Error != "" {
				_, _ = fmt.Fprintf(stdout, " — %s", pr.Error)
			}
			_, _ = fmt.Fprintln(stdout)
		}
		if checkErr == nil {
			_, _ = fmt.Fprintln(stdout, "SSH preflight: PASS")
		} else {
			_, _ = fmt.Fprintln(stdout, "SSH preflight: FAIL")
		}
	}
	return checkErr
}

func runWatch(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	auditLog := fs.String("audit-log", "", "path to JSONL audit log")
	dryRun := fs.Bool("dry-run", false, "monitor and log events without executing failover")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := replication.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if err := replication.ValidateConfig(cfg); err != nil {
		return err
	}
	if !cfg.Failover.Enabled {
		return fmt.Errorf("failover.enabled is not set in config — add a failover section to enable automatic failover")
	}

	if *dryRun {
		fmt.Fprintln(stdout, "dry-run mode: will monitor and log but will NOT fence or promote")
	}

	// Pre-validate SSH before entering the watch loop (skip in dry-run).
	if !*dryRun {
		fmt.Fprintln(stdout, "running SSH preflight check...")
		if _, err := replication.CheckSSHConnectivity(cfg, 0); err != nil {
			return fmt.Errorf("preflight check failed: %w", err)
		}
		fmt.Fprintln(stdout, "SSH preflight: PASS")
	}

	// Pre-validate primary connectivity.
	fmt.Fprintln(stdout, "verifying primary is reachable...")
	if _, verifyErr := replication.VerifyReplication(cfg); verifyErr != nil {
		return fmt.Errorf("initial replication verify failed: %w", verifyErr)
	}
	fmt.Fprintln(stdout, "replication verify: PASS")

	service := replication.NewService(*auditLog)
	f := replication.WatchdogDefaults(cfg.Failover)

	standbys := cfg.ResolveStandbys()
	standbyNames := make([]string, len(standbys))
	for i, s := range standbys {
		standbyNames[i] = s.Name
	}

	fmt.Fprintf(stdout, "watchdog active: cluster=%s primary=%s standbys=[%s] interval=%ds threshold=%d dry-run=%t\n",
		cfg.ClusterName, cfg.Primary.Name, strings.Join(standbyNames, ", "), f.CheckIntervalSec, f.MaxFailures, *dryRun)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-quit
		fmt.Fprintf(stdout, "\nreceived %s, stopping watchdog\n", sig)
		cancel()
	}()

	callbacks := replication.WatchdogCallbacks{
		OnEvent: func(e replication.WatchdogEvent) {
			fmt.Fprintf(stdout, "%s [%s] %s\n", e.Time.Format(time.RFC3339), e.Type, e.Message)
		},
		DryRun: *dryRun,
	}

	err = replication.RunWatchdog(ctx, cfg, service, callbacks)
	if err != nil && err != context.Canceled {
		fmt.Fprintf(stdout, "watchdog exited: %s\n", err)
		return err
	}
	return nil
}

func runDDLSetup(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ddl-setup", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	output := fs.String("output", "text", "output format: text or json")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := replication.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if err := replication.ValidateConfig(cfg); err != nil {
		return err
	}

	result, err := replication.SetupDDLTrackingAll(cfg)
	return writeResult(stdout, result, *output, err)
}

func runDDLSync(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ddl-sync", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	output := fs.String("output", "text", "output format: text or json")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := replication.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if err := replication.ValidateConfig(cfg); err != nil {
		return err
	}

	result, err := replication.SyncDDL(cfg)
	return writeResult(stdout, result, *output, err)
}

func runConflicts(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("conflicts", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	output := fs.String("output", "text", "output format: text or json")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := replication.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	if err := replication.ValidateConfig(cfg); err != nil {
		return err
	}

	result, err := replication.CheckConflicts(cfg)
	return writeResult(stdout, result, *output, err)
}

func runPromote(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	output := fs.String("output", "text", "output format: text or json")
	auditLog := fs.String("audit-log", "", "path to JSONL audit log")
	execute := fs.Bool("execute", false, "run remote commands over SSH instead of returning a dry run")
	skipPreflight := fs.Bool("skip-preflight", false, "skip SSH connectivity check before execution")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *execute && !*skipPreflight {
		cfg, err := replication.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		if _, err := replication.CheckSSHConnectivity(cfg, 0); err != nil {
			return fmt.Errorf("preflight check failed: %w", err)
		}
		fmt.Fprintln(stdout, "SSH preflight: PASS")
	}

	service := replication.NewService(*auditLog)
	result, err := service.PromoteConfigFile(*configPath, replication.OperationOptions{Execute: *execute})
	return writeResult(stdout, result, *output, err)
}

func runRejoin(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rejoin", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON configuration")
	output := fs.String("output", "text", "output format: text or json")
	auditLog := fs.String("audit-log", "", "path to JSONL audit log")
	execute := fs.Bool("execute", false, "run remote commands over SSH instead of returning a dry run")
	skipPreflight := fs.Bool("skip-preflight", false, "skip SSH connectivity check before execution")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *execute && !*skipPreflight {
		cfg, err := replication.LoadConfig(*configPath)
		if err != nil {
			return err
		}
		if _, err := replication.CheckSSHConnectivity(cfg, 0); err != nil {
			return fmt.Errorf("preflight check failed: %w", err)
		}
		fmt.Fprintln(stdout, "SSH preflight: PASS")
	}

	service := replication.NewService(*auditLog)
	result, err := service.RejoinConfigFile(*configPath, replication.OperationOptions{Execute: *execute})
	return writeResult(stdout, result, *output, err)
}

func runHistory(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	auditLog := fs.String("audit-log", "var/audit/replicon.jsonl", "path to JSONL audit log")
	output := fs.String("output", "text", "output format: text or json")
	limit := fs.Int("limit", 20, "maximum number of entries to show")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}

	service := replication.NewService(*auditLog)
	results, err := service.History(*limit)
	if err != nil {
		return err
	}

	switch strings.ToLower(strings.TrimSpace(*output)) {
	case "json":
		sanitized := make([]replication.CommandResult, 0, len(results))
		for _, result := range results {
			sanitized = append(sanitized, result.Sanitized())
		}
		payload, marshalErr := json.MarshalIndent(sanitized, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		_, _ = fmt.Fprintln(stdout, string(payload))
		return nil
	case "", "text":
		for _, result := range results {
			result = result.Sanitized()
			_, _ = fmt.Fprintf(stdout, "%s %s %s %s\n",
				result.StartedAt.Format(time.RFC3339),
				result.Action,
				result.Status,
				result.Text(),
			)
		}
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", *output)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `replicon helps prepare and verify two-node PostgreSQL replication.

Usage:
  replicon init [-mode master-slave|master-master]
  replicon validate -config replicon.json [-output text|json] [-audit-log path]
  replicon plan -config replicon.json
  replicon render -config replicon.json -target primary
  replicon render -config replicon.json -target standby
  replicon render -config replicon.json -target node-a
  replicon render -config replicon.json -target node-b
  replicon verify -config replicon.json [-output text|json] [-audit-log path]
  replicon probe -config replicon.json [-output text|json] [-audit-log path]
  replicon promote -config replicon.json [-execute] [-skip-preflight] [-output text|json] [-audit-log path]
  replicon rejoin -config replicon.json [-execute] [-skip-preflight] [-output text|json] [-audit-log path]
  replicon preflight -config replicon.json [-output text|json]
  replicon watch -config replicon.json [-audit-log path] [-dry-run]
  replicon ddl-setup -config replicon.json [-output text|json]
  replicon ddl-sync -config replicon.json [-output text|json]
  replicon conflicts -config replicon.json [-output text|json]
  replicon history [-audit-log path] [-limit 20] [-output text|json]
  replicon serve -config replicon.json -tls-cert server.crt -tls-key server.key [-listen :8080] [-audit-log path]
  replicon version`)
}

func writeResult(w io.Writer, result replication.CommandResult, output string, err error) error {
	switch strings.ToLower(strings.TrimSpace(output)) {
	case "json":
		payload, marshalErr := result.JSON()
		if marshalErr != nil {
			return marshalErr
		}
		_, _ = fmt.Fprintln(w, string(payload))
	case "", "text":
		_, _ = fmt.Fprintln(w, result.Text())
	default:
		return fmt.Errorf("unsupported output format %q", output)
	}
	return err
}

func commandHandler(fn func(string) (replication.CommandResult, error), configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		result, err := fn(configPath)
		statusCode := http.StatusOK
		if err != nil {
			statusCode = http.StatusBadRequest
		}
		if writeErr := writeJSONResponse(w, statusCode, result.Sanitized()); writeErr != nil {
			http.Error(w, writeErr.Error(), http.StatusInternalServerError)
		}
	}
}

func operationHandler(fn func() (replication.CommandResult, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		result, err := fn()
		statusCode := http.StatusOK
		if err != nil {
			statusCode = http.StatusBadRequest
		}
		if writeErr := writeJSONResponse(w, statusCode, result.Sanitized()); writeErr != nil {
			http.Error(w, writeErr.Error(), http.StatusInternalServerError)
		}
	}
}

func writeJSONResponse(w http.ResponseWriter, statusCode int, payload any) error {
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, err = w.Write(append(body, '\n'))
	return err
}

func withAPIKeyAuth(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		token := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if token == "" {
			authz := strings.TrimSpace(r.Header.Get("Authorization"))
			if strings.HasPrefix(authz, "Bearer ") {
				token = strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
			}
		}

		if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
