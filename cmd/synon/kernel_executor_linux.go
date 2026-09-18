//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/kernel/detached"
	workspace "synon-go/internal/persistence/workspace"
)

func runKernelExecutorCLI(args []string) error {
	flags := flag.NewFlagSet("synon-biomed kernel-executor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	home := flags.String("home", "", "Synon Biomed data directory")
	backendID := flags.String("backend-id", "", "durable backend identity")
	backendGeneration := flags.Int64("backend-generation", 0, "durable backend generation")
	executorInstanceID := flags.String("executor-instance-id", "", "executor instance identity")
	socketPath := flags.String("socket", "", "private Unix control socket")
	condaHome := flags.String("conda-home", "", "managed Conda home")
	condaEnvsPath := flags.String("conda-envs-path", "", "managed Conda environments")
	heartbeat := flags.Duration("heartbeat", 10*time.Second, "durable executor heartbeat interval")
	idleTimeout := flags.Duration("idle-timeout", 15*time.Minute, "idle executor lifetime after the last request")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected kernel executor arguments: %s", strings.Join(flags.Args(), " "))
	}
	*home = filepath.Clean(strings.TrimSpace(*home))
	*backendID = strings.TrimSpace(*backendID)
	*executorInstanceID = strings.TrimSpace(*executorInstanceID)
	*socketPath = filepath.Clean(strings.TrimSpace(*socketPath))
	if !filepath.IsAbs(*home) || !filepath.IsAbs(*socketPath) || *backendID == "" ||
		*backendGeneration <= 0 || *executorInstanceID == "" {
		return errors.New("kernel executor requires complete absolute durable authority")
	}
	store, err := workspace.OpenExisting(filepath.Join(*home, "workspace", "synonbiomed-v1.1.sqlite"))
	if err != nil {
		return fmt.Errorf("open kernel executor workspace: %w", err)
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	executor := &detached.Executor{
		Store: store, CondaHome: strings.TrimSpace(*condaHome), BackendID: *backendID,
		BackendGeneration: *backendGeneration, ExecutorInstanceID: *executorInstanceID,
		SocketPath: *socketPath, ResultSpoolDir: filepath.Join(*home, "workspace", "kernel-result-spool"),
		HeartbeatInterval: *heartbeat, IdleTimeout: *idleTimeout,
	}
	if err := executor.ClaimStartup(ctx); err != nil {
		return err
	}
	manager, err := kernelruntime.DiscoverManagerWithPaths(strings.TrimSpace(*condaHome), strings.TrimSpace(*condaEnvsPath))
	if err != nil {
		return executor.RecordStartupFailure("runtime_discovery", fmt.Errorf("discover kernel executor runtime: %w", err))
	}
	executor.Manager = manager
	return executor.Run(ctx)
}
