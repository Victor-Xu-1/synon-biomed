package compute

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var sshAliasPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
var sshUserPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

type SSHHostOverrides struct {
	User         string `json:"user,omitempty"`
	Port         int    `json:"port,omitempty"`
	IdentityFile string `json:"identityFile,omitempty"`
}

type SSHHostRequest struct {
	Alias          string           `json:"alias"`
	InitialContext string           `json:"initialContext,omitempty"`
	DataRoots      []string         `json:"dataRoots,omitempty"`
	Overrides      SSHHostOverrides `json:"overrides,omitempty"`
}

type PreparedSSHHost struct {
	Alias          string
	InitialContext string
	DataRoots      []string
	Overrides      map[string]any
}

func PrepareSSHHost(configPath, home string, request SSHHostRequest) (PreparedSSHHost, error) {
	request.Alias = strings.TrimSpace(request.Alias)
	if !sshAliasPattern.MatchString(request.Alias) {
		return PreparedSSHHost{}, errors.New("Invalid arguments: alias: alias must be 1-64 chars, start with letter/digit/underscore")
	}
	if len(request.InitialContext) > 65536 {
		return PreparedSSHHost{}, errors.New("initialContext exceeds 65536 bytes")
	}
	discovery, err := DiscoverSSHConfigAliases(configPath)
	if err != nil {
		return PreparedSSHHost{}, err
	}
	if !discovery.ConfigFound {
		return PreparedSSHHost{}, fmt.Errorf("no SSH config found at %s", discovery.ConfigPath)
	}
	if !sshAliasAllowed(discovery, request.Alias) {
		return PreparedSSHHost{}, fmt.Errorf("alias %q is not defined in %s", request.Alias, discovery.ConfigPath)
	}
	overrides := map[string]any{}
	if request.Overrides.User != "" {
		if !sshUserPattern.MatchString(request.Overrides.User) {
			return PreparedSSHHost{}, errors.New("overrides.user is invalid")
		}
		overrides["user"] = request.Overrides.User
	}
	if request.Overrides.Port != 0 {
		if request.Overrides.Port < 1 || request.Overrides.Port > 65535 {
			return PreparedSSHHost{}, errors.New("overrides.port must be from 1 to 65535")
		}
		overrides["port"] = request.Overrides.Port
	}
	if request.Overrides.IdentityFile != "" {
		resolved, err := resolveSSHIdentityFile(request.Overrides.IdentityFile, home)
		if err != nil {
			return PreparedSSHHost{}, err
		}
		overrides["identityFile"] = resolved
	}
	roots := make([]string, 0, len(request.DataRoots))
	for _, root := range request.DataRoots {
		root = strings.TrimSpace(root)
		if root != "" && path.IsAbs(root) {
			roots = append(roots, path.Clean(root))
		}
	}
	return PreparedSSHHost{
		Alias: request.Alias, InitialContext: strings.TrimSpace(request.InitialContext),
		DataRoots: roots, Overrides: overrides,
	}, nil
}

func sshAliasAllowed(discovery SSHConfigAliases, alias string) bool {
	for _, pattern := range discovery.Wildcards {
		if !strings.HasPrefix(pattern, "!") {
			continue
		}
		if matched, _ := path.Match(strings.TrimPrefix(pattern, "!"), alias); matched {
			return false
		}
	}
	for _, candidate := range discovery.Aliases {
		if candidate == alias {
			return true
		}
	}
	for _, pattern := range discovery.Wildcards {
		if matched, _ := path.Match(pattern, alias); matched {
			return true
		}
	}
	return false
}

func resolveSSHIdentityFile(raw, home string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "~/") {
		raw = filepath.Join(home, strings.TrimPrefix(raw, "~/"))
	}
	if !filepath.IsAbs(raw) {
		return "", errors.New("overrides.identityFile must be absolute or start with ~/")
	}
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", errors.New("overrides.identityFile does not exist")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("overrides.identityFile must be a regular file")
	}
	if !pathWithin(resolved, home) && !pathWithin(resolved, "/etc/ssh") {
		return "", errors.New("overrides.identityFile must live under $HOME or /etc/ssh")
	}
	return resolved, nil
}

func pathWithin(candidate, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
