package server

import (
	"context"
	"log"
	"net/http"

	"os"

	"path/filepath"

	"strings"
	"sync"
	"time"

	"synon-go/internal/account"
	adaptercommon "synon-go/internal/adapters/common"
	adapterfeishu "synon-go/internal/adapters/feishu"
	adapterwechat "synon-go/internal/adapters/wechat"
	"synon-go/internal/agentruntime"

	"synon-go/internal/capabilities"
	"synon-go/internal/cloudstore"
	compute "synon-go/internal/compute"
	"synon-go/internal/datadir"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/mcpdirectory"
	"synon-go/internal/memoryconfig"
	"synon-go/internal/observability"
	eventjournal "synon-go/internal/persistence/journal"
	pairingstore "synon-go/internal/persistence/pairing"
	runtimekv "synon-go/internal/persistence/runtimekv"
	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	settingsstore "synon-go/internal/persistence/settings"
	taskruns "synon-go/internal/persistence/taskruns"
	taskstore "synon-go/internal/persistence/tasks"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	pluginhost "synon-go/internal/plugins/host"

	runtimecontrol "synon-go/internal/runtimecontrol"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
	"synon-go/internal/synonlink"

	"synon-go/internal/tools/mcpstdio"

	"synon-go/internal/tools/registry"
	"synon-go/internal/tools/shellops"
	"synon-go/internal/tools/webfetch"
	"synon-go/internal/tools/websearch"
	"synon-go/internal/vmresources"
	"synon-go/internal/vmrestart"
)

type Options struct {
	Capabilities            capabilities.Report
	SynonLink               *synonlink.Service
	SynonLinkAuth           SynonLinkAuthOptions
	WebAuth                 WebAuthOptions
	LinkPackagePath         string
	Tools                   *registry.Registry
	Operations              *registry.Registry
	Plugins                 *pluginhost.Host
	FileRoot                string
	RuntimeAssetsDir        string
	ComputeRemoteDialer     compute.RemoteDialer
	ComputeProviderDial     kernelruntime.ProviderProxyDialFunc
	ProviderOperationRunner kernelruntime.ProviderOperationRunner
	HostGPUDetector         func(context.Context) compute.GPUInfo
	ModalConfigPath         string
	SkillDirectories        []string
	SkillCatalog            *skills.Catalog
	ScienceCapabilities     *sciencecapability.Catalog
	AgentCatalog            *agentruntime.AgentCatalog
	AgentCatalogRoot        string
	AgentManifestPath       string
	FeishuDeviceQR          *adapterfeishu.DeviceQRLoginManager
	WeChatQR                *adapterwechat.QRLoginManager
	HTTPClient              *http.Client
	WebFetchOptions         webfetch.Options
	WebSearchOptions        websearch.Options
	VerifierToken           string
	Feedback                FeedbackOptions
	CompactSummarizer       SessionRunnerChatOptions
	RunnerDiagnostics       RunnerDiagnostics
	AdapterDiagnostics      AdapterDiagnostics
	IMOutbound              adaptercommon.SessionOutboundSink
	Workspace               *workspace.Store
	RuntimeStore            *runtimekv.Store
	Transcript              *transcriptstore.Repository
	TranscriptWebReadModel  *transcriptstore.WebReadModelRepository
	MemoryConfig            *memoryconfig.Config
	MCPDirectory            *mcpdirectory.Service
	RCSBFiles               RCSBFileFetcher
	RCSBSearch              RCSBStructureSearcher
	PublicScientificFiles   PublicScientificFileFetcher
	KernelManager           *kernelruntime.Manager
	KernelExecutionBackend  kernelruntime.ExecutionBackend
	DataDirSource           string
	DataDirControlPath      string
	DefaultDataDir          string
	CondaHome               string
	CondaEnvsPath           string
	ConfigAllowedDomains    []string
	ConfigDeniedDomains     []string
	ConfigNetworkProxy      string
	MCPX509Posture          func() mcpstdio.TLSPosture
	RestartRuntime          func(reason string) error
	RuntimeUpdate           RuntimeUpdateOptions
	StartBackgroundServices bool
	HostDirectoryPicker     func(context.Context) (string, error)
	VMRestart               *vmrestart.Manager
	VMResources             *vmresources.Controller
	CloudFactory            cloudstore.ClientFactory
	WebUI                   http.Handler
}

type RunnerDiagnostics struct {
	Enabled               bool
	Provider              string
	RunnerID              string
	CommandConfigured     bool
	ChatEndpoint          string
	ChatModel             string
	ChatAPIKeySet         bool
	ChatAPIKeySource      string
	ChatTools             []string
	ChatToolRoundLimit    int
	PollIntervalMS        int
	LeaseTTLSeconds       int
	ReplayLimit           int64
	OutputLimitBytes      int64
	RuntimeModelAuthority bool
}

type AdapterDiagnostics struct {
	Platforms []AdapterPlatformDiagnostics
}

type AdapterPlatformDiagnostics struct {
	Name               string
	Enabled            bool
	Configured         bool
	InboundConfigured  bool
	OutboundConfigured bool
	Endpoint           string
	CredentialFields   map[string]bool
	CredentialValues   map[string]string `json:"-"`
}

