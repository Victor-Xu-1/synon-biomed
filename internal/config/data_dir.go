package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/datadir"
)

func applyDataDirectoryControl(cfg *Config, configPath string) error {
	cfg.DefaultDataDir = defaultHome()
	controlPath, err := dataDirectoryControlPath()
	if err != nil {
		return err
	}
	cfg.DataDirControlPath = controlPath
	controller := datadir.New(controlPath)
	state, err := controller.Load()
	if err != nil {
		return fmt.Errorf("load data directory control: %w", err)
	}
	// Environment and config values provide the initial location. Once the
	// user explicitly chooses a location in settings, the durable pointer is
	// authoritative so a restart can honor that choice even when the process
	// was originally launched with SYNON_HOME or a config override.
	if state.Current != "" || state.Pending != nil {
		resolved, _, err := controller.Resolve(cfg.DefaultDataDir)
		if err != nil {
			return fmt.Errorf("resolve data directory pointer: %w", err)
		}
		cfg.HomeDir = resolved
		cfg.DataDirSource = "pointer"
		return nil
	}
	if strings.TrimSpace(os.Getenv("SYNON_HOME")) != "" {
		cfg.DataDirSource = "env"
		return nil
	}
	if strings.TrimSpace(configPath) != "" {
		raw, err := os.ReadFile(configPath)
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		if homeRaw, ok := fields["home_dir"]; ok {
			var home string
			if json.Unmarshal(homeRaw, &home) == nil && strings.TrimSpace(home) != "" {
				cfg.DataDirSource = "config"
				return nil
			}
		}
	}
	resolved, source, err := controller.Resolve(defaultHome())
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	cfg.HomeDir = resolved
	cfg.DataDirSource = source
	return nil
}

func dataDirectoryControlPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("SYNON_DATA_DIR_CONTROL")); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("SYNON_DATA_DIR_CONTROL must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	if !filepath.IsAbs(configDirectory) {
		absolute, err := filepath.Abs(configDirectory)
		if err != nil {
			return "", err
		}
		configDirectory = absolute
	}
	return filepath.Join(configDirectory, "synon-go", "data-dir.json"), nil
}
