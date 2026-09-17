package kernel

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	providerKernelHandshakeTimeout = 15 * time.Second
	providerKernelMaxAuthBytes     = 64 << 10
)

type ProviderRuntimeSpec struct {
	ProviderID       string
	Environment      string
	BootstrapPath    string
	EntrypointPath   string
	ProviderPath     string
	EnvironmentsPath string
	InstallID        string
	OrganizationID   string
	ModalEnvironment string
	AppName          string
	PriorAppNames    []string
	Credentials      map[string]string
	EgressRules      []ProviderEgressRule
	ExtraEnvironment map[string]string
	Dial             ProviderProxyDialFunc
	Prelude          string
}

type providerKernelState struct {
	worker     *Worker
	providerID string
	configHash string
	auth       net.Conn
	proxy      *providerProxyMux
	done       chan struct{}
	once       sync.Once
}

func (m *Manager) StartProviderSession(ctx context.Context, spec SessionSpec, runtimeSpec ProviderRuntimeSpec) (*Worker, string, error) {
	if m == nil {
		return nil, "", errors.New("provider kernel manager is unavailable")
	}
	if err := m.Verify(); err != nil {
		return nil, "", err
	}
	spec = normalizeSessionSpec(spec)
	spec.KernelKind = "provider"
	spec.Language = "python"
	spec.Environment = strings.TrimSpace(runtimeSpec.Environment)
	if err := validateSessionSpec(spec); err != nil {
		return nil, "", err
	}
	normalized, configHash, err := m.normalizeProviderRuntimeSpec(runtimeSpec)
	if err != nil {
		return nil, "", err
	}

	m.providerMu.Lock()
	if existing := m.providerWorkers[spec.KernelID]; existing != nil {
		if existing.providerID == normalized.ProviderID && existing.configHash == configHash && existing.worker.TerminalError() == nil {
			worker := existing.worker
			m.providerMu.Unlock()
			return worker, configHash, nil
		}
		delete(m.providerWorkers, spec.KernelID)
		m.providerMu.Unlock()
		existing.close(context.Background())
		return m.StartProviderSession(ctx, spec, runtimeSpec)
	}
	defer m.providerMu.Unlock()

	authParentFile, authChildFile, err := newProviderRuntimeSocketPair("provider-auth")
	if err != nil {
		return nil, "", err
	}
	proxyParentFile, proxyChildFile, err := newProviderRuntimeSocketPair("provider-proxy")
	if err != nil {
		_ = authParentFile.Close()
		_ = authChildFile.Close()
		return nil, "", err
	}
	cleanupFiles := func() {
		for _, file := range []*os.File{authParentFile, authChildFile, proxyParentFile, proxyChildFile} {
			if file != nil {
				_ = file.Close()
			}
		}
	}

	authConnection, err := net.FileConn(authParentFile)
	if err != nil {
		cleanupFiles()
		return nil, "", errors.New("provider credential channel is unavailable")
	}
	_ = authParentFile.Close()
	authParentFile = nil
	proxyConnection, err := net.FileConn(proxyParentFile)
	if err != nil {
		_ = authConnection.Close()
		cleanupFiles()
		return nil, "", errors.New("provider proxy channel is unavailable")
	}
	_ = proxyParentFile.Close()
	proxyParentFile = nil
	proxy, err := startProviderProxyMux(proxyConnection, normalized.EgressRules, normalized.Dial)
	if err != nil {
		_ = authConnection.Close()
		cleanupFiles()
		return nil, "", err
	}

	prefix, python, err := m.managedEnvironmentRuntime(normalized.Environment, "python")
	if err != nil {
		_ = proxy.Close()
		_ = authConnection.Close()
		cleanupFiles()
		return nil, "", fmt.Errorf("resolve provider environment %q: %w", normalized.Environment, err)
	}
	mounts := []WorkerMount{
		TrustedReadOnlyDirectoryMount(filepath.Dir(filepath.Dir(normalized.EntrypointPath))),
		TrustedReadOnlyDirectoryMount(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(normalized.EntrypointPath))), "kernels")),
		TrustedReadOnlyDirectoryMount(filepath.Dir(normalized.ProviderPath)),
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Path < mounts[j].Path })
	authFD := 4 + len(mounts)
	proxyFD := authFD + 1
	hostNetworkNamespace, err := providerHostNetworkNamespaceInode()
	if err != nil {
		_ = proxy.Close()
		_ = authConnection.Close()
		cleanupFiles()
		return nil, "", err
	}
	environment := m.runtimeEnvironmentAtPrefix(normalized.Environment, "python", spec.WorkspaceDir, spec.KernelID, "", "", "", prefix)
	environment = mergeProviderKernelEnvironment(environment, map[string]string{
		"SYNON_PROVIDER_AUTH_FD":       strconv.Itoa(authFD),
		"SYNON_PROVIDER_PROXY_FD":      strconv.Itoa(proxyFD),
		"SYNON_PROVIDER_CONFIG_SHA256": configHash,
		"OPERON_BYOC_ENVS_DIR":         normalized.EnvironmentsPath,
		"OPERON_BYOC_INSTALL_ID":       normalized.InstallID,
		"OPERON_BYOC_ORG_ID":           normalized.OrganizationID,
		"OPERON_BYOC_FRAME_ID":         spec.FrameID,
		"OPERON_HOST_NETNS_INO":        hostNetworkNamespace,
		"MODAL_ENVIRONMENT":            normalized.ModalEnvironment,
		"OPERON_BYOC_ENVIRONMENT":      normalized.ModalEnvironment,
	}, normalized.ExtraEnvironment)
	worker, err := m.startWorkerWithAuxiliary(
		spec.KernelID, spec.WorkspaceDir, python,
		[]string{"-I", normalized.BootstrapPath, "repl", normalized.EntrypointPath, normalized.ProviderPath},
		environment, mounts, spec.ProtectedPaths, "", "python", []*os.File{authChildFile, proxyChildFile},
	)
	authChildFile, proxyChildFile = nil, nil
	cleanupFiles()
	if err != nil {
		_ = proxy.Close()
		_ = authConnection.Close()
		return nil, "", err
	}
	if err := completeProviderKernelHandshake(authConnection, normalized.Credentials); err != nil {
		_ = worker.Close(context.Background())
		_ = proxy.Close()
		_ = authConnection.Close()
		if terminal := worker.TerminalError(); terminal != nil {
			return nil, "", fmt.Errorf("%w: %v", err, terminal)
		}
		return nil, "", err
	}
	if normalized.Prelude != "" {
		if ctx == nil {
			ctx = context.Background()
		}
		response, executeErr := worker.Execute(ctx, normalized.Prelude, "system")
		if executeErr != nil || response.Error != "" {
			_ = worker.Close(context.Background())
			_ = proxy.Close()
			_ = authConnection.Close()
			if executeErr != nil {
				return nil, "", fmt.Errorf("provider kernel preload failed: %w", executeErr)
			}
			return nil, "", fmt.Errorf("provider kernel preload failed: %s", strings.TrimSpace(response.Error))
		}
	}
	lifecycleState := &workerLifecycle{
		spec: spec, generation: lifecycleGeneration.Add(1), lastUsed: time.Now().UTC(), workingDir: spec.WorkspaceDir,
	}
	lifecycleRegistry.mu.Lock()
	lifecycleRegistry.workers[worker] = lifecycleState
	lifecycleRegistry.mu.Unlock()
	go releaseLifecycleWorker(worker, worker.done)
	m.notifyIdleChange()
	state := &providerKernelState{
		worker: worker, providerID: normalized.ProviderID, configHash: configHash,
		auth: authConnection, proxy: proxy, done: make(chan struct{}),
	}
	m.providerWorkers[spec.KernelID] = state
	go m.watchProviderKernel(spec.KernelID, state)
	return worker, configHash, nil
}