type Server struct {
	capabilities  capabilities.Report
	synonLink     *synonlink.Service
	synonLinkAuth *synonLinkAuthenticator
	loginLimiter  *loginRateLimiter
	webAuthenticationState
	accountManagementReader          account.ManagementReader
	linkPackagePath                  string
	tools                            *registry.Registry
	operations                       *registry.Registry
	plugins                          *pluginhost.Host
	fileRoot                         string
	runtimeAssetsDir                 string
	settingsStore                    *settingsstore.Store
	secretStore                      *secretstore.Store
	sessionStore                     *sessionstore.Store
	computeRemoteDialer              compute.RemoteDialer
	computeProviderDial              kernelruntime.ProviderProxyDialFunc
	computeProviderProvisionWake     chan string
	computeProviderJobWake           chan struct{}
	providerOperationRunner          kernelruntime.ProviderOperationRunner
	computeProviderJobsMu            sync.Mutex
	computeProviderJobs              map[string]bool
	computeProviderHandleMu          sync.Mutex
	computeSubmitMu                  sync.Mutex
	computeRunsMu                    sync.Mutex
	computeRuns                      map[string]context.CancelFunc
	hostGPUDetector                  func(context.Context) compute.GPUInfo
	modalConfigPath                  string
	eventJournal                     *eventjournal.EventJournal
	taskStore                        *taskstore.Store
	taskRunStore                     *taskruns.Store
	pairingStore                     *pairingstore.Store
	runtimeStore                     *runtimekv.Store
	runtimeStoreOwned                bool
	usageScanner                     *runtimecontrol.Scanner
	kernelManager                    *kernelruntime.Manager
	kernelExecutionBackend           kernelruntime.ExecutionBackend
	kernelDiscoveryErr               error
	kernelConfinement                func(bool) kernelruntime.ConfinementEvidence
	dataDirectoryController          *datadir.Controller
	dataDirectorySource              string
	defaultDataDirectory             string
	condaHome                        string
	condaEnvsPath                    string
	configAllowedDomains             []string
	configDeniedDomains              []string
	configNetworkProxy               string
	mcpX509Posture                   func() mcpstdio.TLSPosture
	allowedDomainsMu                 sync.Mutex
	hostGrantKernelMu                sync.Mutex
	hostGrantKernelFences            map[string]bool
	kernelLocalExecMu                sync.Mutex
	kernelLocalExecWaiters           map[string]kernelLocalExecWaiterAuthority
	detachedKernelObserverMu         sync.Mutex
	detachedKernelObservers          map[string]context.CancelFunc
	detachedKernelObserversDone      chan struct{}
	detachedKernelObserversDraining  bool
	kernelOperationBootID            string
	approvalDecisionMu               *sync.Mutex
	restartRuntime                   func(reason string) error
	runtimeUpdate                    *runtimeUpdateController
	hostDirectoryPicker              func(context.Context) (string, error)
	vmRestart                        *vmrestart.Manager
	vmResources                      *vmresources.Controller
	cloudFactory                     cloudstore.ClientFactory
	workspaceStore                   *workspace.Store
	transcriptStore                  *transcriptstore.Repository
	transcriptWebReadModel           *transcriptstore.WebReadModelRepository
	memoryConfig                     memoryconfig.Config
	memoryExtraction                 *memoryExtractionRuntime
	transcriptContractErr            error
	transcriptDeliveryMu             sync.Mutex
	transcriptDeliveryStop           context.CancelFunc
	transcriptDeliveryDone           chan struct{}
	transcriptDeliveryWake           chan struct{}
	dataLifecycleStop                context.CancelFunc
	dataLifecycleDone                chan struct{}
	frameResumeDispatchWakeMu        sync.Mutex
	frameResumeDispatchWake          chan struct{}
	mcpDirectory                     *mcpdirectory.Service
	mcpDirectoryOwned                bool
	mcpApps                          *mcpAppBroker
	backgroundServicesStarted        bool
	scientificRuntimeWarmupMu        sync.RWMutex
	scientificRuntimeWarmups         map[string]scientificRuntimeWarmupStatus
	scientificRuntimeWarmupWake      chan string
	runtimeStartedAt                 time.Time
	mcpDiscoverySlots                chan struct{}
	rcsbFiles                        RCSBFileFetcher
	rcsbSearch                       RCSBStructureSearcher
	publicScientificFiles            PublicScientificFileFetcher
	publicScientificDownloadSlots    chan struct{}
	mcpAppResourceTickets            *mcpAppResourceTicketStore
	workspaceEvents                  *workspaceEventHub
	compatEvents                     *compatEventHub
	httpClient                       *http.Client
	speechClientForURL               func(context.Context, string, *http.Client) (*http.Client, error)
	localSpeechRuntime               *localSpeechRuntime
	webImageClientForURL             func(context.Context, string, *http.Client) (*http.Client, error)
	verifierToken                    string
	feedback                         FeedbackOptions
	feishuDeviceQR                   *adapterfeishu.DeviceQRLoginManager
	wechatQR                         *adapterwechat.QRLoginManager
	messageChannelQRMu               sync.Mutex
	messageChannelQRBindings         map[string]messageChannelQRBinding
	sessionSockets                   *sessionWebSocketHub
	sessionRunsMu                    sync.Mutex
	sessionRuns                      map[string]*activeSessionRun
	sessionRunsDraining              bool
	manualReviewMu                   sync.Mutex
	visualReviewChallengeToken       func() (string, error)
	runtimeComponentsMu              sync.RWMutex
	runtimeComponents                map[string]struct{}
	kernelPeerPendingMu              sync.Mutex
	kernelPeerPending                map[string]int
	kernelPeerReservations           map[string]string
	agentKernelForegroundWaitTimeout time.Duration
	kernelIdleNow                    func() time.Time
	feishuDedup                      *adaptercommon.MessageDedup
	wechatDedup                      *adaptercommon.MessageDedup
	skillCatalog                     *skills.Catalog
	scienceCapabilities              *sciencecapability.Catalog
	skillErrors                      []skills.LoadError
	skillDirectories                 []string
	skillMutationMu                  sync.Mutex
	agentCatalog                     *agentruntime.AgentCatalog
	agentCatalogError                error
	csrfToken                        string
	compactSummarizer                SessionRunnerChatOptions
	runnerDiagnostics                RunnerDiagnostics
	adapterDiagnostics               AdapterDiagnostics
	imOutbound                       adaptercommon.SessionOutboundSink
	transcriptIMMu                   sync.Mutex
	transcriptIMClaims               map[transcriptIMClaimKey]transcriptstore.DeliveryClaim
	transcriptIMRoutes               map[string]string
	imInboundMu                      sync.Mutex
	imInboundLocks                   map[string]*imInboundSessionLock
	imProjectionHook                 func()
	compatRequestMu                  sync.Mutex
	agentToolApprovalMu              sync.Mutex
	sessionSubmissionMu              sync.Mutex
	readStateMu                      sync.Mutex
	readState                        map[string]fileReadSnapshot
	readCursorCache                  canonicalReadCursorCache
	transcriptWebCache               transcriptWebProjectionCache
	streamingCacheMu                 sync.Mutex
	streamingCache                   map[string]*frameStreamingCacheEntry
	projectionRetryMu                sync.Mutex
	projectionRetryAt                map[string]time.Time
	transcriptWebCatchupMu           sync.Mutex
	transcriptWebCatchups            map[transcriptWebCatchupKey]*transcriptWebCatchupFlight
	streamingCacheClock              uint64
	backgroundShellMu                sync.Mutex
	backgroundShells                 map[string]*shellops.RunningCommand
	webResearchMu                    sync.Mutex
	webResearchSessions              map[string]*webResearchSessionState
	webZipMu                         sync.Mutex
	webZipCancels                    map[string]context.CancelFunc
	webSnapshotMu                    sync.Mutex
	webSnapshots                     map[string]*webFSSnapshot
	webPreviewHistoryMu              sync.Mutex
	webOfficeMu                      sync.Mutex
	provenanceCensusCache            provenanceCensusCache
	webOfficeWatches                 map[string]*webOfficeWatch
	webShellLauncher                 webShellLauncher
	webUI                            http.Handler
	httpMetrics                      *observability.HTTPRegistry
}

