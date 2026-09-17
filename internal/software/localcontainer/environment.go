package localcontainer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/software"
	"synon-go/internal/toolprogress"
)

const (
	ProviderID        = "local-container"
	EnvironmentPrefix = "swc-"
	maxCatalogEntries = 256
)

type Spec struct {
	Image       string `json:"image"`
	Accelerator string `json:"accelerator,omitempty"`
	Network     string `json:"network,omitempty"`
}

type ResourceSnapshot struct {
	CPUCount      int      `json:"cpu_count"`
	MemoryBytes   int64    `json:"memory_bytes,omitempty"`
	GPU           []string `json:"gpu,omitempty"`
	DockerVersion string   `json:"docker_version"`
	NVIDIARuntime bool     `json:"nvidia_runtime"`
}

type Preflight struct {
	Ready     bool             `json:"ready"`
	Resources ResourceSnapshot `json:"resources"`
	Spec      Spec             `json:"spec"`
}

type Environment struct {
	Name            string    `json:"name"`
	ProviderID      string    `json:"provider_id"`
	RequestedImage  string    `json:"requested_image"`
	ResolvedImage   string    `json:"resolved_image"`
	ImageID         string    `json:"image_id"`
	RepoDigests     []string  `json:"repo_digests,omitempty"`
	ImageSizeBytes  int64     `json:"image_size_bytes,omitempty"`
	Accelerator     string    `json:"accelerator"`
	Network         string    `json:"network"`
	HostEnvironment string    `json:"host_environment"`
	HostGeneration  string    `json:"host_generation"`
	Status          string    `json:"status"`
	Disposition     string    `json:"disposition"`
	CreatedAt       time.Time `json:"created_at"`
	LastVerifiedAt  time.Time `json:"last_verified_at"`
}

type HostEnvironmentManager interface {
	ManagedPythonEnvironmentName() string
	EnsureManagedPythonEnvironment(context.Context) error
	InspectManagedEnvironment(context.Context, string) (kernelruntime.ManagedEnvironment, bool, error)
	CreateManagedEnvironment(context.Context, kernelruntime.CreateManagedEnvironmentInput) (kernelruntime.ManagedEnvironment, error)
}

type commandRunner interface {
	Run(context.Context, ...string) (string, string, error)
	RunStreaming(context.Context, []string, func(string)) (string, string, error)
}

type Manager struct {
	host         HostEnvironmentManager
	catalogRoot  string
	docker       commandRunner
	pollInterval time.Duration
	mu           sync.Mutex
}

func New(host HostEnvironmentManager, condaHome string) (*Manager, error) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.New("Docker executable is unavailable")
	}
	return newManager(host, filepath.Join(strings.TrimSpace(condaHome), "container-environments"), execDockerRunner{executable: dockerPath})
}

func newManager(host HostEnvironmentManager, catalogRoot string, docker commandRunner) (*Manager, error) {
	if host == nil {
		return nil, errors.New("container runtime requires the managed Python command host")
	}
	catalogRoot = filepath.Clean(strings.TrimSpace(catalogRoot))
	if catalogRoot == "." || !filepath.IsAbs(catalogRoot) {
		return nil, errors.New("container environment catalog path is invalid")
	}
	if docker == nil {
		return nil, errors.New("container runtime requires a Docker command client")
	}
	return &Manager{host: host, catalogRoot: catalogRoot, docker: docker, pollInterval: 5 * time.Second}, nil
}

func NormalizeSpec(input Spec) (Spec, error) {
	result := input
	result.Image = strings.TrimSpace(result.Image)
	result.Accelerator = strings.ToLower(strings.TrimSpace(result.Accelerator))
	result.Network = strings.ToLower(strings.TrimSpace(result.Network))
	if result.Accelerator == "" {
		result.Accelerator = "none"
	}
	if result.Network == "" {
		result.Network = "egress"
	}
	if !validImageReference(result.Image) {
		return Spec{}, errors.New("container image must be a bounded registry reference without credentials")
	}
	if result.Accelerator != "none" && result.Accelerator != "required" {
		return Spec{}, errors.New("container accelerator must be none or required")
	}
	if result.Network != "none" && result.Network != "egress" {
		return Spec{}, errors.New("container network must be none or egress")
	}
	return result, nil
}