func (m *Manager) ProviderSessionActive(kernelID, providerID, configHash string) bool {
	if m == nil {
		return false
	}
	m.providerMu.Lock()
	defer m.providerMu.Unlock()
	state := m.providerWorkers[strings.TrimSpace(kernelID)]
	return state != nil && state.providerID == strings.TrimSpace(providerID) &&
		(configHash == "" || state.configHash == strings.TrimSpace(configHash)) && state.worker.TerminalError() == nil
}

func (m *Manager) CloseProviderSession(ctx context.Context, kernelID string) error {
	if m == nil {
		return nil
	}
	m.providerMu.Lock()
	state := m.providerWorkers[strings.TrimSpace(kernelID)]
	delete(m.providerWorkers, strings.TrimSpace(kernelID))
	m.providerMu.Unlock()
	if state == nil {
		return nil
	}
	return state.close(ctx)
}

func (m *Manager) watchProviderKernel(kernelID string, state *providerKernelState) {
	<-state.worker.done
	m.providerMu.Lock()
	if m.providerWorkers[kernelID] == state {
		delete(m.providerWorkers, kernelID)
	}
	m.providerMu.Unlock()
	state.closeResources()
}

func (s *providerKernelState) close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	err := s.worker.Close(ctx)
	s.closeResources()
	return err
}

