package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	providerOperationStagePrefix = "synon-biomed-provider-stage-"
	providerOperationMaxRequest  = 1 << 20
	providerOperationMaxReply    = 1 << 20
	providerOperationMaxOutput   = 64 << 10
)

type ProviderOperationInput struct {
	Runtime   ProviderRuntimeSpec
	Operation string
	Request   map[string]any
	Prepare   func(stage string) error
	Collect   func(stage string, response map[string]any) error
}

type ProviderOperationRunner interface {
	RunProviderOperation(context.Context, ProviderOperationInput) (map[string]any, error)
}

type ProviderOperationError struct {
	Kind    string
	Message string
}

func (e *ProviderOperationError) Error() string {
	if e == nil {
		return "provider operation failed"
	}
	return strings.TrimSpace(e.Kind + ": " + e.Message)
}

func (m *Manager) RunProviderOperation(ctx context.Context, input ProviderOperationInput) (map[string]any, error) {
	if m == nil {
		return nil, errors.New("provider operation runtime is unavailable")
	}
	if err := m.Verify(); err != nil {
		return nil, err
	}
	runtimeSpec, _, err := m.normalizeProviderRuntimeSpec(input.Runtime)
	if err != nil {
		return nil, err
	}
	operation := strings.TrimSpace(input.Operation)
	switch operation {
	case "create", "find_owned_submission", "submit", "wait", "probe_many", "reconcile", "terminate", "tail", "list_dir", "list_volumes", "read_file":
	default:
		return nil, errors.New("provider operation is not admitted")
	}
	stage, err := os.MkdirTemp("/tmp", providerOperationStagePrefix)
	if err != nil {
		return nil, errors.New("provider operation stage is unavailable")
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return nil, errors.New("provider operation stage permissions are unavailable")
	}
	if input.Prepare != nil {
		if err := input.Prepare(stage); err != nil {
			return nil, err
		}
	}
	request := copyProviderOperationMap(input.Request)
	request["stage"] = stage
	if runtimeSpec.AppName != "" {
		request["app_name"] = runtimeSpec.AppName
	}
	if len(runtimeSpec.PriorAppNames) > 0 {
		request["prior_app_names"] = append([]string(nil), runtimeSpec.PriorAppNames...)
	}
	if runtimeSpec.ModalEnvironment != "" {
		request["environment"] = runtimeSpec.ModalEnvironment
	}
	rawRequest, err := json.Marshal(request)
	if err != nil || len(rawRequest) == 0 || len(rawRequest) > providerOperationMaxRequest {
		return nil, errors.New("provider operation request exceeds the bounded contract")
	}
	if err := os.WriteFile(filepath.Join(stage, "req.json"), rawRequest, 0o600); err != nil {
		return nil, errors.New("provider operation request could not be staged")
	}

	proxyParentFile, proxyChildFile, err := newProviderRuntimeSocketPair("provider-operation-proxy")
	if err != nil {
		return nil, err
	}
	proxyConnection, err := net.FileConn(proxyParentFile)
	_ = proxyParentFile.Close()
	if err != nil {
		_ = proxyChildFile.Close()
		return nil, errors.New("provider operation proxy is unavailable")
	}
	proxy, err := startProviderProxyMux(proxyConnection, runtimeSpec.EgressRules, runtimeSpec.Dial)
	if err != nil {
		_ = proxyChildFile.Close()
		return nil, err
	}
	defer proxy.Close()

	prefix, python, err := m.managedEnvironmentRuntime(runtimeSpec.Environment, "python")
	if err != nil {
		_ = proxyChildFile.Close()
		return nil, fmt.Errorf("resolve provider environment %q: %w", runtimeSpec.Environment, err)
	}
	mounts := []WorkerMount{
		TrustedReadOnlyDirectoryMount(filepath.Dir(filepath.Dir(runtimeSpec.EntrypointPath))),
		TrustedReadOnlyDirectoryMount(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(runtimeSpec.EntrypointPath))), "kernels")),
		TrustedReadOnlyDirectoryMount(filepath.Dir(runtimeSpec.ProviderPath)),
	}
	proxyFD := 4 + len(mounts)
	hostNetworkNamespace, err := providerHostNetworkNamespaceInode()
	if err != nil {
		_ = proxyChildFile.Close()
		return nil, err
	}
	environment := m.runtimeEnvironmentAtPrefix(runtimeSpec.Environment, "python", stage, "provider-operation", "", "", "", prefix)
	environment = mergeProviderKernelEnvironment(environment, map[string]string{
		"SYNON_PROVIDER_PROXY_FD": strconv.Itoa(proxyFD),
		"OPERON_HOST_NETNS_INO":   hostNetworkNamespace,
		"OPERON_BYOC_ENVS_DIR":    runtimeSpec.EnvironmentsPath,
		"OPERON_BYOC_INSTALL_ID":  runtimeSpec.InstallID,
		"OPERON_BYOC_ENVIRONMENT": runtimeSpec.ModalEnvironment,
		"MODAL_ENVIRONMENT":       runtimeSpec.ModalEnvironment,
		"OPERON_BYOC_APP_NAME":    runtimeSpec.AppName,
	}, runtimeSpec.ExtraEnvironment)
	command, err := newConfinedWorkerCommandWithAuxiliary(
		stage, python,
		[]string{"-I", runtimeSpec.BootstrapPath, "oneshot", runtimeSpec.EntrypointPath, runtimeSpec.ProviderPath, operation, stage, "1"},
		environment, mounts, nil, []*os.File{proxyChildFile},
	)
	if err != nil {
		_ = proxyChildFile.Close()
		return nil, err
	}
	auth, err := providerOperationAuthPayload(runtimeSpec.Credentials)
	if err != nil {
		closeKernelCommandExtraFiles(command)
		return nil, err
	}
	defer clear(auth)
	command.Stdin = bytes.NewReader(auth)
	stdout, stderr := newTailBuffer(providerOperationMaxOutput), newTailBuffer(providerOperationMaxOutput)
	command.Stdout, command.Stderr = stdout, stderr
	process, err := startWorkerProcess(command)
	closeKernelCommandExtraFiles(command)
	if err != nil {
		return nil, errors.New("provider operation process could not start")
	}
	defer process.close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = process.kill()
		<-done
		return nil, context.Cause(ctx)
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if len(message) > 1024 {
			message = message[len(message)-1024:]
		}
		return nil, fmt.Errorf("provider operation process failed (%v): %s", err, message)
	}
	rawReply, found, err := readBoundedKernelMetadataFile(stage, "reply.json", providerOperationMaxReply)
	if err != nil || !found {
		return nil, errors.New("provider operation returned no valid reply")
	}
	decoder := json.NewDecoder(bytes.NewReader(rawReply))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	reply := map[string]any{}
	if err := decoder.Decode(&reply); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("provider operation reply is invalid")
	}
	if ok, _ := reply["ok"].(bool); !ok {
		kind := strings.TrimSpace(providerOperationString(reply["kind"]))
		message := strings.TrimSpace(providerOperationString(reply["msg"]))
		if len(message) > 2048 {
			message = message[:2048]
		}
		return nil, &ProviderOperationError{Kind: kind, Message: message}
	}
	if input.Collect != nil {
		if err := input.Collect(stage, reply); err != nil {
			return nil, err
		}
	}
	return reply, nil
}

func providerOperationAuthPayload(credentials map[string]string) ([]byte, error) {
	request := make(map[string]string, len(credentials)+1)
	request["op"] = "auth"
	for key, value := range credentials {
		request[key] = value
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > providerKernelMaxAuthBytes {
		return nil, errors.New("provider operation credentials are invalid")
	}
	return append(raw, '\n'), nil
}

func copyProviderOperationMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input)+4)
	for key, value := range input {
		result[key] = value
	}
	return result
}

func providerOperationString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
