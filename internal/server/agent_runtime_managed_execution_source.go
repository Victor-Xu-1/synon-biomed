package server

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/executionprep"
)

func managedExecutionSourceIdentifier(content string, identifiers []string, kernel *agentKernelContext) (string, string, bool) {
	if kernel == nil || strings.TrimSpace(kernel.workspaceDir) == "" {
		return "", "", false
	}
	root, err := filepath.Abs(kernel.workspaceDir)
	if err != nil {
		return "", "", false
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", false
	}
	filesystem, err := os.OpenRoot(root)
	if err != nil {
		return "", "", false
	}
	defer filesystem.Close()
	allowedExtensions := map[string]bool{
		".py": true, ".r": true, ".sh": true, ".bash": true, ".zsh": true,
		".ps1": true, ".pl": true, ".rb": true, ".js": true, ".mjs": true, ".cjs": true,
	}
	candidates := make([]string, 0)
	for _, token := range managedExecutionCommandTokens(content) {
		token = managedExecutionPathToken(token)
		if allowedExtensions[strings.ToLower(filepath.Ext(token))] {
			candidates = append(candidates, token)
		}
	}
	if directory, args, ok := executionprep.ShellCommandInDirectory(content); ok {
		working := root
		if directory != "" {
			working = directory
			if !filepath.IsAbs(working) {
				working = filepath.Join(root, working)
			}
		}
		paths := make([]string, 0, 2)
		if strings.ContainsAny(args[0], `/\`) {
			paths = append(paths, args[0])
		}
		for _, fact := range executionprep.ProcessFacts(args, 0) {
			if fact.Kind == "script" && len(fact.Args) == 1 {
				paths = append(paths, fact.Args[0])
			}
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				path = filepath.Join(working, path)
			}
			candidates = append(candidates, path)
		}
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || seen[resolved] {
			continue
		}
		seen[resolved] = true
		relative, err := filepath.Rel(root, resolved)
		if err != nil || !filepath.IsLocal(relative) {
			continue
		}
		raw, ok := readManagedExecutionSource(filesystem, relative)
		if !ok {
			continue
		}
		language := managedExecutionSourceLanguage(resolved)
		if !allowedExtensions[strings.ToLower(filepath.Ext(resolved))] {
			if !strings.HasPrefix(string(raw), "#!") {
				continue
			}
			line, _, _ := strings.Cut(string(raw), "\n")
			words := strings.Fields(strings.TrimPrefix(line, "#!"))
			if len(words) == 0 {
				continue
			}
			interpreter := filepath.Base(words[0])
			if interpreter == "env" && len(words) == 2 {
				interpreter = words[1]
			}
			switch interpreter {
			case "bash", "sh":
				language = "bash"
			case "python", "python3":
				language = "python"
			case "Rscript":
				language = "r"
			default:
				continue
			}
		}
		if identifier, matched := managedExecutionIdentifier(language, string(raw), identifiers); matched {
			return identifier, filepath.ToSlash(relative), true
		}
	}
	return "", "", false
}

func readManagedExecutionSource(root *os.Root, relative string) ([]byte, bool) {
	file, err := root.Open(relative)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 2<<20 {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	return raw, err == nil && len(raw) <= 2<<20 && !strings.ContainsRune(string(raw), 0)
}
