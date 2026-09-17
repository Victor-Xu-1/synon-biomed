package main

import (
	"os"
	"path/filepath"
	"strings"
)

func discoverSynonBiomedRuntimeAssetsDir(roots []string) string {
	if configured := strings.TrimSpace(os.Getenv("SYNON_BIOMED_RUNTIME_ASSETS_DIR")); configured != "" {
		if resolved := resolveRuntimeAssetsDirectory(configured, roots); resolved != "" {
			return resolved
		}
	}
	for _, root := range roots {
		for _, relative := range []string{
			filepath.Join("assets", "optional"),
			filepath.Join("synonbiomed", "runtime", "assets"),
			filepath.Join("runtime", "assets"),
		} {
			if resolved := existingAbsoluteDirectory(filepath.Join(root, relative)); resolved != "" {
				return resolved
			}
		}
	}
	return ""
}

func resolveRuntimeAssetsDirectory(value string, roots []string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return existingAbsoluteDirectory(value)
	}
	for _, root := range roots {
		if resolved := existingAbsoluteDirectory(filepath.Join(root, value)); resolved != "" {
			return resolved
		}
	}
	return ""
}

func existingAbsoluteDirectory(value string) string {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return ""
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(absolute)
}