func New(options Options) *Server {
	runtimeMemoryConfig := memoryconfig.Default()
	if options.MemoryConfig != nil {
		runtimeMemoryConfig = *options.MemoryConfig
	}
	report := options.Capabilities
	if report.Status == "" {
		report = capabilities.CompactSynon()
	}
	link := options.SynonLink
	if link == nil {
		link = synonlink.NewService()
	}
	remoteDialer := options.ComputeRemoteDialer
	if remoteDialer == nil {
		remoteDialer = compute.OpenSSHRemoteDialer{}
	}
	providerOperationRunner := options.ProviderOperationRunner
	hostGPUDetector := options.HostGPUDetector
	if hostGPUDetector == nil {
		hostGPUDetector = compute.DetectHostGPU
	}
	tools := options.Tools
	operations := options.Operations
	registeredWebSearchOptions := options.WebSearchOptions
	if strings.TrimSpace(registeredWebSearchOptions.Root) == "" {
		registeredWebSearchOptions.Root = options.FileRoot
	}
	if tools != nil && operations == nil {
		catalogs := registry.Split(tools)
		tools = catalogs.ModelTools
		operations = catalogs.Operations
	} else if tools == nil || operations == nil {
		catalogs := registry.DefaultCatalogs()
		if tools == nil {
			tools = catalogs.ModelTools
			tools.SetWebOptions(options.WebFetchOptions, registeredWebSearchOptions)
		}
		if operations == nil {
			operations = catalogs.Operations
			operations.SetWebOptions(options.WebFetchOptions, registeredWebSearchOptions)
		}
	}
	tools.AttachServiceOperations(operations)
	plugins := options.Plugins
	if plugins == nil {
		plugins = pluginhost.NewDefault()
	}
	wechatQR := options.WeChatQR
	if wechatQR == nil {
		wechatQR = adapterwechat.NewQRLoginManager("")
	}
	feishuDeviceQR := options.FeishuDeviceQR
	if feishuDeviceQR == nil {
		feishuDeviceQR = adapterfeishu.NewDeviceQRLoginManager("")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	cloudFactory := options.CloudFactory
	if cloudFactory == nil {
		cloudFactory = cloudstore.NewFactory(httpClient)
	}
	mcpDirectory := options.MCPDirectory
	mcpDirectoryOwned := false
	if mcpDirectory == nil && options.Workspace != nil && strings.TrimSpace(options.FileRoot) != "" {
		mcpDirectory = mcpdirectory.New(options.Workspace, options.FileRoot, httpClient)
		mcpDirectoryOwned = true
	}
	if mcpDirectory != nil && options.Workspace != nil {
		mcpDirectory.SetContactEmailProvider(func(userID string) (string, bool, error) {
			decision, found, err := options.Workspace.LatestContactEmailDecision(strings.TrimSpace(userID))
			if err != nil || !found || decision.Decision != workspace.ContactEmailDecisionAllowed ||
				decision.NoticeVersion != contactEmailNoticeVersion() || strings.TrimSpace(decision.Email) == "" {
				return "", false, err
			}
			return strings.TrimSpace(decision.Email), true, nil
		})
	}
	if mcpDirectory != nil {
		mcpDirectory.SetTLSPostureProvider(options.MCPX509Posture)
	}
	skillCatalog, skillDirectories, skillErrors := loadSkillCatalog(options.SkillCatalog, options.SkillDirectories, options.FileRoot)
	agentCatalog, agentCatalogErr := loadBundledAgentCatalog(options, skillCatalog)
	scienceCapabilities := options.ScienceCapabilities
	if scienceCapabilities == nil {
		if embedded, err := sciencecapability.DefaultCatalog(); err == nil {
			scienceCapabilities = &embedded
		}
	}
	var settings *settingsstore.Store
	var secrets *secretstore.Store
	var sessions *sessionstore.Store
	var journal *eventjournal.EventJournal
	var tasks *taskstore.Store
	var taskRuns *taskruns.Store
	var pairing *pairingstore.Store
	runtime := options.RuntimeStore
	runtimeOwned := false
	var usage *runtimecontrol.Scanner
	if options.FileRoot != "" {
		settings = settingsstore.New(filepath.Join(options.FileRoot, "settings.json"))
		secrets = secretstore.New(options.FileRoot)
		sessions = sessionstore.NewStore(options.FileRoot)
		journal = eventjournal.NewEventJournal(options.FileRoot)
		tasks = taskstore.New(filepath.Join(options.FileRoot, "tasks.json"))
		taskRuns = taskruns.NewStore(options.FileRoot)
		pairing = pairingstore.New(filepath.Join(options.FileRoot, "pairing.json"))
		if runtime == nil {
			runtime = runtimekv.New(filepath.Join(options.FileRoot, "runtime-state.sqlite"))
			runtimeOwned = true
		}
		condaHome := strings.TrimSpace(options.CondaHome)
		if condaHome == "" {
			condaHome = runtimecontrol.CondaRoot(options.FileRoot)
		}
		condaEnvsPath := strings.TrimSpace(options.CondaEnvsPath)
		if condaEnvsPath == "" {
			condaEnvsPath = filepath.Join(condaHome, "envs")
		}
		usage = runtimecontrol.NewScannerWithCondaEnvs(
			options.FileRoot, condaHome, condaEnvsPath, 5*time.Minute,
		)
	}
	if mcpDirectory != nil && secrets != nil {
		mcpDirectory.SetBundledAPIKeyProvider(func(userID, connectorID string) (string, bool, error) {
			secret, found, err := secrets.ResolveForUser(bundledMCPAPIKeySecretID(userID, connectorID), userID)
			if err != nil || !found {
				return "", found, err
			}
			value := strings.TrimSpace(secret.Value)
			return value, value != "", nil
		})
	}
	approvalDecisionMu := &sync.Mutex{}
	if settings != nil {
		link.SetPolicyDefaultsProvider(func() synonlink.PolicyDefaults {
			setting, ok, err := settings.Get(approvalDefaultsSettingKey)
			if err != nil || !ok {
				return synonlink.PolicyDefaults{}
			}
			defaults, err := synonlink.NormalizePolicyDefaults(setting.Value)
			if err != nil {
				return synonlink.PolicyDefaults{}
			}
			return defaults
		})
		link.SetRememberedApprovalStore(
			func(userID, clientID, action string) (synonlink.RememberedApprovalDecision, bool) {
				setting, ok, err := settings.Get(approvalRememberedSettingKey)
				if err != nil || !ok {
					return synonlink.RememberedApprovalDecision{}, false
				}
				decisions := rememberedApprovalDecisionsFromSetting(setting.Value)
				decision, ok := decisions[synonlink.RememberedApprovalKey(userID, clientID, action)]
				return decision, ok
			},
			func(decision synonlink.RememberedApprovalDecision) error {
				approvalDecisionMu.Lock()
				defer approvalDecisionMu.Unlock()
				setting, ok, err := settings.Get(approvalRememberedSettingKey)
				if err != nil {
					return err
				}
				decisions := map[string]synonlink.RememberedApprovalDecision{}
				if ok {
					decisions = rememberedApprovalDecisionsFromSetting(setting.Value)
				}
				decisions[synonlink.RememberedApprovalKey(decision.UserID, decision.ClientID, decision.Action)] = decision
				_, err = settings.Set(approvalRememberedSettingKey, rememberedApprovalDecisionsToSetting(decisions))
				return err
			},
		)
	}
	kernelManager := options.KernelManager
	var kernelDiscoveryErr error
	if kernelManager == nil && options.StartBackgroundServices {
		kernelManager, kernelDiscoveryErr = kernelruntime.DiscoverManagerWithPaths(options.CondaHome, options.CondaEnvsPath)
	}
	if providerOperationRunner == nil && kernelManager != nil {
		providerOperationRunner = kernelManager
	}
	if mcpDirectory != nil && kernelManager != nil {
		mcpDirectory.SetBundledPythonCommandProvider(kernelManager.BundledMCPPythonExecutable)
	}
	var kernelConfinement func(bool) kernelruntime.ConfinementEvidence
	if kernelManager != nil {
		kernelConfinement = func(retry bool) kernelruntime.ConfinementEvidence {
			if retry {
				return kernelManager.RetryConfinementEvidence()
			}
			return kernelManager.ConfinementEvidence()
		}
	}
	var dataDirectoryController *datadir.Controller
	if strings.TrimSpace(options.DataDirControlPath) != "" {
		dataDirectoryController = datadir.New(options.DataDirControlPath)
	}
	modalConfigPath := strings.TrimSpace(options.ModalConfigPath)
	if modalConfigPath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			modalConfigPath = filepath.Join(home, ".modal.toml")
		}
	}
	linkAuth := newSynonLinkAuthenticator(options.SynonLinkAuth)
	webAuthState := newWebAuthenticationState(options, linkAuth, httpClient)
	var transcriptContractErr error
	if options.Transcript != nil {
		transcriptContractErr = options.Transcript.ValidateCurrentContract(context.Background())
		if transcriptContractErr == nil {
			_, transcriptContractErr = options.Transcript.ReconcileActiveHistoryRealtimeRebases(context.Background(), 1000)
		}
	}
	server := &Server{
		capabilities:           report,
		synonLink:              link,
		synonLinkAuth:          linkAuth,
		loginLimiter:           newLoginRateLimiter(),
		webAuthenticationState: webAuthState,
		accountManagementReader: newLocalAccountManagementReaderWithConfig(webAuthState, linkAuth, localAccountManagementConfig{
			settings: settings, allowedDomains: options.ConfigAllowedDomains,
			deniedDomains: options.ConfigDeniedDomains, mcpConfigured: mcpDirectory != nil,
		}),
		linkPackagePath:                  options.LinkPackagePath,
		tools:                            tools,
		operations:                       operations,
		plugins:                          plugins,
		fileRoot:                         options.FileRoot,
		runtimeAssetsDir:                 cleanExistingDirectory(options.RuntimeAssetsDir),
		settingsStore:                    settings,
		secretStore:                      secrets,
		sessionStore:                     sessions,
		eventJournal:                     journal,
		taskStore:                        tasks,
		taskRunStore:                     taskRuns,
		pairingStore:                     pairing,
		runtimeStore:                     runtime,
		runtimeStoreOwned:                runtimeOwned,
		usageScanner:                     usage,
		computeRemoteDialer:              remoteDialer,
		computeProviderDial:              options.ComputeProviderDial,
		computeProviderProvisionWake:     make(chan string, 8),
		computeProviderJobWake:           make(chan struct{}, 1),
		providerOperationRunner:          providerOperationRunner,
		computeProviderJobs:              map[string]bool{},
		computeRuns:                      map[string]context.CancelFunc{},
		hostGPUDetector:                  hostGPUDetector,
		kernelManager:                    kernelManager,
		kernelExecutionBackend:           options.KernelExecutionBackend,
		kernelDiscoveryErr:               kernelDiscoveryErr,
		kernelConfinement:                kernelConfinement,
		modalConfigPath:                  modalConfigPath,
		dataDirectoryController:          dataDirectoryController,
		dataDirectorySource:              options.DataDirSource,
		defaultDataDirectory:             options.DefaultDataDir,
		condaHome:                        options.CondaHome,
		condaEnvsPath:                    options.CondaEnvsPath,
		configAllowedDomains:             append([]string(nil), options.ConfigAllowedDomains...),
		configDeniedDomains:              append([]string(nil), options.ConfigDeniedDomains...),
		configNetworkProxy:               strings.TrimSpace(options.ConfigNetworkProxy),
		mcpX509Posture:                   options.MCPX509Posture,
		hostGrantKernelFences:            map[string]bool{},
		detachedKernelObservers:          map[string]context.CancelFunc{},
		detachedKernelObserversDone:      nil,
		kernelOperationBootID:            newCSRFToken(),
		approvalDecisionMu:               approvalDecisionMu,
		restartRuntime:                   options.RestartRuntime,
		runtimeUpdate:                    newRuntimeUpdateController(options.RuntimeUpdate),
		hostDirectoryPicker:              options.HostDirectoryPicker,
		vmRestart:                        options.VMRestart,
		vmResources:                      options.VMResources,
		cloudFactory:                     cloudFactory,
		workspaceStore:                   options.Workspace,
		transcriptStore:                  options.Transcript,
		transcriptWebReadModel:           options.TranscriptWebReadModel,
		memoryConfig:                     runtimeMemoryConfig,
		transcriptContractErr:            transcriptContractErr,
		workspaceEvents:                  newWorkspaceEventHub(),
		compatEvents:                     newCompatEventHub(),
		mcpDirectory:                     mcpDirectory,
		mcpDirectoryOwned:                mcpDirectoryOwned,
		mcpApps:                          newMCPAppBroker(),
		backgroundServicesStarted:        options.StartBackgroundServices,
		scientificRuntimeWarmups:         map[string]scientificRuntimeWarmupStatus{},
		scientificRuntimeWarmupWake:      make(chan string, len(scientificRuntimeWarmupDefinitions())),
		runtimeStartedAt:                 time.Now().UTC(),
		mcpDiscoverySlots:                processMCPDiscoverySlots,
		rcsbFiles:                        defaultRCSBFileFetcher(options.RCSBFiles),
		rcsbSearch:                       defaultRCSBStructureSearcher(options.RCSBSearch),
		publicScientificFiles:            defaultPublicScientificFileFetcher(options.PublicScientificFiles, options.ConfigNetworkProxy),
		publicScientificDownloadSlots:    make(chan struct{}, agentPublicScientificDownloadConcurrency),
		httpClient:                       httpClient,
		speechClientForURL:               mcpdirectory.SecureHTTPClient,
		localSpeechRuntime:               newLocalSpeechRuntime(options.FileRoot, httpClient),
		webImageClientForURL:             mcpdirectory.SecureHTTPClient,
		verifierToken:                    strings.TrimSpace(options.VerifierToken),
		feedback:                         options.Feedback,
		feishuDeviceQR:                   feishuDeviceQR,
		wechatQR:                         wechatQR,
		messageChannelQRBindings:         map[string]messageChannelQRBinding{},
		sessionSockets:                   newSessionWebSocketHub(),
		sessionRuns:                      map[string]*activeSessionRun{},
		agentKernelForegroundWaitTimeout: defaultAgentKernelForegroundWaitTimeout,
		kernelIdleNow:                    time.Now,
		runtimeComponents:                map[string]struct{}{},
		feishuDedup:                      adaptercommon.NewMessageDedup(30*time.Minute, 20000),
		wechatDedup:                      adaptercommon.NewMessageDedup(30*time.Minute, 20000),
		skillCatalog:                     skillCatalog,
		scienceCapabilities:              scienceCapabilities,
		skillErrors:                      skillErrors,
		skillDirectories:                 skillDirectories,
		agentCatalog:                     agentCatalog,
		agentCatalogError:                agentCatalogErr,
		csrfToken:                        newCSRFToken(),
		compactSummarizer:                options.CompactSummarizer,
		runnerDiagnostics:                options.RunnerDiagnostics,
		adapterDiagnostics:               options.AdapterDiagnostics,
		imOutbound:                       options.IMOutbound,
		transcriptIMClaims:               map[transcriptIMClaimKey]transcriptstore.DeliveryClaim{},
		transcriptIMRoutes:               map[string]string{},
		imInboundLocks:                   map[string]*imInboundSessionLock{},
		readState:                        map[string]fileReadSnapshot{},
		backgroundShells:                 map[string]*shellops.RunningCommand{},
		webResearchSessions:              map[string]*webResearchSessionState{},
		webZipCancels:                    map[string]context.CancelFunc{},
		webSnapshots:                     map[string]*webFSSnapshot{},
		webOfficeWatches:                 map[string]*webOfficeWatch{},
		mcpAppResourceTickets:            newMCPAppResourceTicketStore(),
		webUI:                            options.WebUI,
		httpMetrics:                      observability.NewHTTPRegistry(),
	}
	if mcpDirectory != nil {
		mcpDirectory.SetConnectorUsageRecorder(func(ctx context.Context, userID string, connector mcpdirectory.RuntimeConnector, toolName string, callErr error) {
			server.recordMCPConnectorInvocation(ctx, userID, connector.ID, connector.Source, connector.Name, toolName, callErr)
		})
	}
	if options.StartBackgroundServices {
		server.bindKernelStdoutRealtime(kernelManager)
	}
	if server.workspaceStore != nil {
		server.memoryExtraction = newMemoryExtractionRuntime(server)
	}
	if rules, err := server.loadStorageRules(); err == nil {
		server.configureStorageScanner(rules)
	} else if server.usageScanner != nil {
		log.Printf("storage rules scanner configuration failed: %T", err)
	}
	if err := server.compactToolGatewayAudits(); err != nil {
		log.Printf("tool gateway audit compaction failed: %T", err)
	}
	if options.StartBackgroundServices {
		server.recoverManagedEnvironmentObservations()
		server.startTranscriptWebDelivery()
		server.startDataLifecycle()
	}
	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerKernelRoutes(mux)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/api/go/diagnostics/runtime", s.handleRuntimeDiagnostics)
	mux.HandleFunc("/api/go/diagnostics/outbox/dead-letters", s.handleOutboxDeadLetters)
	mux.HandleFunc("/api/go/diagnostics/outbox/dead-letters/", s.handleOutboxDeadLetterMutation)
	mux.HandleFunc("/api/me", s.handleCurrentUser)
	mux.HandleFunc("/api/auth/user", s.handleWebCurrentUser)
	mux.HandleFunc("/api/auth/providers", s.handleWebAuthProviders)
	mux.HandleFunc("/api/auth/oidc/", s.handleWebOIDC)
	mux.HandleFunc("/api/auth/oauth/wechat/", s.handleWebWeChatOAuth)
	mux.HandleFunc("/api/auth/register", s.handleWebRegister)
	mux.HandleFunc("/api/account/overview", s.handleAccountOverview)
	mux.HandleFunc("/api/account/management", s.handleAccountManagement)
	mux.HandleFunc("/api/account/security", s.handleWebAccountSecurity)
	mux.HandleFunc("/api/account/security/", s.handleWebAccountSecurity)
	mux.HandleFunc("/api/settings/client", s.handleWebClientSettings)
	mux.HandleFunc("/api/stt", s.handleWebSpeechToText)
	mux.HandleFunc("/api/stt/local/status", s.handleLocalSpeechStatus)
	mux.HandleFunc("/api/stt/local/prepare", s.handleLocalSpeechPrepare)
	mux.HandleFunc("/api/assistants", s.handleWebAssistants)
	mux.HandleFunc("/api/assistants/", s.handleWebAssistants)
	mux.HandleFunc("/api/conversations", s.handleWebConversations)
	mux.HandleFunc("/api/conversations/", s.handleWebConversation)
	mux.HandleFunc("/api/contact-email", s.handleContactEmail)
	mux.HandleFunc("/api/synonbiomed/licenses/third-party", s.handleThirdPartyLicenses)
	mux.HandleFunc("/api/synonbiomed/experts", s.handleWebManagedExperts)
	mux.HandleFunc("/api/synonbiomed/catalog", s.handleSynonBiomedCatalog)
	mux.HandleFunc("/api/synonbiomed/projects", s.handleSynonBiomedProjects)
	mux.HandleFunc("/api/system/info", s.handleSystemInfo)
	mux.HandleFunc("/api/system/provenance-census", s.handleProvenanceCensus)
	mux.HandleFunc("/api/fs/dir", s.handleWebFS)
	mux.HandleFunc("/api/fs/list", s.handleWebFS)
	mux.HandleFunc("/api/fs/image-base64", s.handleWebFS)
	mux.HandleFunc("/api/fs/fetch-remote-image", s.handleWebFS)
	mux.HandleFunc("/api/fs/read", s.handleWebFS)
	mux.HandleFunc("/api/fs/read-buffer", s.handleWebFS)
	mux.HandleFunc("/api/fs/temp", s.handleWebFS)
	mux.HandleFunc("/api/fs/write", s.handleWebFS)
	mux.HandleFunc("/api/fs/metadata", s.handleWebFS)
	mux.HandleFunc("/api/fs/upload", s.handleWebFSUpload)
	mux.HandleFunc("/api/fs/copy", s.handleWebFS)
	mux.HandleFunc("/api/fs/remove", s.handleWebFS)
	mux.HandleFunc("/api/fs/rename", s.handleWebFS)
	mux.HandleFunc("/api/fs/zip", s.handleWebZip)
	mux.HandleFunc("/api/fs/zip/cancel", s.handleWebZip)
	mux.HandleFunc("/api/fs/snapshot/init", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/compare", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/baseline", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/info", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/dispose", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/stage", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/stage-all", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/unstage", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/unstage-all", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/discard", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/reset", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/fs/snapshot/branches", s.handleWebFSSnapshot)
	mux.HandleFunc("/api/shell/open-file", s.handleWebShell)
	mux.HandleFunc("/api/shell/show-item-in-folder", s.handleWebShell)
	mux.HandleFunc("/api/shell/open-external", s.handleWebShell)
	mux.HandleFunc("/api/shell/check-tool-installed", s.handleWebShell)
	mux.HandleFunc("/api/shell/open-folder-with", s.handleWebShell)
	mux.HandleFunc("/api/preview-history/list", s.handleWebPreviewHistory)
	mux.HandleFunc("/api/preview-history/save", s.handleWebPreviewHistory)
	mux.HandleFunc("/api/preview-history/get-content", s.handleWebPreviewHistory)
	mux.HandleFunc("/api/document/convert", s.handleWebDocumentConvert)
	mux.HandleFunc("/api/word-preview/start", s.handleWebOfficePreview)
	mux.HandleFunc("/api/word-preview/stop", s.handleWebOfficePreview)
	mux.HandleFunc("/api/excel-preview/start", s.handleWebOfficePreview)
	mux.HandleFunc("/api/excel-preview/stop", s.handleWebOfficePreview)
	mux.HandleFunc("/api/ppt-preview/start", s.handleWebOfficePreview)
	mux.HandleFunc("/api/ppt-preview/stop", s.handleWebOfficePreview)
	mux.HandleFunc("/api/office-watch-proxy", s.handleWebOfficeProxy)
	mux.HandleFunc("/api/office-watch-proxy/", s.handleWebOfficeProxy)
	mux.HandleFunc("/api/ppt-proxy", s.handleWebOfficeProxy)
	mux.HandleFunc("/api/ppt-proxy/", s.handleWebOfficeProxy)
	mux.HandleFunc("/api/assets/logos/", s.handleWebLogoAsset)
	mux.HandleFunc("/api/environments/status", s.handleEnvironmentStatus)
	mux.HandleFunc("/api/environments/retry", s.handleEnvironmentRetry)
	mux.HandleFunc("/api/auth/login", s.handleSynonLinkLogin)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	mux.HandleFunc("/login", s.handleWebLogin)
	mux.HandleFunc("/register", s.handleWebRegister)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/synon-link/ws", s.handleSynonLinkCompatibilityWebSocket)
	mux.HandleFunc("/api/feedback/available", s.handleFeedbackAvailability)
	mux.HandleFunc("/api/feedback/safety", s.handleSafetyFeedback)
	mux.HandleFunc("/api/feedback", s.handleFeedback)
	mux.HandleFunc("/api/credentials/github/host-probe", s.handleHostGitHubCredentialProbe)
	mux.HandleFunc("/api/credentials/github/use-host", s.handleHostGitHubCredentialUse)
	mux.HandleFunc("/api/contracts/summary", s.handleContractSummary)
	mux.HandleFunc("/api/contracts/service-methods", s.handleContractServiceMethods)
	mux.HandleFunc("/api/contracts/http-routes", s.handleContractHTTPRoutes)
	mux.HandleFunc("/api/contracts/events", s.handleContractEvents)
	mux.HandleFunc("/api/contracts/query-keys", s.handleContractQueryKeys)
	mux.HandleFunc("/api/contracts/domains", s.handleContractDomains)
	mux.HandleFunc("/api/go/settings/allowed-domains", s.handleRuntimeAllowedDomains)
	mux.HandleFunc("/api/preferences/allowed-domains", s.handleCompatibilityAllowedDomains)
	mux.HandleFunc("/api/preferences/allowed-domains/", s.handleCompatibilityAllowedDomains)
	mux.HandleFunc("/api/approvals/grants", s.handleApprovalGrantsCompatibility)
	mux.HandleFunc("/api/approvals/grants/all", s.handleApprovalGrantsBulkCompatibility)
	mux.HandleFunc("/api/preferences/builtin-allowlist", s.handleBuiltinAllowlist)
	mux.HandleFunc("/api/preferences/builtin-allowlist/disabled", s.handleBuiltinAllowlist)
	mux.HandleFunc("/api/preferences/builtin-allowlist/disabled-groups", s.handleBuiltinAllowlist)
	mux.HandleFunc("/api/preferences/builtin-allowlist/onboarding-seen", s.handleBuiltinAllowlist)
	mux.HandleFunc("/api/go/preferences/builtin-allowlist", s.handleBuiltinAllowlist)
	mux.HandleFunc("/api/go/preferences/builtin-allowlist/disabled", s.handleBuiltinAllowlist)
	mux.HandleFunc("/api/go/preferences/builtin-allowlist/disabled-groups", s.handleBuiltinAllowlist)
	mux.HandleFunc("/api/go/preferences/builtin-allowlist/onboarding-seen", s.handleBuiltinAllowlist)
	mux.HandleFunc("/api/go/settings/first-run", s.handleRuntimeFirstRun)
	mux.HandleFunc("/api/go/settings/use-intent", s.handleRuntimeUseIntent)
	mux.HandleFunc("/api/preferences/first-run-onboarding", s.handleCompatibilityFirstRun)
	mux.HandleFunc("/api/preferences/first-run-onboarding/complete", s.handleCompatibilityFirstRunComplete)
	mux.HandleFunc("/api/preferences/scientific-runtimes", s.handleScientificRuntimeWarmups)
	mux.HandleFunc("/api/preferences/use-intent", s.handleCompatibilityUseIntent)
	mux.HandleFunc("/api/go/settings/ambient-backdrop", s.handleRuntimeAmbientBackdrop)
	mux.HandleFunc("/api/go/secrets", s.handleSecrets)
	mux.HandleFunc("/api/go/secrets/", s.handleSecret)
	mux.HandleFunc("/api/secrets", s.handleCompatibilitySecrets)
	mux.HandleFunc("/api/secrets/", s.handleCompatibilitySecretMutation)
	mux.HandleFunc("/api/cloud-credentials", s.handleCloudCredentials)
	mux.HandleFunc("/api/cloud-credentials/", s.handleCloudCredential)
	mux.HandleFunc("/api/go/preferences/host-grants", s.handleHostGrants)
	mux.HandleFunc("/api/go/preferences/host-grants/picker", s.handleHostGrantPicker)
	mux.HandleFunc("/api/preferences/host-grants", s.handleCompatibilityHostGrants)
	mux.HandleFunc("/api/preferences/host-grants/picker", s.handleCompatibilityHostGrantPicker)
	mux.HandleFunc("/api/preferences/host-home", s.handleCompatibilityHostHome)
	mux.HandleFunc("/api/preferences/host-browse", s.handleCompatibilityHostBrowse)
	mux.HandleFunc("/api/go/preferences/host-home", s.handleHostHome)
	mux.HandleFunc("/api/go/preferences/host-browse", s.handleHostBrowse)
	mux.HandleFunc("/api/go/preferences/import-bundle", s.handleSkillBundleImport)
	mux.HandleFunc("/api/preferences/import-bundle", s.handleSkillBundleImport)
	mux.HandleFunc("/api/skills", s.handleWebSkills)
	mux.HandleFunc("/api/skills/import", s.handleSkillBundleImport)
	mux.HandleFunc("/api/skills/from-conversation", s.handleSkillFromConversation)
	mux.HandleFunc("/api/skills/materialize-for-agent", s.handleWebSkillMaterialize)
	mux.HandleFunc("/api/skills/info", s.handleWebSkillInfo)
	mux.HandleFunc("/api/skills/scan", s.handleWebSkillScan)
	mux.HandleFunc("/api/skills/detect-paths", s.handleWebSkillDetectPaths)
	mux.HandleFunc("/api/skills/detect-external", s.handleWebSkillDetectExternal)
	mux.HandleFunc("/api/skills/import-history", s.handleWebSkillImportHistory)
	mux.HandleFunc("/api/skills/import-limits", s.handleWebSkillImportLimits)
	mux.HandleFunc("/api/skills/paths", s.handleWebSkillPaths)
	mux.HandleFunc("/api/skills/external-paths", s.handleWebSkillExternalPaths)
	mux.HandleFunc("/api/skills/builtin-rule", s.handleWebBuiltinRule)
	mux.HandleFunc("/api/skills/builtin-skill", s.handleWebBuiltinSkill)
	mux.HandleFunc("/api/marketplace/preview", s.handleWebSkillMarketplacePreview)
	mux.HandleFunc("/api/marketplace/import", s.handleWebSkillMarketplaceImport)
	mux.HandleFunc("/api/marketplace/sources", s.handleWebSkillMarketplaceSources)
	mux.HandleFunc("/api/marketplace/sources/", s.handleWebSkillMarketplaceSources)
	mux.HandleFunc("/api/skills/assistant-rule/read", s.handleWebAssistantRules)
	mux.HandleFunc("/api/skills/assistant-rule/write", s.handleWebAssistantRules)
	mux.HandleFunc("/api/skills/assistant-rule/", s.handleWebAssistantRules)
	mux.HandleFunc("/api/preferences/running-frames", s.handleRunningFrameCount)
	mux.HandleFunc("/api/preferences/disk-usage", s.handleDiskUsage)
	mux.HandleFunc("/api/preferences/disk-usage/conda", s.handleCondaDiskUsage)
	mux.HandleFunc("/api/go/preferences/running-frames", s.handleRunningFrameCount)
	mux.HandleFunc("/api/go/preferences/disk-usage", s.handleDiskUsage)
	mux.HandleFunc("/api/go/preferences/disk-usage/conda", s.handleCondaDiskUsage)
	mux.HandleFunc("/api/preferences/vm-resources", s.handleVMResources)
	mux.HandleFunc("/api/preferences/vm-resources/restart", s.handleVMRestart)
	mux.HandleFunc("/api/go/preferences/vm-resources", s.handleVMResources)
	mux.HandleFunc("/api/go/preferences/vm-resources/restart", s.handleVMRestart)
	mux.HandleFunc("/api/settings/data-dir", s.handleDataDirectory)
	mux.HandleFunc("/api/settings/data-dir/last-move", s.handleDataDirectoryLastMove)
	mux.HandleFunc("/api/settings/storage-rules", s.handleStorageRules)
	mux.HandleFunc("/api/status/update", s.handleRuntimeUpdate)
	mux.HandleFunc("/api/status/update/", s.handleRuntimeUpdate)
	mux.HandleFunc("/api/go/settings/data-dir", s.handleDataDirectory)
	mux.HandleFunc("/api/go/settings/data-dir/last-move", s.handleDataDirectoryLastMove)
	mux.HandleFunc("/api/go/settings/storage-rules", s.handleStorageRules)
	mux.HandleFunc("/api/system/refresh-kernels", s.handleRefreshKernels)
	mux.HandleFunc("/api/go/preferences/kernels/refresh", s.handleRefreshKernels)
	mux.HandleFunc("/api/projects", s.handleProjectsCompatibility)
	mux.HandleFunc("/api/projects/processing-counts", s.handleCompatibilityProcessingCounts)
	mux.HandleFunc("/api/projects/", s.handleProjectAttachments)
	mux.HandleFunc("/api/benches/", s.handleCompatibilityBenchUpdate)
	mux.HandleFunc("/api/attachments/", s.handleAttachment)
	mux.HandleFunc("/api/artifacts/download", s.handleArtifactDownloadSelection)
	mux.HandleFunc("/api/artifacts/upload/", s.handleArtifactUpload)
	s.registerWorkspaceArtifactReadCompatibilityRoutes(mux)
	mux.HandleFunc("/api/annotations/", s.handleAnnotationRecord)
	mux.HandleFunc("/api/token-classes", s.handleCompatibilityTokenClasses)
	mux.HandleFunc("/api/frames", s.handleFramesCompatibility)
	mux.HandleFunc("/api/frames/", s.handleFrameCompatibility)
	mux.HandleFunc("/api/request", s.handleSubmitRequestCompatibility)
	mux.HandleFunc("/api/go/projects", s.handleWorkspaceProjects)
	mux.HandleFunc("/api/go/projects/", s.handleWorkspaceProject)
	mux.HandleFunc("/api/go/benches/", s.handleWorkspaceBench)
	mux.HandleFunc("/api/go/frames/", s.handleWorkspaceFrame)
	mux.HandleFunc("/api/go/requests", s.handleRuntimeRequestSubmission)
	mux.HandleFunc("/api/go/sessions/", s.handleRuntimeSessionConfig)
	mux.HandleFunc("/api/go/agents", s.handleWorkspaceAgents)
	mux.HandleFunc("/api/go/agents/", s.handleWorkspaceAgent)
	mux.HandleFunc("/api/csrf", s.handleCSRF)
	mux.HandleFunc("/api/agents", s.handleAgentCompatibility)
	mux.HandleFunc("/api/agents/", s.handleAgentProfileCompatibility)
	mux.HandleFunc("/api/synonbiomed/expert-profiles", s.handleSynonBiomedExpertProfiles)
	mux.HandleFunc("/api/synonbiomed/expert-profiles/", s.handleSynonBiomedExpertProfiles)
	mux.HandleFunc("/api/compute/inference-providers", s.handleInferenceProviders)
	mux.HandleFunc("/api/compute/inference-providers/", s.handleInferenceProvider)
	mux.HandleFunc("/api/compute/providers", s.handleComputeProviders)
	mux.HandleFunc("/api/compute/providers/", s.handleComputeProvider)
	mux.HandleFunc("/api/go/artifacts/", s.handleWorkspaceArtifact)
	s.registerComputeWorkbenchRoutes(mux)
	mux.HandleFunc("/api/go/artifact-versions/", s.handleWorkspaceArtifactVersion)
	mux.HandleFunc("/api/go/folders/", s.handleWorkspaceFolder)
	mux.HandleFunc("/api/notes/", s.handleCompatibilityNoteMutation)
	mux.HandleFunc("/api/go/notes/", s.handleWorkspaceNote)
	mux.HandleFunc("/api/go/events", s.handleWorkspaceEvents)
	mux.HandleFunc("/api/go/events/stream", s.handleWorkspaceEventStream)
	mux.HandleFunc("/api/events", s.handleCompatEvents)
	mux.HandleFunc("/api/events/stream", s.handleCompatEventStream)
	mux.HandleFunc("/api/events/ws", s.handleCompatEventWebSocket)
	mux.HandleFunc("/api/ws", s.handleCompatEventWebSocket)
	mux.HandleFunc("/ws/events", s.handleCompatEventWebSocket)
	mux.HandleFunc("/api/go/memories", s.handleWorkspaceMemories)
	mux.HandleFunc("/api/go/memories/recall", s.handleWorkspaceMemoryRecall)
	mux.HandleFunc("/api/go/memories/", s.handleWorkspaceMemory)
	mux.HandleFunc("/api/memories", s.handleMemoryCompatibility)
	mux.HandleFunc("/api/memories/", s.handleMemoryRecordCompatibility)
	mux.HandleFunc("/api/memory/context", s.handleMemoryContextCompatibility)
	mux.HandleFunc("/api/memory/enabled", s.handleMemoryEnabledCompatibility)
	mux.HandleFunc("/api/memory/auto-enabled", s.handleMemoryAutoExtractionEnabledCompatibility)
	mux.HandleFunc("/api/memory/categories", s.handleMemoryCategoriesCompatibility)
	mux.HandleFunc("/api/memory/categories/", s.handleMemoryCategoryCompatibility)
	mux.HandleFunc("/api/memory/sessions/", s.handleMemorySessionCompatibility)
	mux.HandleFunc("/api/go/routines", s.handleWorkspaceRoutines)
	mux.HandleFunc("/api/go/routines/", s.handleWorkspaceRoutine)
	mux.HandleFunc("/api/go/skills", s.handleWorkspaceSkills)
	mux.HandleFunc("/api/go/skills/", s.handleWorkspaceSkill)
	mux.HandleFunc("/api/skills/catalog", s.handleCompatibilitySkillCatalog)
	mux.HandleFunc("/api/skills/catalog/", s.handleCompatibilityCatalogSkill)
	mux.HandleFunc("/api/skills/usage", s.handleCompatibilitySkillUsage)
	mux.HandleFunc("/api/skills/drafts", s.handleCompatibilitySkillDrafts)
	mux.HandleFunc("/api/skills/", s.handleCompatibilitySkillMutation)
	mux.HandleFunc("/api/go/mcp/servers", s.handleWorkspaceMCPServers)
	mux.HandleFunc("/api/go/mcp/servers/", s.handleWorkspaceMCPServer)
	mux.HandleFunc("/api/go/mcp/attachments/counts", s.handleWorkspaceMCPAttachmentCounts)
	mux.HandleFunc("/api/go/mcp/connectors", s.handleWorkspaceMCPConnectors)
	mux.HandleFunc("/api/llm/providers", s.handleLLMProviders)
	mux.HandleFunc("/api/llm/providers/", s.handleLLMProvider)
	mux.HandleFunc("/api/llm/test", s.handleLLMProviderTest)
	mux.HandleFunc("/api/mcp-servers", s.handleMCPServerCompatibility)
	mux.HandleFunc("/api/mcp-servers/", s.handleMCPServerCompatibility)
	mux.HandleFunc("/api/mcp-servers/connectors", s.handleMCPDirectory)
	mux.HandleFunc("/api/mcp-servers/optional", s.handleMCPOptional)
	mux.HandleFunc("/api/mcp-servers/optional/", s.handleMCPOptional)
	mux.HandleFunc("/api/mcp-servers/directory-health", s.handleMCPDirectory)
	mux.HandleFunc("/api/mcp-servers/reconcile", s.handleMCPDirectory)
	mux.HandleFunc("/api/mcp-servers/marketplace", s.handleMCPMarketplace)
	mux.HandleFunc("/api/mcp-servers/directory", s.handleMCPDirectory)
	mux.HandleFunc("/api/mcp-servers/connectors/", s.handleMCPDirectory)
	mux.HandleFunc("/api/mcp/", s.handleWebMCPBridge)
	mux.HandleFunc("/api/mcp/apps/tool-call", s.handleMCPAppToolCall)
	mux.HandleFunc("/api/mcp/apps/registrations", s.handleMCPAppRegistrations)
	mux.HandleFunc("/api/mcp/apps/requests", s.handleMCPAppRequests)
	mux.HandleFunc("/api/mcp/apps/results", s.handleMCPAppResults)
	mux.HandleFunc("/api/mcp-apps/viewer-bindings", s.handleMCPAppViewerBindings)
	mux.HandleFunc("/api/mcp/apps/resource-tickets", s.handleMCPAppResourceTickets)
	mux.HandleFunc("/mcp-app-resource", s.handleMCPAppSandboxResource)
	mux.HandleFunc("/api/mcp/apps/pin", s.handleMCPAppPin)
	mux.HandleFunc("/api/models", s.handleWorkspaceModels)
	mux.HandleFunc("/v1/models", s.handleWorkspaceModels)
	mux.HandleFunc("/api/plugins/synon/capabilities", s.handleCapabilities)
	mux.HandleFunc("/api/plugins/synon/link/download", s.handleSynonLinkDownload)
	mux.HandleFunc("/api/plugins", s.handlePlugins)
	mux.HandleFunc("/api/plugins/", s.handlePlugins)
	mux.HandleFunc("/api/synon-link", s.handleSynonLink)
	mux.HandleFunc("/api/synon-link/", s.handleSynonLink)
	mux.HandleFunc("/api/adapters/feishu/event", s.handleFeishuEvent)
	mux.HandleFunc("/api/adapters/wechat/event", s.handleWeChatEvent)
	mux.HandleFunc("/api/adapters/message-channels", s.handleMessageChannelStatuses)
	mux.HandleFunc("/api/adapters/message-channels/unpair", s.handleMessageChannelUnpair)
	mux.HandleFunc("/api/adapters/feishu/qr/start", s.handleFeishuQRStart)
	mux.HandleFunc("/api/adapters/feishu/qr/poll", s.handleFeishuQRPoll)
	mux.HandleFunc("/api/adapters/wechat/qr/start", s.handleWeChatQRStart)
	mux.HandleFunc("/api/adapters/wechat/qr/poll", s.handleWeChatQRPoll)
	mux.HandleFunc("/api/tools", s.handleTools)
	mux.HandleFunc("/api/tools/", s.handleTools)
	mux.HandleFunc("/ws/", s.handleSessionWebSocket)
	if s.webUI != nil {
		mux.Handle("/", s.webUI)
	}
	return s.httpMetrics.Wrap(withBrowserOriginPolicy(s.withWebSessionMigration(s.withWebIdentity(s.withAPIAuthentication(s.withBrowserCSRF(mux))))))
}
