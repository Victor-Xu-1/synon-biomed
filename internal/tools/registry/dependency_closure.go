package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"synon-go/internal/skills"
	"synon-go/internal/toolcontract"
)

type DependencyClass string

const (
	DependencyClassTool            DependencyClass = "tool"
	DependencyClassHostCapability  DependencyClass = "host_capability"
	DependencyClassExternalRuntime DependencyClass = "external_runtime"
)

type DependencyEvidence struct {
	Source string `json:"source"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type RuntimeStatus struct {
	Executable        bool   `json:"executable"`
	AuthorityResolved bool   `json:"authorityResolved,omitempty"`
	Availability      string `json:"availability,omitempty"`
	Route             string `json:"route,omitempty"`
	Evidence          string `json:"evidence,omitempty"`
}

type RuntimeResolver interface {
	ResolveDependencyRuntime(name string, class DependencyClass) RuntimeStatus
}

type RuntimeResolverFunc func(name string, class DependencyClass) RuntimeStatus

func (fn RuntimeResolverFunc) ResolveDependencyRuntime(name string, class DependencyClass) RuntimeStatus {
	if fn == nil {
		return RuntimeStatus{}
	}
	return fn(name, class)
}

type DependencyStatus struct {
	Name                string               `json:"name"`
	Class               DependencyClass      `json:"class"`
	RegistryPresent     bool                 `json:"registryPresence"`
	RuntimeExecutable   bool                 `json:"runtimeExecutable"`
	AuthorityResolved   bool                 `json:"authorityResolved,omitempty"`
	RuntimeAvailability string               `json:"runtimeAvailability"`
	RuntimeRoute        string               `json:"runtimeRoute,omitempty"`
	RuntimeEvidence     string               `json:"runtimeEvidence,omitempty"`
	ClosureRequired     bool                 `json:"closureRequired"`
	Evidence            []DependencyEvidence `json:"evidence"`
}

type SkillDependencyStatus struct {
	Skill        string             `json:"skill"`
	Path         string             `json:"path"`
	Closed       bool               `json:"closed"`
	Dependencies []DependencyStatus `json:"dependencies"`
}

func (s SkillDependencyStatus) Dependency(name string) DependencyStatus {
	for _, dependency := range s.Dependencies {
		if dependency.Name == name {
			return dependency
		}
	}
	return DependencyStatus{}
}

func (s SkillDependencyStatus) ExternalRuntimeNames() []string {
	names := []string{}
	for _, dependency := range s.Dependencies {
		if dependency.Class == DependencyClassExternalRuntime {
			names = append(names, dependency.Name)
		}
	}
	return names
}

type SkillDependencyReport struct {
	TotalSkills     int                     `json:"totalSkills"`
	Closed          bool                    `json:"closed"`
	MissingRequired int                     `json:"missingRequired"`
	Skills          []SkillDependencyStatus `json:"skills"`
	Errors          []string                `json:"errors,omitempty"`
}

func (r SkillDependencyReport) Skill(name string) SkillDependencyStatus {
	for _, skill := range r.Skills {
		if skill.Skill == name {
			return skill
		}
	}
	return SkillDependencyStatus{}
}

type discoveredDependency struct {
	name     string
	class    DependencyClass
	required bool
	evidence []DependencyEvidence
}

var (
	standardToolsPattern   = regexp.MustCompile(`(?i)^\s*Standard tools:\s*(.+?)\s*$`)
	standardLibraryPattern = regexp.MustCompile(`(?i)^\s*Standard library only:\s*(.+?)\s*$`)
	pipInstallPattern      = regexp.MustCompile(`(?i)^\s*pip(?:3)?\s+install\s+(.+?)\s*$`)
	commandVPattern        = regexp.MustCompile(`\bcommand\s+-v\s+([A-Za-z0-9_.-]+)`)
	pythonWhichPattern     = regexp.MustCompile(`\bshutil\.which\(["']([A-Za-z0-9_.-]+)["']\)`)
	pythonCommandPattern   = regexp.MustCompile(`(?m)(?:cmd\s*=|runner\(|subprocess\.(?:run|Popen)\()\s*\[\s*["']([A-Za-z0-9_.-]+)["']`)
	bodyToolReferenceVerb  = regexp.MustCompile(`(?i)\b(?:call|use|run|invoke|execute|via)(?:\s+(?:the|a|an))?\s*$`)
)

func AuditSkillDependencyClosure(loaded []skills.Skill, reg *Registry, resolver RuntimeResolver) SkillDependencyReport {
	return auditSkillDependencyClosure(loaded, reg, resolver, true)
}

// AuditRuntimeSkillDependencyClosure validates whether a Skill can be loaded
// into the current runner without treating every allowed-tools entry as a
// mandatory dependency. allowed-tools is an execution allowlist: a Skill may
// describe several alternative routes while using only one of them for the
// current task. Tools invoked by the Skill body, declared as Standard tools,
// or required by shipped scripts remain closure requirements.
func AuditRuntimeSkillDependencyClosure(loaded []skills.Skill, reg *Registry, resolver RuntimeResolver) SkillDependencyReport {
	return auditSkillDependencyClosure(loaded, reg, resolver, false)
}

func auditSkillDependencyClosure(
	loaded []skills.Skill,
	reg *Registry,
	resolver RuntimeResolver,
	requireAllowedTools bool,
) SkillDependencyReport {
	report := SkillDependencyReport{TotalSkills: len(loaded), Closed: true, Skills: make([]SkillDependencyStatus, 0, len(loaded))}
	for _, skill := range loaded {
		discovered, auditErrors := discoverSkillDependencies(skill, reg, requireAllowedTools)
		report.Errors = append(report.Errors, auditErrors...)
		row := SkillDependencyStatus{Skill: skill.Name, Path: skill.Path, Closed: len(auditErrors) == 0, Dependencies: make([]DependencyStatus, 0, len(discovered))}
		if len(auditErrors) > 0 {
			report.Closed = false
		}
		for _, dependency := range discovered {
			_, present := reg.Get(dependency.name)
			runtime := RuntimeStatus{}
			if resolver != nil {
				runtime = resolver.ResolveDependencyRuntime(dependency.name, dependency.class)
			}
			if dependency.required && dependency.class == DependencyClassTool && !present && !runtime.AuthorityResolved {
				message := fmt.Sprintf("%s: unresolved tool dependency %q", skill.Path, dependency.name)
				report.Errors = append(report.Errors, message)
				row.Closed = false
				report.Closed = false
			}
			availability := runtime.Availability
			if availability == "" {
				if runtime.Executable {
					availability = "available"
				} else if dependency.class == DependencyClassExternalRuntime {
					availability = "evidence_only"
				} else {
					availability = "unavailable"
				}
			}
			required := dependency.required
			closed := runtime.Executable && (dependency.class != DependencyClassTool || present || runtime.AuthorityResolved)
			if required && !closed {
				row.Closed = false
				report.Closed = false
				report.MissingRequired++
			}
			row.Dependencies = append(row.Dependencies, DependencyStatus{
				Name: dependency.name, Class: dependency.class, RegistryPresent: present,
				RuntimeExecutable: runtime.Executable, AuthorityResolved: runtime.AuthorityResolved,
				RuntimeAvailability: availability, RuntimeRoute: runtime.Route,
				RuntimeEvidence: runtime.Evidence, ClosureRequired: required, Evidence: dependency.evidence,
			})
		}
		report.Skills = append(report.Skills, row)
	}
	sort.Slice(report.Skills, func(i, j int) bool { return report.Skills[i].Skill < report.Skills[j].Skill })
	return report
}

func discoverSkillDependencies(skill skills.Skill, reg *Registry, requireAllowedTools bool) ([]discoveredDependency, []string) {
	dependencies := map[string]*discoveredDependency{}
	auditErrors := []string{}
	add := func(name string, class DependencyClass, evidence DependencyEvidence, required bool) {
		name = strings.Trim(strings.TrimSpace(name), "` .")
		if name == "" {
			return
		}
		if class == DependencyClassTool {
			canonical, ok := toolcontract.NormalizeRuntimeName(name)
			if !ok {
				auditErrors = append(auditErrors, fmt.Sprintf("%s: unrecognized tool dependency %q", skill.Path, name))
				return
			}
			name = canonical
		}
		key := string(class) + "\x00" + name
		dependency := dependencies[key]
		if dependency == nil {
			dependency = &discoveredDependency{name: name, class: class}
			dependencies[key] = dependency
		}
		dependency.required = dependency.required || required
		dependency.evidence = append(dependency.evidence, evidence)
	}
	for _, name := range skill.Tools {
		add(name, DependencyClassTool, DependencyEvidence{Source: "frontmatter", Path: skill.Path, Detail: "tools/allowed-tools"}, requireAllowedTools)
	}
	for index, line := range strings.Split(skill.Body, "\n") {
		lineNumber := index + 1
		if match := standardToolsPattern.FindStringSubmatch(line); len(match) == 2 {
			for _, name := range splitDependencyNames(match[1]) {
				class := DependencyClassTool
				if name == "host.llm" {
					class = DependencyClassHostCapability
				}
				add(name, class, DependencyEvidence{Source: "body_dependencies", Path: skill.Path, Line: lineNumber, Detail: strings.TrimSpace(line)}, true)
			}
		}
		if match := standardLibraryPattern.FindStringSubmatch(line); len(match) == 2 {
			for _, name := range splitDependencyNames(match[1]) {
				add(name, DependencyClassExternalRuntime, DependencyEvidence{Source: "body_dependencies", Path: skill.Path, Line: lineNumber, Detail: "standard library"}, true)
			}
		}
		if match := pipInstallPattern.FindStringSubmatch(line); len(match) == 2 {
			packages := strings.SplitN(match[1], "#", 2)[0]
			for _, name := range splitDependencyNames(packages) {
				add(name, DependencyClassExternalRuntime, DependencyEvidence{Source: "body_dependencies", Path: skill.Path, Line: lineNumber, Detail: "python package"}, true)
			}
		}
		for _, name := range []string{"web_search", "fetch_article_fulltext", "host.llm"} {
			if strings.Contains(line, name) {
				class := DependencyClassTool
				if name == "host.llm" {
					class = DependencyClassHostCapability
				}
				add(name, class, DependencyEvidence{Source: "body_invocation", Path: skill.Path, Line: lineNumber, Detail: strings.TrimSpace(line)}, true)
			}
		}
		for _, name := range []string{toolcontract.AskUser, "AskUserQuestion", "ask_user_question"} {
			if strings.Contains(line, name) {
				add(name, DependencyClassTool, DependencyEvidence{Source: "body_invocation", Path: skill.Path, Line: lineNumber, Detail: strings.TrimSpace(line)}, true)
				break
			}
		}
		for _, name := range reg.AllNames() {
			if name == "" {
				continue
			}
			if lineInvokesRuntimeTool(line, name) {
				add(name, DependencyClassTool, DependencyEvidence{Source: "body_invocation", Path: skill.Path, Line: lineNumber, Detail: strings.TrimSpace(line)}, true)
			}
		}
	}
	auditErrors = append(auditErrors, discoverScriptDependencies(skill, add)...)
	out := make([]discoveredDependency, 0, len(dependencies))
	for _, dependency := range dependencies {
		out = append(out, *dependency)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].class != out[j].class {
			return out[i].class < out[j].class
		}
		return out[i].name < out[j].name
	})
	return out, auditErrors
}

