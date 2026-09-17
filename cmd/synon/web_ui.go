package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/webui"
)

func discoverWebUI(configured string, roots []string) (http.Handler, string, error) {
	configured = strings.TrimSpace(configured)
	switch strings.ToLower(configured) {
	case "disabled", "off":
		// Explicit API-only mode: never serve a workbench web UI, even when a
		// build artifact exists under one of the discovery roots.
		return nil, "", nil
	}
	if configured != "" {
		resolved := resolveRuntimeAssetPath(configured, roots...)
		handler, err := webui.New(resolved)
		if err != nil {
			return nil, "", fmt.Errorf("configure workbench web UI: %w", err)
		}
		return handler, handlerRoot(resolved), nil
	}

	seen := map[string]struct{}{}
	for _, root := range roots {
		for _, relative := range []string{"web", filepath.Join("frontend", "out", "renderer")} {
			candidate := filepath.Join(root, relative)
			absolute, err := filepath.Abs(candidate)
			if err != nil {
				continue
			}
			key := filepath.Clean(absolute)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if _, err := os.Stat(filepath.Join(key, "index.html")); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return nil, "", fmt.Errorf("inspect discovered workbench web UI: %w", err)
			}
			handler, err := webui.New(key)
			if err != nil {
				return nil, "", fmt.Errorf("configure discovered workbench web UI: %w", err)
			}
			return handler, key, nil
		}
	}
	return nil, "", nil
}

func handlerRoot(root string) string {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}
