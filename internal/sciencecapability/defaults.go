package sciencecapability

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed scientific-capabilities.v2.json executionpacks/*.py
var bundledExecutionPacks embed.FS

var (
	defaultCatalogOnce sync.Once
	defaultCatalog     Catalog
	defaultCatalogErr  error
)

func DefaultCatalog() (Catalog, error) {
	defaultCatalogOnce.Do(func() {
		raw, err := bundledExecutionPacks.ReadFile("scientific-capabilities.v2.json")
		if err != nil {
			defaultCatalogErr = err
			return
		}
		defaultCatalog, defaultCatalogErr = Decode(bytes.NewReader(raw))
	})
	return defaultCatalog, defaultCatalogErr
}

func DefaultCatalogSHA256() (string, error) {
	raw, err := bundledExecutionPacks.ReadFile("scientific-capabilities.v2.json")
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func ExecutionPackScript(pack ExecutionPack) ([]byte, string, error) {
	if pack.Mode != "local" || !safeExecutionPackPath(pack.Script) {
		return nil, "", errors.New("local execution pack script is unavailable")
	}
	path := filepath.ToSlash(strings.TrimSpace(pack.Script))
	raw, err := bundledExecutionPacks.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if len(pack.Modules) == 0 {
		digest := sha256.Sum256(raw)
		return append([]byte(nil), raw...), hex.EncodeToString(digest[:]), nil
	}
	var bundle bytes.Buffer
	bundle.WriteString("import sys as _synon_sys, types as _synon_types\n")
	for _, modulePath := range pack.Modules {
		modulePath = filepath.ToSlash(strings.TrimSpace(modulePath))
		moduleName := strings.TrimSuffix(filepath.Base(modulePath), filepath.Ext(modulePath))
		if !safeExecutionPackPath(modulePath) || !identifierPattern.MatchString(strings.ReplaceAll(moduleName, "_", "-")) {
			return nil, "", errors.New("local execution pack module is unavailable")
		}
		moduleRaw, readErr := bundledExecutionPacks.ReadFile(modulePath)
		if readErr != nil {
			return nil, "", readErr
		}
		encodedName, _ := json.Marshal(moduleName)
		encodedPath, _ := json.Marshal(modulePath)
		encodedSource, _ := json.Marshal(string(moduleRaw))
		bundle.WriteString("_synon_module = _synon_types.ModuleType(" + string(encodedName) + ")\n")
		bundle.WriteString("_synon_module.__file__ = " + string(encodedPath) + "\n")
		bundle.WriteString("_synon_sys.modules[" + string(encodedName) + "] = _synon_module\n")
		bundle.WriteString("exec(compile(" + string(encodedSource) + ", " + string(encodedPath) + ", 'exec'), _synon_module.__dict__)\n")
	}
	encodedPath, _ := json.Marshal(path)
	encodedSource, _ := json.Marshal(string(raw))
	bundle.WriteString("exec(compile(" + string(encodedSource) + ", " + string(encodedPath) + ", 'exec'), {'__name__': '__main__', '__file__': " + string(encodedPath) + "})\n")
	assembled := bundle.Bytes()
	digest := sha256.Sum256(assembled)
	return append([]byte(nil), assembled...), hex.EncodeToString(digest[:]), nil
}
