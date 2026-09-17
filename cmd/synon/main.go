package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
	adapterfeishu "synon-go/internal/adapters/feishu"
	adapterwechat "synon-go/internal/adapters/wechat"
	"synon-go/internal/buildinfo"
	"synon-go/internal/capabilities"
	"synon-go/internal/config"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/networktls"
	"synon-go/internal/operationlog"
	runtimekv "synon-go/internal/persistence/runtimekv"
	workspace "synon-go/internal/persistence/workspace"
	pluginhost "synon-go/internal/plugins/host"
	"synon-go/internal/server"
	"synon-go/internal/synonlink"
	"synon-go/internal/tools/mcpstdio"
	"synon-go/internal/tools/webfetch"
	"synon-go/internal/tools/websearch"
	"synon-go/internal/vmresources"
)

func main() {
	if err := runMain(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	args := append([]string(nil), os.Args[1:]...)
	if len(args) > 0 {
		switch args[0] {
		case "release-manifest":
			return runReleaseManifestCLI(args[1:], os.Stdout)
		case "release-supply-chain":
			return runReleaseSupplyChainCLI(args[1:], os.Stdout)
		case "migration-v1.1":
			return runV11MigrationCLI(context.Background(), args[1:], os.Stdout)
		case "migrate":
			return runV11MigrationCLI(context.Background(), normalizeMigrationArgs(args[1:]), os.Stdout)
		case "workspace-import":
			return runWorkspaceImportCLI(context.Background(), args[1:], os.Stdout)
		case "verify-contracts":
			return runVerifyContractsCLI(args[1:], os.Stdout)
		case "contracts":
			return runContractsCLI(args[1:], os.Stdout)
		case "assets":
			return runAssetsCLI(args[1:], os.Stdout)
		case "doctor":
			return runDoctorCLI(context.Background(), args[1:], os.Stdout)
		case "model-smoke":
			return runModelSmokeCLI(context.Background(), args[1:], os.Stdout)
		case "mcp-ketcher":
			return runKetcherMCPCLI(context.Background(), args[1:], os.Stdin, os.Stdout)
		case "kernel-executor":
			return runKernelExecutorCLI(args[1:])
		case "stress-http":
			return runStressHTTP(args[1:], os.Stdout)
		}
	}
	args = normalizeServeArgs(args)
	info := buildinfo.Release()
	serveFlags, helpRequested, err := parseServeCommandFlags(args, os.Stdout)
	if err != nil {
		return err
	}
	if helpRequested {
		return nil
	}

	if serveFlags.version {
		fmt.Printf("%s %s\n", info.Name, info.Version)
		return nil
	}
	if serveFlags.health {
		fmt.Printf(`{"status":"ok","name":%q,"version":%q}`+"\n", info.Name, info.Version)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	tlsResolver := networktls.NewResolver(networktls.Config{
		Mode: cfg.Network.MCPX509Strict, CABundle: cfg.Network.CABundle,
	})
	return runServer(info, cfg, tlsResolver)
}

type serveCommandFlags struct {
	version bool
	health  bool
}

func parseServeCommandFlags(args []string, output io.Writer) (serveCommandFlags, bool, error) {
	flags := flag.NewFlagSet("synon-go serve", flag.ContinueOnError)
	flags.SetOutput(output)
	version := flags.Bool("version", false, "print version")
	health := flags.Bool("health-json", false, "print health JSON and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return serveCommandFlags{}, true, nil
		}
		return serveCommandFlags{}, false, err
	}
	if flags.NArg() != 0 {
		return serveCommandFlags{}, false, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	return serveCommandFlags{version: *version, health: *health}, false, nil
}

func runServer(info buildinfo.Info, cfg config.Config, tlsResolver *networktls.Resolver) error {
	operationalLog, err := operationlog.Open(filepath.Join(cfg.HomeDir, "logs"))
	if err != nil {
		return fmt.Errorf("configure operational log retention: %w", err)
	}
	previousLogOutput := log.Writer()
	log.SetOutput(io.MultiWriter(previousLogOutput, operationalLog))
	defer func() {
		log.SetOutput(previousLogOutput)
		if closeErr := operationalLog.Close(); closeErr != nil {
			log.Printf("close operational log: %v", closeErr)
		}
	}()
	tlsResolveContext, tlsResolveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	initialTLSPosture := tlsResolver.Current(tlsResolveContext)
	tlsResolveCancel()
	if initialTLSPosture.Diagnostics.OverrideUnrecognized {
		log.Printf("MCP X.509 posture override is unrecognized; continuing with configured/automatic strict policy")
	}
	if initialTLSPosture.Diagnostics.CABundleRefusal != "" {
		log.Printf("network CA bundle rejected; continuing with system trust and strict MCP policy: %s", initialTLSPosture.Diagnostics.CABundleRefusal)
	}
	if initialTLSPosture.Diagnostics.DetectionErrored {
		log.Printf("automatic enterprise TLS detection failed; continuing with strict MCP policy")
	}
	runtimeHTTPClient, err := networktls.HTTPClient(http.DefaultClient, initialTLSPosture.CABundle, cfg.Network.Proxy)
	if err != nil {
		return fmt.Errorf("configure runtime TLS trust: %w", err)
	}
	runtimeState := runtimekv.New(filepath.Join(cfg.HomeDir, "runtime-state.sqlite"))
	defer runtimeState.Close()
	if err := applyPersistedMessageChannelRuntimeConfig(&cfg, runtimeState); err != nil {
		return err
	}
	if err := mcpstdio.MigrateLegacyOAuthTokens(cfg.HomeDir); err != nil {
		return fmt.Errorf("migrate legacy MCP OAuth tokens: %w", err)
	}
	workspaceStore, err := openWorkspaceStore(cfg.HomeDir)
	if err != nil {
		return err
	}
	defer workspaceStore.Close()
	transcriptRepository, err := workspaceStore.TranscriptRepository(context.Background())
	if err != nil {
		return fmt.Errorf("open workspace transcript authority: %w", err)
	}
	transcriptWebReadModel, err := workspaceStore.TranscriptWebReadModel(context.Background())
	if err != nil {
		return fmt.Errorf("open workspace transcript Web read model: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var app *server.Server
	imOutbound, err := newIMOutboundDispatcher(ctx, cfg, runtimeHTTPClient, imOutboundRuntimeHooks{
		Authorize: func(binding adaptercommon.IMSessionBinding) error {
			if app == nil {
				return errors.New("IM outbound pairing authority is not ready")
			}
			return app.AuthorizeIMOutbound(binding)
		},
		OnResult: func(result adaptercommon.SessionOutboundResult) error {
			if app == nil {
				return nil
			}
			return app.RecordIMOutboundResult(result)
		},
	})
	if err != nil {
		return fmt.Errorf("configure IM outbound delivery: %w", err)
	}
	if imOutbound != nil {
		defer imOutbound.Close()
	}
	plugins, err := pluginhost.NewDefaultWithExternalDirectories(cfg.PluginDirectories)
	if err != nil {
		return err
	}
	if err := plugins.StartExternalProcesses(ctx); err != nil {
		plugins.StopExternalProcesses()
		return err
	}
	defer plugins.StopExternalProcesses()
	link := synonlink.NewService()
	link.SetTaskLogPath(filepath.Join(cfg.HomeDir, "synon-link-task-logs.jsonl"))
	compactSummarizer := newCompactSummarizerOptions(cfg)
	restartRequests := make(chan string, 1)
	vmConfigPath, err := vmresources.DiscoverConfigPath()
	if err != nil {
		return err
	}
	vmResourceController := vmresources.New(vmConfigPath, vmresources.DetectLimits())
	vmRestartManager, err := newVMRestartManager(cfg)
	if err != nil {
		return err
	}
	webHandler, webRoot, err := discoverWebUI(cfg.WebRoot, runtimeAssetRoots())
	if err != nil {
		return err
	}
	scientificCapabilities, err := loadScientificCapabilityCatalog()
	if err != nil {
		return err
	}
	skillDirectories := cfg.SkillDirectories
	if len(skillDirectories) == 0 {
		if packagedSkills := resolveRuntimeAssetDirectoryPath("skills/synonbiomed", runtimeAssetRoots()...); packagedSkills != "" {
			skillDirectories = []string{packagedSkills}
		}
	}
	kernelManager, err := kernelruntime.DiscoverManagerWithPaths(cfg.CondaHome, cfg.CondaEnvsPath)
	if err != nil {
		return fmt.Errorf("discover local scientific runtime manager: %w", err)
	}
	kernelExecutionBackend, err := newKernelExecutionBackend(cfg, workspaceStore)
	if err != nil {
		return err
	}
	app = server.New(applyRuntimeMemoryConfig(server.Options{
		Capabilities: capabilities.CompactSynon(),
		SynonLink:    link,
		SynonLinkAuth: server.SynonLinkAuthOptions{
			Username: cfg.SynonLinkAuth.Username,
			Password: cfg.SynonLinkAuth.Password,
			UserID:   "local",
			TTL:      time.Duration(cfg.SynonLinkAuth.SessionTTLMinutes) * time.Minute,
		},
		WebAuth: server.WebAuthOptions{
			PublicBaseURL: cfg.WebAuth.PublicBaseURL,
			Google: server.WebOIDCProviderOptions{
				ClientID:     cfg.WebAuth.Google.ClientID,
				ClientSecret: cfg.WebAuth.Google.ClientSecret,
			},
			Apple: server.WebAppleOIDCProviderOptions{
				ClientID:   cfg.WebAuth.Apple.ClientID,
				TeamID:     cfg.WebAuth.Apple.TeamID,
				KeyID:      cfg.WebAuth.Apple.KeyID,
				PrivateKey: cfg.WebAuth.Apple.PrivateKey,
			},
			WeChat: server.WebWeChatOAuthOptions{
				AppID:     cfg.WebAuth.WeChat.AppID,
				AppSecret: cfg.WebAuth.WeChat.AppSecret,
			},
		},
		LinkPackagePath:     resolveRuntimeAssetPath(cfg.SynonLinkPackage, runtimeAssetRoots()...),
		FileRoot:            cfg.HomeDir,
		RuntimeAssetsDir:    discoverSynonBiomedRuntimeAssetsDir(runtimeAssetRoots()),
		Plugins:             plugins,
		SkillDirectories:    skillDirectories,
		ScienceCapabilities: scientificCapabilities,
		CompactSummarizer:   compactSummarizer,
		RunnerDiagnostics:   newRunnerDiagnostics(cfg),
		AdapterDiagnostics:  newAdapterDiagnostics(cfg),
		FeishuDeviceQR:      adapterfeishu.NewDeviceQRLoginManager(""),
		WeChatQR:            adapterwechat.NewQRLoginManager(cfg.WeChat.BaseURL),
		IMOutbound:          imOutbound,
		Feedback: server.FeedbackOptions{
			ServiceURL:        cfg.Feedback.ServiceURL,
			Token:             cfg.Feedback.Token,
			Beta:              cfg.Feedback.Beta,
			Disabled:          cfg.Feedback.Disabled,
			TelemetryDisabled: cfg.DisableTelemetry,
		},
		Workspace:              workspaceStore,
		RuntimeStore:           runtimeState,
		KernelManager:          kernelManager,
		KernelExecutionBackend: kernelExecutionBackend,
		Transcript:             transcriptRepository,
		TranscriptWebReadModel: transcriptWebReadModel,
		DataDirSource:          cfg.DataDirSource,
		DataDirControlPath:     cfg.DataDirControlPath,
		DefaultDataDir:         cfg.DefaultDataDir,
		CondaHome:              cfg.CondaHome,
		CondaEnvsPath:          cfg.CondaEnvsPath,
		ConfigAllowedDomains:   cfg.Network.AllowedDomains,
		ConfigDeniedDomains:    cfg.Network.DeniedDomains,
		ConfigNetworkProxy:     cfg.Network.Proxy,
		HTTPClient:             runtimeHTTPClient,
		WebFetchOptions:        webfetch.Options{BaseHTTPClient: runtimeHTTPClient},
		WebSearchOptions:       websearch.Options{BaseHTTPClient: runtimeHTTPClient},
		MCPX509Posture: func() mcpstdio.TLSPosture {
			posture := tlsResolver.Current(context.Background())
			return mcpstdio.TLSPosture{Strict: posture.Strict, CABundle: posture.CABundle, ProxyURL: cfg.Network.Proxy}
		},
		RestartRuntime:          newRuntimeRestartRequester(restartRequests),
		StartBackgroundServices: true,
		VMRestart:               vmRestartManager,
		VMResources:             vmResourceController,
		WebUI:                   webHandler,
	}, cfg))
	if recovery, err := app.RecoverIMOutbound(ctx); err != nil {
		return fmt.Errorf("recover IM outbound delivery: %w", err)
	} else if recovery.Routes > 0 {
		fmt.Printf("IM outbound recovery routes=%d restored=%d skipped=%d replayed=%d\n", recovery.Routes, recovery.Restored, recovery.Skipped, recovery.Replayed)
		for _, warning := range recovery.Warnings {
			log.Printf("IM outbound recovery warning: %s", warning)
		}
	}
	defer func() {
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := app.Close(shutdownContext); err != nil {
			log.Printf("close kernel runtime: %v", err)
		}
	}()
	realtimeOutbox, err := newRealtimeOutboxDispatcher(workspaceStore, app)
	if err != nil {
		return fmt.Errorf("configure realtime outbox: %w", err)
	}
	kernelSettlementOutbox, err := newKernelSettlementOutboxDispatcher(workspaceStore, app)
	if err != nil {
		return fmt.Errorf("configure kernel result settlement outbox: %w", err)
	}
	routineScheduler, err := newRoutineScheduler(cfg, app, workspaceStore)
	if err != nil {
		return fmt.Errorf("configure routine scheduler: %w", err)
	}
	if err := startRoutineScheduler(ctx, routineScheduler); err != nil {
		return fmt.Errorf("start routine scheduler: %w", err)
	}
	defer stopRoutineScheduler(app, routineScheduler)
	srv := &http.Server{
		Addr:              cfg.Address(),
		Handler:           withRoutineSchedulerWake(app.Handler(), routineScheduler.Wake),
		ReadHeaderTimeout: 5 * time.Second,
	}

	listener, err := net.Listen("tcp", cfg.Address())
	if err != nil {
		return err
	}
	defer listener.Close()
	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- srv.Serve(listener)
	}()
	startSupervisor := func(name string, run func(context.Context) error) {
		app.ReportRuntimeComponent(name, errRuntimeComponentStarting)
		go runRuntimeSupervisor(
			ctx, name, func(runCtx context.Context, ready func()) error {
				// Component construction and configuration have already completed;
				// entering its lifecycle loop is the explicit readiness boundary.
				ready()
				return run(runCtx)
			}, app.ReportRuntimeComponent, app.IsDraining, defaultRuntimeSupervisorPolicy(),
		)
	}
	startSupervisor("realtime-outbox", realtimeOutbox.Run)
	fmt.Println("realtime outbox dispatcher enabled")
	startSupervisor("kernel-result-settlement", kernelSettlementOutbox.Run)
	fmt.Println("kernel result settlement dispatcher enabled")
	if app.ManagedEnvironmentSupervisorEnabled() {
		startSupervisor("managed-environment-runtime", app.RunManagedEnvironmentSupervisor)
		fmt.Println("managed environment supervisor enabled")
		startSupervisor("scientific-runtime-warmups", app.RunScientificRuntimeWarmups)
		fmt.Println("scientific runtime warmups enabled")
		startSupervisor("compute-provider-runtime", app.RunComputeProviderProvisioner)
		fmt.Println("compute provider runtime supervisor enabled")
	}
	startSupervisor("compute-provider-jobs", app.RunComputeProviderJobSupervisor)
	fmt.Println("compute provider job supervisor enabled")
	if app.ManagedPythonProvisioningEnabled() {
		startSupervisor("managed-python-runtime", app.RunManagedPythonProvisioner)
		fmt.Println("managed Python runtime supervisor enabled")
	}
	if app.KernelIdleReaperEnabled() {
		startSupervisor("kernel-idle-reaper", app.RunKernelIdleReaper)
		fmt.Println("kernel idle reaper enabled")
	}
	if app.KernelLocalExecApprovalRecoveryEnabled() {
		startSupervisor("kernel-local-exec-recovery", app.RunKernelLocalExecApprovalRecovery)
		fmt.Println("kernel local execution approval recovery enabled")
	}
	if app.KernelLocalOperationRecoveryEnabled() {
		startSupervisor("kernel-local-operation-recovery", app.RunKernelLocalOperationRecovery)
		fmt.Println("kernel local operation recovery enabled")
	}
	if app.DetachedKernelExecutionRecoveryEnabled() {
		startSupervisor("detached-kernel-execution-recovery", app.RunDetachedKernelExecutionRecovery)
		fmt.Println("detached kernel execution recovery enabled")
	}
	fmt.Println("routine scheduler enabled")
	if poller, err := newWeChatPoller(runtimeHTTPClient, cfg, app); err != nil {
		return err
	} else if poller != nil {
		startSupervisor("wechat-poller", poller.Run)
		fmt.Println("wechat polling adapter enabled")
	}
	if stream, err := newFeishuStreamClient(cfg, app); err != nil {
		return err
	} else if stream != nil {
		startSupervisor("feishu-stream", stream.Start)
		go func() {
			<-ctx.Done()
			stream.Close()
		}()
		fmt.Println("feishu stream adapter enabled")
	}
	if runnerOptions, enabled, err := newSessionRunnerCommandOptions(cfg); err != nil {
		return err
	} else if enabled {
		startSupervisor("runner-command", func(runCtx context.Context) error {
			return app.RunSessionRunnerCommandLoop(runCtx, runnerOptions)
		})
		fmt.Println("session runner command supervisor enabled")
	}
	if runnerOptions, enabled, err := newSessionRunnerChatOptions(cfg); err != nil {
		return err
	} else if enabled {
		chatWorkers := sessionRunnerChatWorkerCount(cfg.Runner)
		startSupervisor("runner-chat", func(runCtx context.Context) error {
			return app.RunSessionRunnerChatPool(runCtx, runnerOptions, chatWorkers)
		})
		dispatchWorkers := frameResumeDispatchWorkerCount(chatWorkers)
		startSupervisor("frame-resume-recovery", app.RunFrameResumeRecoveryLoop)
		for workerIndex := 0; workerIndex < dispatchWorkers; workerIndex++ {
			workerIndex := workerIndex
			workerID := fmt.Sprintf("synon-frame-resume-dispatcher-%d", workerIndex+1)
			startSupervisor(fmt.Sprintf("frame-resume-dispatch-%d", workerIndex+1), func(runCtx context.Context) error {
				return app.RunFrameResumeDispatchLoop(runCtx, server.FrameResumeDispatchOptions{
					WorkerID: workerID,
					Chat:     runnerOptions,
				})
			})
		}
		fmt.Printf("session runner chat supervisor enabled with %d workers\n", chatWorkers)
		fmt.Printf("frame resume dispatch supervisors enabled with %d workers\n", dispatchWorkers)
		fmt.Println("frame resume recovery supervisor enabled")
	}
	fmt.Printf("%s listening on http://%s\n", info.Name, cfg.Address())
	if webRoot != "" {
		fmt.Printf("workbench web UI enabled from %s\n", webRoot)
		fmt.Printf("browser sign-in: %s\n", browserLoginURL(cfg))
	}
	go runLegacyFrameHistoryRecoveryLoop(ctx, transcriptRepository)
	if vmRestartManager != nil {
		if err := vmRestartManager.MarkRuntimeStarted(); err != nil {
			return fmt.Errorf("mark recovered VM runtime ready: %w", err)
		}
	}

	stop := make(chan os.Signal, 1)
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if reloadSignal := runtimeIdleReloadSignal(); reloadSignal != nil {
		signals = append(signals, reloadSignal)
	}
	signal.Notify(stop, signals...)
	for {
		select {
		case sig := <-stop:
			if isRuntimeIdleReloadSignal(sig) && !app.TryDrainIdle() {
				fmt.Println("verified runtime reload deferred while tasks are active")
				continue
			}
			fmt.Printf("received %s, shutting down\n", sig)
			drainContext, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
			drainErr := app.Drain(drainContext)
			drainCancel()
			cancel()
			shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			shutdownErr := srv.Shutdown(shutdownContext)
			shutdownCancel()
			return errors.Join(drainErr, shutdownErr)
		case reason := <-restartRequests:
			fmt.Printf("runtime restart requested (%s), shutting down\n", reason)
			drainContext, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
			drainErr := app.Drain(drainContext)
			drainCancel()
			cancel()
			shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			shutdownErr := srv.Shutdown(shutdownContext)
			shutdownCancel()
			if err := errors.Join(drainErr, shutdownErr); err != nil {
				return err
			}
			return &runtimeRestartRequestedError{Reason: reason}
		case err := <-serverErrCh:
			if err == http.ErrServerClosed {
				return nil
			}
			return err
		}
	}
}

func browserLoginURL(cfg config.Config) string {
	host, port, err := net.SplitHostPort(cfg.Address())
	if err != nil {
		return "http://" + cfg.Address() + "/#/login"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/#/login"
}

func applyRuntimeMemoryConfig(options server.Options, cfg config.Config) server.Options {
	memory := cfg.Memory
	options.MemoryConfig = &memory
	return options
}

func openWorkspaceStore(home string) (*workspace.Store, error) {
	return workspace.Open(filepath.Join(home, "workspace", "synonbiomed-v1.1.sqlite"))
}

func normalizeRunnerConfig(runner config.RunnerConfig) config.RunnerConfig {
	if strings.TrimSpace(runner.ID) == "" {
		runner.ID = "synon-go-runner"
	}
	if runner.PollIntervalMS <= 0 {
		runner.PollIntervalMS = 1000
	}
	if runner.LeaseTTLSeconds <= 0 {
		runner.LeaseTTLSeconds = 300
	}
	if runner.ChatTimeoutSeconds <= 0 {
		runner.ChatTimeoutSeconds = 120
	}
	if runner.ChatMaxAttempts <= 0 {
		runner.ChatMaxAttempts = 4
	}
	if runner.CommandTimeoutSeconds <= 0 {
		runner.CommandTimeoutSeconds = 600
	}
	if runner.ReplayLimit <= 0 {
		runner.ReplayLimit = 200
	}
	if runner.OutputLimitBytes <= 0 {
		runner.OutputLimitBytes = 1024 * 1024
	}
	return runner
}

// minimumSessionRunnerChatWorkers is the automatic baseline for independent
// conversations. It is intentionally separate from CPU scheduling capacity:
// the runner is I/O-bound while waiting on model/tool calls, and deployments
// may still select a lower explicit value when their provider or host requires it.
const minimumSessionRunnerChatWorkers = 10

// sessionRunnerChatWorkerCount keeps scheduling capacity separate from task
// semantics. A zero configuration follows the Go scheduler capacity of the
// current deployment, but never advertises fewer than the supported baseline.
// An explicit positive value remains an operator-controlled capacity override.
func sessionRunnerChatWorkerCount(runner config.RunnerConfig) int {
	if runner.ChatWorkers > 0 {
		return runner.ChatWorkers
	}
	workers := runtime.GOMAXPROCS(0)
	if workers < minimumSessionRunnerChatWorkers {
		return minimumSessionRunnerChatWorkers
	}
	return workers
}

// maxFrameResumeDispatchWorkers keeps recovery concurrency bounded without
// reducing the automatic ten-conversation baseline. A slow recovery therefore
// cannot monopolize the queue or block nine independent conversations.
const maxFrameResumeDispatchWorkers = 10

func frameResumeDispatchWorkerCount(chatWorkers int) int {
	if chatWorkers < 2 {
		return 2
	}
	if chatWorkers > maxFrameResumeDispatchWorkers {
		return maxFrameResumeDispatchWorkers
	}
	return chatWorkers
}

func newSessionRunnerCommandOptions(cfg config.Config) (server.SessionRunnerCommandOptions, bool, error) {
	cfg.Runner = normalizeRunnerConfig(cfg.Runner)
	if !cfg.Runner.Enabled {
		return server.SessionRunnerCommandOptions{}, false, nil
	}
	provider := sessionRunnerProvider(cfg.Runner)
	switch provider {
	case "command":
	case "openai_chat", "go_builtin", server.WorkspaceSessionRunnerProvider, "disabled":
		return server.SessionRunnerCommandOptions{}, false, nil
	default:
		return server.SessionRunnerCommandOptions{}, false, fmt.Errorf("unsupported runner.provider: %s", provider)
	}
	command := strings.TrimSpace(cfg.Runner.Command)
	if command == "" {
		return server.SessionRunnerCommandOptions{}, false, fmt.Errorf("runner.command is required when runner.enabled is true")
	}
	if cfg.Runner.PollIntervalMS <= 0 {
		return server.SessionRunnerCommandOptions{}, false, fmt.Errorf("runner.poll_interval_ms must be positive")
	}
	if cfg.Runner.LeaseTTLSeconds <= 0 {
		return server.SessionRunnerCommandOptions{}, false, fmt.Errorf("runner.lease_ttl_seconds must be positive")
	}
	if cfg.Runner.ReplayLimit <= 0 {
		return server.SessionRunnerCommandOptions{}, false, fmt.Errorf("runner.replay_limit must be positive")
	}
	if cfg.Runner.OutputLimitBytes <= 0 {
		return server.SessionRunnerCommandOptions{}, false, fmt.Errorf("runner.output_limit_bytes must be positive")
	}
	if cfg.Runner.CommandTimeoutSeconds <= 0 {
		return server.SessionRunnerCommandOptions{}, false, fmt.Errorf("runner.command_timeout_seconds must be positive")
	}
	return server.SessionRunnerCommandOptions{
		RunnerID:         cfg.Runner.ID,
		Command:          command,
		Args:             append([]string(nil), cfg.Runner.Args...),
		CommandTimeout:   time.Duration(cfg.Runner.CommandTimeoutSeconds) * time.Second,
		LeaseTTL:         time.Duration(cfg.Runner.LeaseTTLSeconds) * time.Second,
		PollInterval:     time.Duration(cfg.Runner.PollIntervalMS) * time.Millisecond,
		ReplayLimit:      cfg.Runner.ReplayLimit,
		OutputLimitBytes: cfg.Runner.OutputLimitBytes,
	}, true, nil
}

func newSessionRunnerChatOptions(cfg config.Config) (server.SessionRunnerChatOptions, bool, error) {
	cfg.Runner = normalizeRunnerConfig(cfg.Runner)
	provider := sessionRunnerProvider(cfg.Runner)
	if !cfg.Runner.Enabled && provider != "go_builtin" && provider != server.WorkspaceSessionRunnerProvider {
		return server.SessionRunnerChatOptions{}, false, nil
	}
	switch provider {
	case "openai_chat":
	case "go_builtin":
	case server.WorkspaceSessionRunnerProvider:
	case "command", "disabled":
		return server.SessionRunnerChatOptions{}, false, nil
	default:
		return server.SessionRunnerChatOptions{}, false, fmt.Errorf("unsupported runner.provider: %s", provider)
	}
	endpoint := strings.TrimSpace(cfg.Runner.ChatEndpoint)
	model := strings.TrimSpace(cfg.Runner.ChatModel)
	if provider == "go_builtin" {
		endpoint = server.BuiltinSessionRunnerChatEndpoint
		model = server.BuiltinSessionRunnerChatModel
	} else if provider == server.WorkspaceSessionRunnerProvider {
		endpoint = ""
		model = ""
	} else {
		if endpoint == "" {
			return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.chat_endpoint is required when runner.provider is openai_chat")
		}
		if model == "" {
			return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.chat_model is required when runner.provider is openai_chat")
		}
	}
	if cfg.Runner.PollIntervalMS <= 0 {
		return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.poll_interval_ms must be positive")
	}
	if cfg.Runner.LeaseTTLSeconds <= 0 {
		return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.lease_ttl_seconds must be positive")
	}
	if cfg.Runner.ChatTimeoutSeconds <= 0 {
		return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.chat_timeout_seconds must be positive")
	}
	if cfg.Runner.ChatToolRoundLimit < 0 {
		return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.chat_tool_round_limit must be zero (unlimited) or positive")
	}
	if cfg.Runner.ChatToolCallBatchLimit < 0 {
		return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.chat_tool_call_batch_limit must be zero (unlimited) or positive")
	}
	if cfg.Runner.ChatMaxAttempts <= 0 {
		return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.chat_max_attempts must be positive")
	}
	if cfg.Runner.ReplayLimit <= 0 {
		return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.replay_limit must be positive")
	}
	if cfg.Runner.OutputLimitBytes <= 0 {
		return server.SessionRunnerChatOptions{}, false, fmt.Errorf("runner.output_limit_bytes must be positive")
	}
	return server.SessionRunnerChatOptions{
		RunnerID:             cfg.Runner.ID,
		Endpoint:             endpoint,
		APIKey:               cfg.Runner.ChatAPIKey,
		Model:                model,
		SystemPrompt:         cfg.Runner.ChatSystemPrompt,
		AllowedTools:         append([]string(nil), cfg.Runner.ChatTools...),
		RequestTimeout:       time.Duration(cfg.Runner.ChatTimeoutSeconds) * time.Second,
		MaxToolRounds:        cfg.Runner.ChatToolRoundLimit,
		MaxToolCallsPerRound: cfg.Runner.ChatToolCallBatchLimit,
		MaxAttempts:          cfg.Runner.ChatMaxAttempts,
		LeaseTTL:             time.Duration(cfg.Runner.LeaseTTLSeconds) * time.Second,
		PollInterval:         time.Duration(cfg.Runner.PollIntervalMS) * time.Millisecond,
		ReplayLimit:          cfg.Runner.ReplayLimit,
		OutputLimitBytes:     cfg.Runner.OutputLimitBytes,
		RequireSavedModel:    provider == server.WorkspaceSessionRunnerProvider,
	}, true, nil
}

func newCompactSummarizerOptions(cfg config.Config) server.SessionRunnerChatOptions {
	endpoint := strings.TrimSpace(cfg.CompactSummarizer.Endpoint)
	model := strings.TrimSpace(cfg.CompactSummarizer.Model)
	apiKey := cfg.CompactSummarizer.APIKey
	timeoutSeconds := cfg.CompactSummarizer.TimeoutSeconds
	maxAttempts := cfg.CompactSummarizer.MaxAttempts
	runnerID := "synon-go-compact"
	if endpoint == "" && model == "" && strings.TrimSpace(apiKey) == "" && timeoutSeconds == 0 && maxAttempts == 0 {
		endpoint = strings.TrimSpace(cfg.Runner.ChatEndpoint)
		model = strings.TrimSpace(cfg.Runner.ChatModel)
		apiKey = cfg.Runner.ChatAPIKey
		timeoutSeconds = cfg.Runner.ChatTimeoutSeconds
		maxAttempts = cfg.Runner.ChatMaxAttempts
		runnerID = strings.TrimSpace(cfg.Runner.ID)
		if runnerID == "" {
			runnerID = "synon-go-compact"
		}
	}
	if endpoint == "" || model == "" {
		return server.SessionRunnerChatOptions{}
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	return server.SessionRunnerChatOptions{
		RunnerID:       runnerID,
		Endpoint:       endpoint,
		APIKey:         apiKey,
		Model:          model,
		RequestTimeout: timeout,
		MaxAttempts:    maxAttempts,
	}
}

func newRunnerDiagnostics(cfg config.Config) server.RunnerDiagnostics {
	cfg.Runner = normalizeRunnerConfig(cfg.Runner)
	apiKeySource := "none"
	if strings.TrimSpace(cfg.Runner.ChatAPIKey) != "" {
		apiKeySource = "config"
	}
	if strings.TrimSpace(os.Getenv("SYNON_RUNNER_CHAT_API_KEY")) != "" {
		apiKeySource = "env:SYNON_RUNNER_CHAT_API_KEY"
	}
	provider := sessionRunnerProvider(cfg.Runner)
	enabled := cfg.Runner.Enabled || provider == "go_builtin" || provider == server.WorkspaceSessionRunnerProvider
	chatEndpoint := cfg.Runner.ChatEndpoint
	chatModel := cfg.Runner.ChatModel
	if provider == "go_builtin" {
		chatEndpoint = server.BuiltinSessionRunnerChatEndpoint
		chatModel = server.BuiltinSessionRunnerChatModel
	}
	return server.RunnerDiagnostics{
		Enabled:               enabled,
		Provider:              provider,
		RuntimeModelAuthority: provider == server.WorkspaceSessionRunnerProvider,
		RunnerID:              cfg.Runner.ID,
		CommandConfigured:     strings.TrimSpace(cfg.Runner.Command) != "",
		ChatEndpoint:          chatEndpoint,
		ChatModel:             chatModel,
		ChatAPIKeySet:         strings.TrimSpace(cfg.Runner.ChatAPIKey) != "",
		ChatAPIKeySource:      apiKeySource,
		ChatTools:             append([]string(nil), cfg.Runner.ChatTools...),
		ChatToolRoundLimit:    cfg.Runner.ChatToolRoundLimit,
		PollIntervalMS:        cfg.Runner.PollIntervalMS,
		LeaseTTLSeconds:       cfg.Runner.LeaseTTLSeconds,
		ReplayLimit:           cfg.Runner.ReplayLimit,
		OutputLimitBytes:      cfg.Runner.OutputLimitBytes,
	}
}

func newAdapterDiagnostics(cfg config.Config) server.AdapterDiagnostics {
	feishuConfigured := strings.TrimSpace(cfg.Feishu.AppID) != "" && strings.TrimSpace(cfg.Feishu.AppSecret) != ""
	weChatConfigured := strings.TrimSpace(cfg.WeChat.AccountID) != "" && strings.TrimSpace(cfg.WeChat.BotToken) != ""
	return server.AdapterDiagnostics{Platforms: []server.AdapterPlatformDiagnostics{
		{
			Name:               "feishu",
			Enabled:            adapterEnabled(cfg.EnabledAdapters, "feishu"),
			Configured:         feishuConfigured,
			InboundConfigured:  feishuConfigured,
			OutboundConfigured: feishuConfigured,
			Endpoint:           firstNonEmpty(strings.TrimSpace(cfg.Feishu.Domain), "https://open.feishu.cn"),
			CredentialFields: map[string]bool{
				"app_id":             strings.TrimSpace(cfg.Feishu.AppID) != "",
				"app_secret":         strings.TrimSpace(cfg.Feishu.AppSecret) != "",
				"verification_token": strings.TrimSpace(cfg.Feishu.VerificationToken) != "",
				"encrypt_key":        strings.TrimSpace(cfg.Feishu.EncryptKey) != "",
			},
			CredentialValues: map[string]string{
				"app_id":             strings.TrimSpace(cfg.Feishu.AppID),
				"app_secret":         strings.TrimSpace(cfg.Feishu.AppSecret),
				"verification_token": strings.TrimSpace(cfg.Feishu.VerificationToken),
				"encrypt_key":        strings.TrimSpace(cfg.Feishu.EncryptKey),
			},
		},
		{
			Name:               "wechat",
			Enabled:            adapterEnabled(cfg.EnabledAdapters, "wechat"),
			Configured:         weChatConfigured,
			InboundConfigured:  weChatConfigured,
			OutboundConfigured: weChatConfigured,
			Endpoint:           firstNonEmpty(strings.TrimSpace(cfg.WeChat.BaseURL), "https://ilinkai.weixin.qq.com"),
			CredentialFields: map[string]bool{
				"account_id": strings.TrimSpace(cfg.WeChat.AccountID) != "",
				"bot_token":  strings.TrimSpace(cfg.WeChat.BotToken) != "",
				"user_id":    strings.TrimSpace(cfg.WeChat.UserID) != "",
			},
			CredentialValues: map[string]string{
				"account_id": strings.TrimSpace(cfg.WeChat.AccountID),
				"bot_token":  strings.TrimSpace(cfg.WeChat.BotToken),
				"user_id":    strings.TrimSpace(cfg.WeChat.UserID),
			},
		},
	}}
}

func sessionRunnerProvider(runner config.RunnerConfig) string {
	provider := strings.TrimSpace(runner.Provider)
	if provider != "" {
		return provider
	}
	if strings.TrimSpace(runner.ChatEndpoint) != "" || strings.TrimSpace(runner.ChatModel) != "" {
		return "openai_chat"
	}
	if strings.TrimSpace(runner.Command) != "" {
		return "command"
	}
	return server.WorkspaceSessionRunnerProvider
}

func newWeChatPoller(client *http.Client, cfg config.Config, app *server.Server) (*adapterwechat.Poller, error) {
	if !adapterEnabled(cfg.EnabledAdapters, "wechat") || cfg.WeChat.BotToken == "" {
		return nil, nil
	}
	if app == nil {
		return nil, fmt.Errorf("wechat polling requires server")
	}
	interval := time.Duration(cfg.WeChat.PollIntervalMS) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	return adapterwechat.NewPoller(client, cfg.WeChat.BaseURL, cfg.WeChat.BotToken, adapterwechat.PollerOptions{
		Interval: interval,
		Timeout:  35 * time.Second,
	}, func(ctx context.Context, inbound adapterwechat.Message) error {
		_, err := app.HandleWeChatInbound(ctx, inbound)
		return err
	}), nil
}

type streamStartCloser interface {
	Start(context.Context) error
	Close()
}

func newFeishuStreamClient(cfg config.Config, app *server.Server) (streamStartCloser, error) {
	if !adapterEnabled(cfg.EnabledAdapters, "feishu") || cfg.Feishu.AppID == "" || cfg.Feishu.AppSecret == "" {
		return nil, nil
	}
	if app == nil {
		return nil, fmt.Errorf("feishu stream requires server")
	}
	router := adapterfeishu.NewStreamRouter(adapterfeishu.StreamRouterOptions{
		OnInboundEvent: func(ctx context.Context, inbound adapterfeishu.InboundEvent) error {
			_, err := app.HandleFeishuInbound(ctx, inbound)
			return err
		},
	})
	dispatcher := adapterfeishu.NewStreamEventDispatcher(cfg.Feishu.VerificationToken, cfg.Feishu.EncryptKey, router)
	return adapterfeishu.NewSDKStreamClient(adapterfeishu.SDKStreamClientConfig{
		AppID:         cfg.Feishu.AppID,
		AppSecret:     cfg.Feishu.AppSecret,
		Domain:        cfg.Feishu.Domain,
		AutoReconnect: true,
	}, dispatcher)
}

func adapterEnabled(adapters []string, target string) bool {
	for _, adapter := range adapters {
		if adapter == target {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
