// Package executionprep describes statically witnessed execution effects. It
// never executes user source or downloads/installs anything. The existing tool
// authorities remain the sole owners of those operations.
package executionprep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	MaxSourceBytes  = 262144
	MaxFacts        = 256
	maxDepth        = 8
	PackageMutation = "package_mutation"
	FileAcquisition = "file_acquisition"
)

type Fact struct {
	Kind string   `json:"kind"`
	Name string   `json:"name"`
	Args []string `json:"args"`
	Line int      `json:"line"`
}

type Requirement struct {
	Effect    string `json:"effect"`
	Authority string `json:"authority"`
	Language  string `json:"language"`
	Mechanism string `json:"mechanism"`
	Line      int    `json:"line,omitempty"`
}

type Witness struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Result struct {
	Requirements []Requirement `json:"requirements,omitempty"`
	ReadFiles    []Witness     `json:"read_files,omitempty"`
	Unresolved   []string      `json:"unresolved,omitempty"`
}

type Request struct{ Language, Source, Environment, WorkspaceRoot, WorkingDir string }
type NativeParser func(context.Context, string, string) ([]Fact, error)

// Analyze traverses nested language and process entries with one bounded plan.
// A missing parser or dynamic value is unknown, not evidence of a forbidden
// effect. In particular, HTTP API calls are not blanket-classified as downloads.
func Analyze(ctx context.Context, request Request, native NativeParser) (Result, error) {
	if ctx == nil || len(request.Source) > MaxSourceBytes {
		return Result{}, errors.New("invalid execution preparation input")
	}
	analysis := analyzer{native: native, request: request, seen: make(map[string]bool)}
	if request.WorkingDir == "" {
		analysis.request.WorkingDir = request.WorkspaceRoot
	}
	err := analysis.source(ctx, request.Language, request.Source, 0)
	return analysis.result, err
}

type analyzer struct {
	native  NativeParser
	request Request
	result  Result
	seen    map[string]bool
	count   int
}

func (a *analyzer) source(ctx context.Context, language, source string, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > maxDepth || len(source) > MaxSourceBytes || a.count >= MaxFacts {
		a.unknown("analysis_budget")
		return nil
	}
	digest := sha256.Sum256([]byte(language + "\x00" + source))
	key := hex.EncodeToString(digest[:])
	if a.seen[key] {
		return nil
	}
	a.seen[key] = true
	var facts []Fact
	var err error
	if language == "bash" {
		facts, err = ParseShell(source)
	} else if a.native != nil {
		facts, err = a.native(ctx, language, source)
	} else {
		err = errors.New("native parser unavailable")
	}
	if err != nil {
		a.unknown(language + "_unresolved")
		return nil
	}
	if len(facts) >= MaxFacts {
		a.unknown("analysis_budget")
	}
	for _, fact := range facts {
		a.count++
		if a.count > MaxFacts {
			a.unknown("analysis_budget")
			break
		}
		switch fact.Kind {
		case "source":
			if len(fact.Args) == 1 {
				if err := a.source(ctx, fact.Name, fact.Args[0], depth+1); err != nil {
					return err
				}
			}
		case "script":
			if len(fact.Args) == 1 {
				if err := a.script(ctx, fact.Name, fact.Args[0], depth+1); err != nil {
					return err
				}
			}
		case "process":
			for _, child := range ProcessFacts(fact.Args, fact.Line) {
				if child.Kind == "call" {
					a.classify(language, child)
				} else if child.Kind == "source" && len(child.Args) == 1 {
					if err := a.source(ctx, child.Name, child.Args[0], depth+1); err != nil {
						return err
					}
				} else if child.Kind == "script" && len(child.Args) == 1 {
					if err := a.script(ctx, child.Name, child.Args[0], depth+1); err != nil {
						return err
					}
				}
			}
		case "call":
			a.classify(language, fact)
		}
	}
	return nil
}

func (a *analyzer) classify(language string, fact Fact) {
	effect, authority := "", ""
	if packageMutationCalls[fact.Name] {
		effect, authority = PackageMutation, "managed_environment"
	}
	if fileAcquisitionCalls[fact.Name] && len(fact.Args) > 0 && publicURL(fact.Args[0]) {
		effect, authority = FileAcquisition, "durable_download"
	}
	if effect == "" {
		return
	}
	for _, r := range a.result.Requirements {
		if r.Effect == effect && r.Language == language && r.Mechanism == fact.Name {
			return
		}
	}
	a.result.Requirements = append(a.result.Requirements, Requirement{Effect: effect, Authority: authority, Language: language, Mechanism: fact.Name, Line: fact.Line})
}

func (a *analyzer) unknown(reason string) {
	for _, existing := range a.result.Unresolved {
		if existing == reason {
			return
		}
	}
	a.result.Unresolved = append(a.result.Unresolved, reason)
}

func publicURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ftp") {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return false
		}
	}
	return true
}

func (a *analyzer) script(ctx context.Context, language, path string, depth int) error {
	if a.request.WorkspaceRoot == "" || a.request.WorkingDir == "" || path == "" {
		a.unknown("script_unresolved")
		return nil
	}
	root, err := os.OpenRoot(a.request.WorkspaceRoot)
	if err != nil {
		a.unknown("script_unresolved")
		return nil
	}
	defer root.Close()
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.request.WorkingDir, path)
	}
	relative, err := filepath.Rel(a.request.WorkspaceRoot, path)
	if err != nil || !filepath.IsLocal(relative) {
		a.unknown("script_outside_workspace")
		return nil
	}
	file, err := root.Open(relative)
	if err != nil {
		a.unknown("script_unresolved")
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxSourceBytes {
		a.unknown("script_unresolved")
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxSourceBytes+1))
	if err != nil || len(raw) > MaxSourceBytes {
		a.unknown("script_unresolved")
		return nil
	}
	digest := sha256.Sum256(raw)
	a.result.ReadFiles = append(a.result.ReadFiles, Witness{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(digest[:])})
	return a.source(ctx, language, string(raw), depth)
}

// These are language/runtime capabilities, never scientific package names or
// task-specific examples. All callers use this one effect registry.
var packageMutationCalls = map[string]bool{
	"process.package_mutation": true, "pip._internal.main": true, "pip.main": true,
	"install.packages": true, "utils::install.packages": true, "remove.packages": true, "utils::remove.packages": true,
	"update.packages": true, "utils::update.packages": true, "BiocManager::install": true,
	"remotes::install_github": true, "remotes::install_git": true, "remotes::install_local": true,
}
var fileAcquisitionCalls = map[string]bool{
	"process.file_download": true, "file.write_http": true, "urllib.request.urlretrieve": true,
	"pandas.read_csv": true, "pandas.read_table": true, "pandas.read_excel": true, "pandas.read_parquet": true,
	"download.file": true, "utils::download.file": true, "read.csv": true, "utils::read.csv": true,
	"read.table": true, "utils::read.table": true, "read.delim": true, "utils::read.delim": true,
	"readRDS": true, "base::readRDS": true,
}
