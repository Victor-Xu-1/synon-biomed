package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/config"
	"synon-go/internal/server"
)

const modelSmokePrompt = "Reply with a short confirmation that the Synon model protocol smoke request was processed."

type modelSmokeTarget struct {
	Name                 string `json:"name"`
	Provider             string `json:"provider"`
	Status               string `json:"status"`
	Configured           bool   `json:"configured"`
	CredentialConfigured bool   `json:"credentialConfigured"`
	RequiresNetwork      bool   `json:"requiresNetwork"`
	LiveChecked          bool   `json:"liveChecked"`
	ResponseNonEmpty     bool   `json:"responseNonEmpty,omitempty"`
	FailureKind          string `json:"failureKind,omitempty"`
	Message              string `json:"message,omitempty"`

	options server.SessionRunnerChatOptions
}

type modelSmokeReport struct {
	Status          string             `json:"status"`
	Mode            string             `json:"mode"`
	Targets         []modelSmokeTarget `json:"targets"`
	SecretsRedacted bool               `json:"secretsRedacted"`
}

func runModelSmokeCLI(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("model-smoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "Synon JSON config path")
	target := flags.String("target", "all", "runner, compact, or all")
	planOnly := flags.Bool("plan", false, "report readiness without model requests")
	runLive := flags.Bool("run", false, "perform configured model requests")
	requireAll := flags.Bool("require-all", false, "fail a live run when any selected target is unavailable")
	timeout := flags.Duration("timeout", 30*time.Second, "per-request timeout")
	maxAttempts := flags.Int("max-attempts", 1, "bounded attempts per target")
	_ = flags.Bool("json", false, "write JSON output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *planOnly && *runLive {
		return errors.New("--plan and --run are mutually exclusive")
	}
	selected := strings.ToLower(strings.TrimSpace(*target))
	if selected != "all" && selected != "runner" && selected != "compact" {
		return errors.New("--target must be runner, compact, or all")
	}
	if *timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if *maxAttempts < 1 || *maxAttempts > 5 {
		return errors.New("--max-attempts must be between 1 and 5")
	}
	restoreConfig, err := applyModelSmokeConfigPath(*configPath)
	if err != nil {
		return err
	}
	defer restoreConfig()
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load model smoke config: %w", err)
	}
	targets, err := modelSmokeTargets(cfg, selected, *timeout, *maxAttempts)
	if err != nil {
		return err
	}
	report := modelSmokeReport{Status: "planned", Mode: "plan", Targets: targets, SecretsRedacted: true}
	if *runLive {
		report.Mode = "run"
		report.Targets = runModelSmokeTargets(ctx, targets)
		report.Status = modelSmokeReportStatus(report.Targets)
	}
	if err := writeMigrationJSON(output, report); err != nil {
		return err
	}
	if *runLive && modelSmokeRunFailed(report.Targets, *requireAll) {
		return errors.New("model smoke did not pass for all required targets")
	}
	return nil
}

func applyModelSmokeConfigPath(path string) (func(), error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return func() {}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("model smoke config: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("model smoke config must be a regular file")
	}
	previous, existed := os.LookupEnv("SYNON_CONFIG")
	if err := os.Setenv("SYNON_CONFIG", path); err != nil {
		return nil, err
	}
	return func() {
		if existed {
			_ = os.Setenv("SYNON_CONFIG", previous)
		} else {
			_ = os.Unsetenv("SYNON_CONFIG")
		}
	}, nil
}

func modelSmokeTargets(cfg config.Config, selected string, timeout time.Duration, maxAttempts int) ([]modelSmokeTarget, error) {
	targets := make([]modelSmokeTarget, 0, 2)
	if selected == "all" || selected == "runner" {
		options, enabled, err := newSessionRunnerChatOptions(cfg)
		if err != nil {
			return nil, fmt.Errorf("runner model smoke: %w", err)
		}
		provider := sessionRunnerProvider(cfg.Runner)
		configured := enabled && !options.RequireSavedModel
		targets = append(targets, newModelSmokeTarget("runner", provider, options, configured, timeout, maxAttempts))
	}
	if selected == "all" || selected == "compact" {
		options := newCompactSummarizerOptions(cfg)
		targets = append(targets, newModelSmokeTarget("compact", "openai_compatible", options, options.Endpoint != "" && options.Model != "", timeout, maxAttempts))
	}
	return targets, nil
}