func lineInvokesRuntimeTool(line, name string) bool {
	quoted := "`" + name + "`"
	for offset := 0; offset < len(line); {
		index := strings.Index(line[offset:], quoted)
		if index < 0 {
			break
		}
		index += offset
		if bodyToolReferenceVerb.MatchString(line[:index]) {
			return true
		}
		offset = index + len(quoted)
	}
	for offset := 0; offset < len(line); {
		index := strings.Index(line[offset:], name)
		if index < 0 {
			return false
		}
		index += offset
		beforeBoundary := index == 0 || !runtimeToolIdentifierByte(line[index-1])
		after := index + len(name)
		for after < len(line) && (line[after] == ' ' || line[after] == '\t') {
			after++
		}
		if beforeBoundary && after < len(line) && line[after] == '(' {
			return true
		}
		offset = index + len(name)
	}
	return false
}

func runtimeToolIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_' || value == '-' || value == '.'
}

func discoverScriptDependencies(skill skills.Skill, add func(string, DependencyClass, DependencyEvidence, bool)) []string {
	scriptsRoot := filepath.Join(filepath.Dir(skill.Path), "scripts")
	if _, err := os.Stat(scriptsRoot); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return []string{fmt.Sprintf("stat %s: %v", scriptsRoot, err)}
	}
	errorsFound := []string{}
	_ = filepath.WalkDir(scriptsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			errorsFound = append(errorsFound, fmt.Sprintf("walk %s: %v", path, walkErr))
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			errorsFound = append(errorsFound, fmt.Sprintf("read %s: %v", path, err))
			return nil
		}
		content := string(data)
		for _, command := range scriptCommands(path, content) {
			add(command, DependencyClassExternalRuntime, DependencyEvidence{Source: "script_command", Path: path, Line: firstLineContaining(content, command)}, true)
		}
		return nil
	})
	return errorsFound
}

