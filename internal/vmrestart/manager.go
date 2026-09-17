package vmrestart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"synon-go/internal/buildinfo"
)

const (
	StateIdle      = "idle"
	StatePending   = "pending"
	StateCompleted = "completed"
	StateFailed    = "failed"
)

var (
	serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]+\.service$`)
	environmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type RunCommand func(context.Context, string, ...string) ([]byte, error)
type LaunchHost func(context.Context, HostRestartRequest) error

type Options struct {
	HomeDir          string
	Executable       string
	WorkingDirectory string
	UnitDirectory    string
	Distro           string
	User             string
	ServiceName      string
	Environment      map[string]string
	RunCommand       RunCommand
	LaunchHost       LaunchHost
	Now              func() time.Time
}

type HostRestartRequest struct {
	Distro               string
	User                 string
	ServiceName          string
	ShutdownDelaySeconds int
	StartAttempts        int
}

type ServiceInfo struct {
	UnitPath        string `json:"unitPath"`
	EnvironmentPath string `json:"environmentPath"`
	ServiceName     string `json:"serviceName"`
}

type Status struct {
	State       string    `json:"state"`
	Distro      string    `json:"distro,omitempty"`
	ServiceName string    `json:"serviceName,omitempty"`
	RequestedAt time.Time `json:"requestedAt,omitzero"`
	CompletedAt time.Time `json:"completedAt,omitzero"`
	Error       string    `json:"error,omitempty"`
}

type Manager struct {
	mu               sync.Mutex
	homeDir          string
	executable       string
	workingDirectory string
	unitDirectory    string
	distro           string
	user             string
	serviceName      string
	environment      map[string]string
	runCommand       RunCommand
	launchHost       LaunchHost
	now              func() time.Time
}

func New(options Options) (*Manager, error) {
	options.HomeDir = filepath.Clean(strings.TrimSpace(options.HomeDir))
	options.Executable = filepath.Clean(strings.TrimSpace(options.Executable))
	options.WorkingDirectory = filepath.Clean(strings.TrimSpace(options.WorkingDirectory))
	options.UnitDirectory = filepath.Clean(strings.TrimSpace(options.UnitDirectory))
	options.Distro = strings.TrimSpace(options.Distro)
	options.User = strings.TrimSpace(options.User)
	options.ServiceName = strings.TrimSpace(options.ServiceName)
	if options.ServiceName == "" {
		options.ServiceName = "synon-go.service"
	}
	if !filepath.IsAbs(options.HomeDir) || !filepath.IsAbs(options.Executable) ||
		!filepath.IsAbs(options.WorkingDirectory) {
		return nil, errors.New("restart home, executable, and working directory must be absolute")
	}
	if options.UnitDirectory == "." || options.UnitDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve systemd user home: %w", err)
		}
		options.UnitDirectory = filepath.Join(home, ".config", "systemd", "user")
	}
	if !filepath.IsAbs(options.UnitDirectory) {
		return nil, errors.New("systemd unit directory must be absolute")
	}
	if options.Distro == "" || options.User == "" {
		return nil, errors.New("WSL distro and user are required")
	}
	if !serviceNamePattern.MatchString(options.ServiceName) {
		return nil, errors.New("systemd service name is invalid")
	}
	info, err := os.Lstat(options.Executable)
	if err != nil {
		return nil, fmt.Errorf("inspect restart executable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("restart executable must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("restart executable must not be group- or world-writable")
	}
	for key, value := range options.Environment {
		if !environmentPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid environment variable name %q", key)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("environment variable %s contains unsupported control characters", key)
		}
	}
	if options.RunCommand == nil {
		options.RunCommand = runCommand
	}
	if options.LaunchHost == nil {
		options.LaunchHost = LaunchPowerShellHost
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Manager{
		homeDir: options.HomeDir, executable: options.Executable,
		workingDirectory: options.WorkingDirectory, unitDirectory: options.UnitDirectory,
		distro: options.Distro, user: options.User, serviceName: options.ServiceName,
		environment: cloneEnvironment(options.Environment), runCommand: options.RunCommand,
		launchHost: options.LaunchHost, now: options.Now,
	}, nil
}

func (m *Manager) Prepare(ctx context.Context) (ServiceInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prepareLocked(ctx)
}

func (m *Manager) prepareLocked(ctx context.Context) (ServiceInfo, error) {
	stateRoot := filepath.Join(m.homeDir, "systemd")
	if err := ensurePrivateDirectory(stateRoot); err != nil {
		return ServiceInfo{}, fmt.Errorf("prepare restart state root: %w", err)
	}
	if err := ensurePrivateDirectory(m.unitDirectory); err != nil {
		return ServiceInfo{}, fmt.Errorf("prepare systemd user directory: %w", err)
	}
	info := ServiceInfo{
		UnitPath:        filepath.Join(m.unitDirectory, m.serviceName),
		EnvironmentPath: filepath.Join(stateRoot, "runtime.env"),
		ServiceName:     m.serviceName,
	}
	if err := writeAtomicFile(info.EnvironmentPath, []byte(formatEnvironment(m.environment)), 0o600); err != nil {
		return ServiceInfo{}, fmt.Errorf("write systemd environment: %w", err)
	}
	if err := writeAtomicFile(info.UnitPath, []byte(m.serviceUnit(info.EnvironmentPath)), 0o600); err != nil {
		return ServiceInfo{}, fmt.Errorf("write systemd unit: %w", err)
	}
	if _, err := m.runCommand(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return ServiceInfo{}, fmt.Errorf("reload systemd user manager: %w", err)
	}
	if _, err := m.runCommand(ctx, "systemctl", "--user", "enable", m.serviceName); err != nil {
		return ServiceInfo{}, fmt.Errorf("enable systemd user service: %w", err)
	}
	return info, nil
}

func (m *Manager) Restart(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.statusLocked()
	if err != nil {
		return Status{}, err
	}
	if current.State == StatePending {
		return Status{}, errors.New("a WSL VM restart is already pending")
	}
	if _, err := m.prepareLocked(ctx); err != nil {
		return Status{}, err
	}
	pending := Status{
		State: StatePending, Distro: m.distro, ServiceName: m.serviceName,
		RequestedAt: m.now().UTC(),
	}
	if err := m.writeStatusLocked(pending); err != nil {
		return Status{}, err
	}
	request := HostRestartRequest{
		Distro: m.distro, User: m.user, ServiceName: m.serviceName,
		ShutdownDelaySeconds: 2, StartAttempts: 20,
	}
	if err := m.launchHost(ctx, request); err != nil {
		failed := pending
		failed.State = StateFailed
		failed.Error = err.Error()
		failed.CompletedAt = m.now().UTC()
		_ = m.writeStatusLocked(failed)
		return Status{}, fmt.Errorf("launch Windows WSL restart supervisor: %w", err)
	}
	return pending, nil
}

func (m *Manager) MarkRuntimeStarted() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	status, err := m.statusLocked()
	if err != nil {
		return err
	}
	if status.State != StatePending {
		return nil
	}
	status.State = StateCompleted
	status.CompletedAt = m.now().UTC()
	status.Error = ""
	return m.writeStatusLocked(status)
}

func (m *Manager) Status() (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

func (m *Manager) IsRestarting() bool {
	status, err := m.Status()
	return err == nil && status.State == StatePending
}

func (m *Manager) statusLocked() (Status, error) {
	raw, err := os.ReadFile(m.statusPath())
	if errors.Is(err, os.ErrNotExist) {
		return Status{State: StateIdle}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("read VM restart status: %w", err)
	}
	var status Status
	if err := json.Unmarshal(raw, &status); err != nil {
		return Status{}, fmt.Errorf("parse VM restart status: %w", err)
	}
	switch status.State {
	case StateIdle, StatePending, StateCompleted, StateFailed:
	default:
		return Status{}, fmt.Errorf("invalid VM restart state %q", status.State)
	}
	return status, nil
}

func (m *Manager) writeStatusLocked(status Status) error {
	if err := ensurePrivateDirectory(filepath.Dir(m.statusPath())); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := writeAtomicFile(m.statusPath(), raw, 0o600); err != nil {
		return fmt.Errorf("write VM restart status: %w", err)
	}
	return nil
}

func (m *Manager) statusPath() string {
	return filepath.Join(m.homeDir, "systemd", "restart-state.json")
}

func (m *Manager) serviceUnit(environmentPath string) string {
	return strings.Join([]string{
		"[Unit]",
		"Description=" + buildinfo.Release().Name + " managed agent runtime",
		"Wants=network-online.target",
		"After=network-online.target",
		"",
		"[Service]",
		"Type=simple",
		"WorkingDirectory=" + systemdPath(m.workingDirectory),
		"EnvironmentFile=" + systemdPath(environmentPath),
		"ExecStart=" + systemdQuote(m.executable),
		"Restart=always",
		"RestartSec=2",
		"TimeoutStopSec=15",
		"KillMode=mixed",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	}, "\n")
}

func formatEnvironment(environment map[string]string) string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteString("=\"")
		value := strings.ReplaceAll(environment[key], "\\", "\\\\")
		value = strings.ReplaceAll(value, "\"", "\\\"")
		builder.WriteString(value)
		builder.WriteString("\"\n")
	}
	return builder.String()
}

func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

func systemdPath(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		safe := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("/._:-", rune(character))
		switch {
		case character == '%':
			builder.WriteString("%%")
		case safe:
			builder.WriteByte(character)
		default:
			builder.WriteString(fmt.Sprintf("\\x%02x", character))
		}
	}
	return builder.String()
}

func cloneEnvironment(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
