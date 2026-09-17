package compute

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	maxSSHConfigFiles = 64
	maxSSHConfigBytes = 2 << 20
)

type SSHConfigAliases struct {
	Aliases       []string `json:"aliases"`
	ConfigFound   bool     `json:"configFound"`
	ConfigPath    string   `json:"configPath"`
	WildcardCount int      `json:"wildcardCount"`
	IsWSL         bool     `json:"isWsl"`
	Wildcards     []string `json:"-"`
}

func DiscoverSSHConfigAliases(configPath string) (SSHConfigAliases, error) {
	configPath = filepath.Clean(strings.TrimSpace(configPath))
	result := SSHConfigAliases{Aliases: []string{}, ConfigPath: configPath, IsWSL: isWSL()}
	if configPath == "." || !filepath.IsAbs(configPath) {
		return result, errors.New("SSH config path must be absolute")
	}
	root := filepath.Dir(configPath)
	aliases := map[string]struct{}{}
	wildcards := map[string]struct{}{}
	seen := map[string]struct{}{}
	var total int64
	var visit func(string, int) error
	visit = func(path string, depth int) error {
		if depth > 8 || len(seen) >= maxSSHConfigFiles {
			return errors.New("SSH config include limit exceeded")
		}
		path = filepath.Clean(path)
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("SSH config include escapes the .ssh directory")
		}
		if _, ok := seen[path]; ok {
			return nil
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && depth == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SSH config files must be regular non-symlink files")
		}
		total += info.Size()
		if total > maxSSHConfigBytes {
			return errors.New("SSH config byte limit exceeded")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		seen[path] = struct{}{}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch strings.ToLower(fields[0]) {
			case "host":
				for _, name := range fields[1:] {
					if strings.ContainsAny(name, "*?!") {
						wildcards[name] = struct{}{}
					} else if name != "" {
						aliases[name] = struct{}{}
					}
				}
			case "include":
				for _, pattern := range fields[1:] {
					pattern = strings.TrimPrefix(pattern, "~/")
					matches, err := filepath.Glob(filepath.Join(root, pattern))
					if err != nil {
						return fmt.Errorf("invalid SSH Include pattern: %w", err)
					}
					for _, match := range matches {
						if err := visit(match, depth+1); err != nil {
							return err
						}
					}
				}
			}
		}
		return scanner.Err()
	}
	if err := visit(configPath, 0); err != nil {
		return result, err
	}
	result.ConfigFound = len(seen) > 0
	for alias := range aliases {
		result.Aliases = append(result.Aliases, alias)
	}
	sort.Strings(result.Aliases)
	for wildcard := range wildcards {
		result.Wildcards = append(result.Wildcards, wildcard)
	}
	sort.Strings(result.Wildcards)
	result.WildcardCount = len(wildcards)
	return result, nil
}

func isWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	raw, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(raw)), "microsoft")
}