func (s *providerKernelState) closeResources() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.auth != nil {
			_ = s.auth.Close()
		}
		if s.proxy != nil {
			_ = s.proxy.Close()
		}
		close(s.done)
	})
}

func (m *Manager) normalizeProviderRuntimeSpec(input ProviderRuntimeSpec) (ProviderRuntimeSpec, string, error) {
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.Environment = strings.TrimSpace(input.Environment)
	input.InstallID = strings.TrimSpace(input.InstallID)
	input.OrganizationID = strings.TrimSpace(input.OrganizationID)
	input.ModalEnvironment = strings.TrimSpace(input.ModalEnvironment)
	input.AppName = strings.TrimSpace(input.AppName)
	input.PriorAppNames = append([]string(nil), input.PriorAppNames...)
	for index := range input.PriorAppNames {
		input.PriorAppNames[index] = strings.TrimSpace(input.PriorAppNames[index])
		if input.PriorAppNames[index] == "" || len(input.PriorAppNames[index]) > 128 || strings.ContainsAny(input.PriorAppNames[index], "\x00\r\n") {
			return ProviderRuntimeSpec{}, "", errors.New("provider prior app name is invalid")
		}
	}
	if len(input.PriorAppNames) > 16 || len(input.AppName) > 128 || strings.ContainsAny(input.AppName, "\x00\r\n") {
		return ProviderRuntimeSpec{}, "", errors.New("provider app configuration is invalid")
	}
	input.Prelude = strings.TrimSpace(input.Prelude)
	if !ValidEnvironmentName(input.ProviderID) || !ValidEnvironmentName(input.Environment) || input.InstallID == "" || len(input.InstallID) > 256 {
		return ProviderRuntimeSpec{}, "", errors.New("provider runtime identity is invalid")
	}
	var err error
	for label, value := range map[string]*string{
		"bootstrap": &input.BootstrapPath, "entrypoint": &input.EntrypointPath, "provider": &input.ProviderPath,
	} {
		*value, err = canonicalProviderRuntimeFile(*value)
		if err != nil {
			return ProviderRuntimeSpec{}, "", fmt.Errorf("provider runtime %s: %w", label, err)
		}
	}
	input.EnvironmentsPath, err = canonicalProviderRuntimeDirectory(input.EnvironmentsPath)
	if err != nil {
		return ProviderRuntimeSpec{}, "", fmt.Errorf("provider runtime environments: %w", err)
	}
	input.EgressRules, err = normalizeProviderEgressRules(input.EgressRules)
	if err != nil {
		return ProviderRuntimeSpec{}, "", err
	}
	if len(input.Credentials) == 0 || len(input.Credentials) > 32 {
		return ProviderRuntimeSpec{}, "", errors.New("provider runtime credentials are unavailable")
	}
	if len([]byte(input.Prelude)) > maxManagedKernelCodeBytes {
		return ProviderRuntimeSpec{}, "", errors.New("provider kernel preload exceeds the bounded code contract")
	}
	credentials := make(map[string]string, len(input.Credentials))
	for key, value := range input.Credentials {
		if key == "" || len(key) > 128 || value == "" || len(value) > 16<<10 || strings.ContainsAny(key+value, "\x00\r\n") {
			return ProviderRuntimeSpec{}, "", errors.New("provider runtime credential shape is invalid")
		}
		credentials[key] = value
	}
	input.Credentials = credentials
	for key, value := range input.ExtraEnvironment {
		if key == "" || strings.ContainsAny(key, "=\x00\r\n") || strings.ContainsRune(value, '\x00') || providerSecretEnvironmentKey(key) {
			return ProviderRuntimeSpec{}, "", errors.New("provider runtime environment is invalid")
		}
	}
	digestInput, _ := json.Marshal(map[string]any{
		"provider": input.ProviderID, "environment": input.Environment,
		"bootstrap": input.BootstrapPath, "entrypoint": input.EntrypointPath, "provider_path": input.ProviderPath,
		"envs": input.EnvironmentsPath, "install": input.InstallID, "organization": input.OrganizationID,
		"modal_environment": input.ModalEnvironment, "egress": input.EgressRules, "extra_environment": input.ExtraEnvironment,
		"app_name": input.AppName, "prior_app_names": input.PriorAppNames,
		"prelude_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(input.Prelude))),
	})
	digest := sha256.Sum256(digestInput)
	return input, hex.EncodeToString(digest[:]), nil
}