func validImageReference(value string) bool {
	if value == "" || len(value) > 1024 || strings.HasPrefix(value, "-") || strings.Contains(value, "://") || strings.ContainsAny(value, "\x00\r\n\t ") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	if strings.Count(value, "@") > 1 {
		return false
	}
	if at := strings.IndexByte(value, '@'); at >= 0 {
		digest := value[at+1:]
		return at > 0 && regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(digest)
	}
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]+$`).MatchString(value)
}

func IsEnvironmentName(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(value, EnvironmentPrefix) || len(value) != len(EnvironmentPrefix)+24 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, EnvironmentPrefix))
	return err == nil
}

func (m *Manager) Preflight(ctx context.Context, input Spec) (Preflight, error) {
	spec, err := NormalizeSpec(input)
	if err != nil {
		return Preflight{}, err
	}
	stdout, stderr, err := m.docker.Run(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return Preflight{}, fmt.Errorf("Docker daemon is unavailable: %s", boundedDiagnostic(stderr, err))
	}
	version := strings.TrimSpace(stdout)
	runtimes, runtimeStderr, err := m.docker.Run(ctx, "info", "--format", "{{json .Runtimes}}")
	if err != nil {
		return Preflight{}, fmt.Errorf("Docker runtime inventory failed: %s", boundedDiagnostic(runtimeStderr, err))
	}
	nvidia := strings.Contains(strings.ToLower(runtimes), `"nvidia"`)
	if spec.Accelerator == "required" && !nvidia {
		return Preflight{}, errors.New("the selected container requires an accelerator, but Docker has no NVIDIA runtime")
	}
	return Preflight{
		Ready: true,
		Spec:  spec,
		Resources: ResourceSnapshot{
			CPUCount: runtime.NumCPU(), MemoryBytes: hostMemoryBytes(), GPU: hostGPUSummary(ctx),
			DockerVersion: version, NVIDIARuntime: nvidia,
		},
	}, nil
}

func (m *Manager) Prepare(ctx context.Context, input Spec, operationID string) (Environment, Preflight, error) {
	if m == nil {
		return Environment{}, Preflight{}, errors.New("container environment manager is unavailable")
	}
	preflight, err := m.Preflight(ctx, input)
	if err != nil {
		return Environment{}, Preflight{}, err
	}
	spec := preflight.Spec
	m.mu.Lock()
	defer m.mu.Unlock()

	toolprogress.Report(ctx, toolprogress.Update{Phase: "checking_container_image", Message: "Checking the selected OCI image", Indeterminate: true})
	image, found, err := m.inspectImage(ctx, spec.Image)
	if err != nil {
		return Environment{}, preflight, err
	}
	pulled := false
	if !found {
		toolprogress.Report(ctx, toolprogress.Update{Phase: "downloading_container_image", Message: "Downloading the selected OCI image", Indeterminate: true})
		if err := m.pullImage(ctx, spec.Image); err != nil {
			return Environment{}, preflight, err
		}
		pulled = true
		image, found, err = m.inspectImage(ctx, spec.Image)
		if err != nil || !found {
			if err == nil {
				err = errors.New("Docker did not publish the selected image after pull")
			}
			return Environment{}, preflight, err
		}
	}
	toolprogress.Report(ctx, toolprogress.Update{Phase: "preparing_container_command_host", Message: "Preparing the reusable container command host", Indeterminate: true})

	environmentName, err := environmentName(image.ID, spec)
	if err != nil {
		return Environment{}, preflight, err
	}
	if err := m.host.EnsureManagedPythonEnvironment(ctx); err != nil {
		return Environment{}, preflight, fmt.Errorf("prepare container command host: %w", err)
	}
	hostEnvironment, hostFound, inspectErr := m.host.InspectManagedEnvironment(ctx, environmentName)
	disposition := software.ProvisionDispositionReused
	if inspectErr != nil || !hostFound || hostEnvironment.Status != "ready" || strings.TrimSpace(hostEnvironment.Generation) == "" {
		if hostFound {
			disposition = software.ProvisionDispositionRepaired
		} else {
			disposition = software.ProvisionDispositionInstalled
		}
		hostEnvironment, err = m.host.CreateManagedEnvironment(ctx, kernelruntime.CreateManagedEnvironmentInput{
			Name: environmentName, Language: "python", SourceEnvironment: m.host.ManagedPythonEnvironmentName(),
			OperationID: strings.TrimSpace(operationID),
		})
		if err != nil {
			return Environment{}, preflight, fmt.Errorf("prepare immutable container command host: %w", err)
		}
	}
	if pulled && disposition == software.ProvisionDispositionReused {
		disposition = software.ProvisionDispositionInstalled
	}
	now := time.Now().UTC()
	record := Environment{
		Name: environmentName, ProviderID: ProviderID, RequestedImage: spec.Image,
		ResolvedImage: image.resolvedReference(spec.Image), ImageID: image.ID,
		RepoDigests: append([]string(nil), image.RepoDigests...), ImageSizeBytes: image.Size,
		Accelerator: spec.Accelerator, Network: spec.Network,
		HostEnvironment: hostEnvironment.Name, HostGeneration: hostEnvironment.Generation,
		Status: "ready", Disposition: disposition, CreatedAt: now, LastVerifiedAt: now,
	}
	if existing, found, _ := m.readRecord(environmentName); found && !existing.CreatedAt.IsZero() {
		record.CreatedAt = existing.CreatedAt
	}
	if err := m.writeRecord(record); err != nil {
		return Environment{}, preflight, err
	}
	completed := float64(100)
	toolprogress.Report(ctx, toolprogress.Update{Phase: "container_environment_ready", Message: "Container environment is ready", PhasePercent: &completed})
	return record, preflight, nil
}

func (m *Manager) Resolve(ctx context.Context, name string) (Environment, bool, error) {
	if m == nil || !IsEnvironmentName(name) {
		return Environment{}, false, nil
	}
	record, found, err := m.readRecord(strings.ToLower(strings.TrimSpace(name)))
	if err != nil || !found {
		return Environment{}, found, err
	}
	image, imageFound, err := m.inspectImage(ctx, record.ImageID)
	if err != nil {
		return Environment{}, true, err
	}
	hostEnvironment, hostFound, hostErr := m.host.InspectManagedEnvironment(ctx, record.HostEnvironment)
	if hostErr != nil {
		return Environment{}, true, hostErr
	}
	if !imageFound || image.ID != record.ImageID || !hostFound || hostEnvironment.Status != "ready" || hostEnvironment.Generation != record.HostGeneration {
		record.Status = "unavailable"
		return record, true, nil
	}
	record.Status = "ready"
	record.LastVerifiedAt = time.Now().UTC()
	return record, true, nil
}

func (m *Manager) List(ctx context.Context) ([]Environment, error) {
	if m == nil {
		return nil, errors.New("container environment manager is unavailable")
	}
	entries, err := os.ReadDir(m.catalogRoot)
	if errors.Is(err, os.ErrNotExist) {
		return []Environment{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Environment, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || len(result) >= maxCatalogEntries {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		record, found, resolveErr := m.Resolve(ctx, name)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if found {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

type dockerImage struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	Size        int64    `json:"Size"`
}

func (i dockerImage) resolvedReference(requested string) string {
	if strings.Contains(requested, "@sha256:") {
		return requested
	}
	for _, digest := range i.RepoDigests {
		if strings.TrimSpace(digest) != "" {
			return digest
		}
	}
	return i.ID
}

func (m *Manager) inspectImage(ctx context.Context, reference string) (dockerImage, bool, error) {
	stdout, stderr, err := m.docker.Run(ctx, "image", "inspect", reference)
	if err != nil {
		lower := strings.ToLower(stderr + "\n" + err.Error())
		if strings.Contains(lower, "no such image") || strings.Contains(lower, "not found") {
			return dockerImage{}, false, nil
		}
		return dockerImage{}, false, fmt.Errorf("inspect Docker image: %s", boundedDiagnostic(stderr, err))
	}
	var images []dockerImage
	if err := json.Unmarshal([]byte(stdout), &images); err != nil || len(images) != 1 {
		return dockerImage{}, false, errors.New("Docker image inspection returned an invalid receipt")
	}
	image := images[0]
	image.ID = strings.ToLower(strings.TrimSpace(image.ID))
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(image.ID) {
		return dockerImage{}, false, errors.New("Docker image inspection did not return a content identity")
	}
	image.RepoDigests = uniqueSorted(image.RepoDigests)
	return image, true, nil
}

func (m *Manager) pullImage(ctx context.Context, reference string) error {
	started := time.Now()
	var progressMu sync.Mutex
	var latestBytes int64
	var latestTotal int64
	var latestMessage string
	lastSampleAt := started
	lastSampleBytes := int64(0)
	report := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		completed, total := dockerProgressBytes(line)
		progressMu.Lock()
		if completed > 0 {
			latestBytes = completed
		}
		if total > 0 {
			latestTotal = total
		}
		latestMessage = boundedText(line, 180)
		now := time.Now()
		elapsed := now.Sub(lastSampleAt).Seconds()
		var speed *float64
		if elapsed > 0 && latestBytes >= lastSampleBytes {
			value := float64(latestBytes-lastSampleBytes) / elapsed
			if value > 0 {
				speed = &value
			}
		}
		lastSampleAt, lastSampleBytes = now, latestBytes
		update := toolprogress.Update{Phase: "downloading_container_image", Message: latestMessage, BytesPerSecond: speed, Indeterminate: latestTotal == 0}
		if latestBytes > 0 {
			value := latestBytes
			update.BytesCompleted = &value
		}
		if latestTotal > 0 {
			value := latestTotal
			update.BytesTotal = &value
			percent := float64(latestBytes) * 100 / float64(latestTotal)
			update.PhasePercent = &percent
		}
		progressMu.Unlock()
		toolprogress.Report(ctx, update)
	}
	stdout, stderr, err := m.docker.RunStreaming(ctx, []string{"pull", reference}, report)
	if err != nil {
		return fmt.Errorf("pull Docker image: %s", boundedDiagnostic(stderr+"\n"+stdout, err))
	}
	return nil
}

func environmentName(imageID string, spec Spec) (string, error) {
	if !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(imageID) {
		return "", errors.New("container image identity is invalid")
	}
	payload, err := json.Marshal(struct {
		ImageID, Accelerator, Network string
	}{imageID, spec.Accelerator, spec.Network})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return EnvironmentPrefix + hex.EncodeToString(digest[:12]), nil
}

func (m *Manager) recordPath(name string) string {
	return filepath.Join(m.catalogRoot, name+".json")
}

func (m *Manager) readRecord(name string) (Environment, bool, error) {
	if !IsEnvironmentName(name) {
		return Environment{}, false, nil
	}
	raw, err := os.ReadFile(m.recordPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return Environment{}, false, nil
	}
	if err != nil {
		return Environment{}, false, err
	}
	if len(raw) == 0 || len(raw) > 64*1024 {
		return Environment{}, false, errors.New("container environment record is invalid")
	}
	var record Environment
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || decoder.Decode(&struct{}{}) != io.EOF || record.Name != name || record.ProviderID != ProviderID {
		return Environment{}, false, errors.New("container environment record is invalid")
	}
	return record, true, nil
}

func (m *Manager) writeRecord(record Environment) error {
	if !IsEnvironmentName(record.Name) || record.ProviderID != ProviderID {
		return errors.New("container environment record authority is invalid")
	}
	if err := os.MkdirAll(m.catalogRoot, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(m.catalogRoot, ".container-environment-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, m.recordPath(record.Name))
}

type execDockerRunner struct{ executable string }

func (r execDockerRunner) Run(ctx context.Context, arguments ...string) (string, string, error) {
	command := exec.CommandContext(ctx, r.executable, arguments...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.String(), stderr.String(), err
}

func (r execDockerRunner) RunStreaming(ctx context.Context, arguments []string, onLine func(string)) (string, string, error) {
	command := exec.CommandContext(ctx, r.executable, arguments...)
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		return "", "", err
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		return "", "", err
	}
	if err := command.Start(); err != nil {
		return "", "", err
	}
	type streamLine struct {
		stderr bool
		text   string
		err    error
	}
	lines := make(chan streamLine, 64)
	var readers sync.WaitGroup
	read := func(source io.Reader, stderr bool) {
		defer readers.Done()
		scanner := bufio.NewScanner(source)
		scanner.Split(splitDockerProgressFrames)
		scanner.Buffer(make([]byte, 32*1024), 1024*1024)
		for scanner.Scan() {
			lines <- streamLine{stderr: stderr, text: scanner.Text()}
		}
		if scanErr := scanner.Err(); scanErr != nil {
			lines <- streamLine{stderr: stderr, err: scanErr}
		}
	}
	readers.Add(2)
	go read(stdoutPipe, false)
	go read(stderrPipe, true)
	go func() { readers.Wait(); close(lines) }()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var streamErr error
	for line := range lines {
		if line.err != nil {
			streamErr = errors.Join(streamErr, line.err)
			continue
		}
		if line.stderr {
			stderr.WriteString(line.text + "\n")
		} else {
			stdout.WriteString(line.text + "\n")
		}
		if onLine != nil {
			onLine(line.text)
		}
	}
	waitErr := command.Wait()
	return stdout.String(), stderr.String(), errors.Join(waitErr, streamErr)
}

func hostMemoryBytes() int64 {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			value, parseErr := strconv.ParseInt(fields[1], 10, 64)
			if parseErr == nil && value > 0 {
				return value * 1024
			}
		}
	}
	return 0
}

func hostGPUSummary(ctx context.Context) []string {
	executable, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, executable, "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	output, err := command.Output()
	if err != nil {
		return nil
	}
	result := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = boundedText(line, 160)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func uniqueSorted(values []string) []string {
	seen := map[string]string{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value != "" {
			seen[strings.ToLower(value)] = value
		}
	}
	result := make([]string, 0, len(seen))
	for _, value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func boundedDiagnostic(output string, err error) string {
	message := strings.TrimSpace(output)
	if message == "" && err != nil {
		message = err.Error()
	}
	return boundedText(message, 600)
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	characters := []rune(value)
	if len(characters) > limit {
		value = string(characters[:limit])
	}
	return value
}