func scriptCommands(path string, content string) []string {
	commands := map[string]bool{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".py":
		commands["python3"] = true
		for _, pattern := range []*regexp.Regexp{pythonWhichPattern, pythonCommandPattern} {
			for _, match := range pattern.FindAllStringSubmatch(content, -1) {
				commands[match[1]] = true
			}
		}
	case ".sh":
		commands["bash"] = true
		for _, match := range commandVPattern.FindAllStringSubmatch(content, -1) {
			commands[match[1]] = true
		}
		for _, command := range []string{"sed", "ssh", "git", "jq", "python3"} {
			if regexp.MustCompile(`\b`+regexp.QuoteMeta(command)+`\b`).FindStringIndex(content) != nil {
				commands[command] = true
			}
		}
	}
	result := make([]string, 0, len(commands))
	for command := range commands {
		result = append(result, command)
	}
	sort.Strings(result)
	return result
}

func splitDependencyNames(value string) []string {
	fields := strings.FieldsFunc(strings.TrimSpace(strings.TrimSuffix(value, ".")), func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t'
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		name := strings.Trim(strings.TrimSpace(field), "`'")
		if name != "" && !strings.HasPrefix(name, "-") {
			result = append(result, name)
		}
	}
	return result
}

func firstLineContaining(content string, value string) int {
	for index, line := range strings.Split(content, "\n") {
		if strings.Contains(line, value) {
			return index + 1
		}
	}
	return 0
}