func canonicalProviderRuntimeFile(value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(value) {
		return "", errors.New("path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil || resolved != value {
		return "", errors.New("path must be canonical")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("file is unavailable")
	}
	return resolved, nil
}

func canonicalProviderRuntimeDirectory(value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(value) {
		return "", errors.New("path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil || resolved != value {
		return "", errors.New("path must be canonical")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("directory is unavailable")
	}
	return resolved, nil
}

func providerSecretEnvironmentKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "AUTH", "COOKIE", "BEARER", "API_KEY"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func mergeProviderKernelEnvironment(base []string, groups ...map[string]string) []string {
	values := map[string]string{}
	order := []string{}
	set := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		if _, found := values[key]; !found {
			order = append(order, key)
		}
		values[key] = value
	}
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			set(key, value)
		}
	}
	for _, group := range groups {
		for key, value := range group {
			set(key, value)
		}
	}
	result := make([]string, 0, len(order))
	for _, key := range order {
		result = append(result, key+"="+values[key])
	}
	return result
}

func completeProviderKernelHandshake(connection net.Conn, credentials map[string]string) error {
	if connection == nil {
		return errors.New("provider credential channel is unavailable")
	}
	if err := connection.SetDeadline(time.Now().Add(providerKernelHandshakeTimeout)); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(connection, 4096)
	line, err := reader.ReadBytes('\n')
	if err != nil || len(line) > 4096 {
		return errors.New("provider kernel readiness handshake failed")
	}
	var ready struct {
		Ready    bool `json:"ready"`
		Confined bool `json:"confined"`
	}
	if json.Unmarshal(line, &ready) != nil || !ready.Ready || !ready.Confined {
		return errors.New("provider kernel did not prove confinement")
	}
	auth := make(map[string]string, len(credentials)+1)
	auth["op"] = "auth"
	for key, value := range credentials {
		auth[key] = value
	}
	payload, err := json.Marshal(auth)
	if err != nil || len(payload) > providerKernelMaxAuthBytes {
		return errors.New("provider kernel credential payload is invalid")
	}
	payload = append(payload, '\n')
	if _, err := connection.Write(payload); err != nil {
		clear(payload)
		return errors.New("provider kernel credential delivery failed")
	}
	clear(payload)
	return connection.SetDeadline(time.Time{})
}