func newModelSmokeTarget(name, provider string, options server.SessionRunnerChatOptions, configured bool, timeout time.Duration, maxAttempts int) modelSmokeTarget {
	options.RequestTimeout = timeout
	options.MaxAttempts = maxAttempts
	builtin := options.Endpoint == server.BuiltinSessionRunnerChatEndpoint
	target := modelSmokeTarget{
		Name: name, Provider: provider, Configured: configured,
		CredentialConfigured: strings.TrimSpace(options.APIKey) != "",
		RequiresNetwork:      configured && !builtin,
		Status:               "skipped_missing_config",
		options:              options,
	}
	if configured {
		target.Status = "planned"
	}
	return target
}

func runModelSmokeTargets(ctx context.Context, targets []modelSmokeTarget) []modelSmokeTarget {
	results := make([]modelSmokeTarget, len(targets))
	for index := range targets {
		results[index] = runModelSmokeTarget(ctx, targets[index])
	}
	return results
}

func runModelSmokeTarget(ctx context.Context, target modelSmokeTarget) modelSmokeTarget {
	if !target.Configured {
		return target
	}
	if target.options.Endpoint == server.BuiltinSessionRunnerChatEndpoint {
		content, err := server.BuiltinSessionRunnerSmoke(modelSmokePrompt)
		return finishModelSmokeTarget(target, content, err)
	}
	parsed, err := url.Parse(target.options.Endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		target.Status = "failed"
		target.LiveChecked = true
		target.FailureKind = "invalid_endpoint"
		target.Message = "configured endpoint must be an absolute HTTP or HTTPS URL"
		return target
	}
	client := agentruntime.OpenAIChatClient{
		Endpoint: target.options.Endpoint, APIKey: target.options.APIKey, Model: target.options.Model,
		HTTPClient: &http.Client{Timeout: target.options.RequestTimeout}, RequestTimeout: target.options.RequestTimeout,
		MaxAttempts: target.options.MaxAttempts,
	}
	response, err := client.Complete(ctx, agentruntime.ModelRequest{Messages: []agentruntime.Message{{Role: "user", Content: modelSmokePrompt}}})
	return finishModelSmokeTarget(target, response.Message.Content, err)
}

func finishModelSmokeTarget(target modelSmokeTarget, content string, err error) modelSmokeTarget {
	target.LiveChecked = true
	if err != nil {
		target.Status = "failed"
		target.FailureKind = "request_failed"
		target.Message = "configured model request failed; inspect runtime logs without exposing credentials"
		return target
	}
	target.ResponseNonEmpty = strings.TrimSpace(content) != ""
	if !target.ResponseNonEmpty {
		target.Status = "failed"
		target.FailureKind = "empty_response"
		target.Message = "model returned no assistant text"
		return target
	}
	target.Status = "pass"
	return target
}

func modelSmokeReportStatus(targets []modelSmokeTarget) string {
	passed := 0
	failed := 0
	for _, target := range targets {
		switch target.Status {
		case "pass":
			passed++
		case "failed":
			failed++
		}
	}
	if failed > 0 {
		return "failed"
	}
	if passed == 0 {
		return "skipped"
	}
	return "passed"
}

func modelSmokeRunFailed(targets []modelSmokeTarget, requireAll bool) bool {
	passed := 0
	for _, target := range targets {
		if target.Status == "failed" || (requireAll && target.Status != "pass") {
			return true
		}
		if target.Status == "pass" {
			passed++
		}
	}
	return passed == 0
}
