package lspstatic

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"synon-go/internal/tools/fileevents"
)

const maxDiagnosticsPerFile = 10
const maxTotalDiagnostics = 30
const maxDeliveredDiagnosticFiles = 500

type pendingLSPDiagnostic struct {
	ServerName string
	Files      []DiagnosticFile
	Timestamp  time.Time
}

type DiagnosticFile struct {
	URI         string            `json:"uri"`
	Diagnostics []json.RawMessage `json:"diagnostics"`
}

type PendingDiagnosticSet struct {
	ServerName string           `json:"serverName"`
	Files      []DiagnosticFile `json:"files"`
}

var diagnosticRegistry = struct {
	sync.Mutex
	pending        map[string]pendingLSPDiagnostic
	delivered      map[string]map[string]struct{}
	deliveredOrder []string
}{
	pending:   map[string]pendingLSPDiagnostic{},
	delivered: map[string]map[string]struct{}{},
}

func init() {
	fileevents.RegisterChangeHandler(func(path string) {
		ClearDeliveredDiagnosticsForFile(fileURI(path))
	})
}

func RegisterPendingLSPDiagnostic(serverName string, files []DiagnosticFile) {
	filtered := make([]DiagnosticFile, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.URI) == "" || len(file.Diagnostics) == 0 {
			continue
		}
		filtered = append(filtered, file)
	}
	if len(filtered) == 0 {
		return
	}
	id := randomDiagnosticID()
	diagnosticRegistry.Lock()
	defer diagnosticRegistry.Unlock()
	diagnosticRegistry.pending[id] = pendingLSPDiagnostic{
		ServerName: strings.TrimSpace(serverName),
		Files:      filtered,
		Timestamp:  time.Now(),
	}
}

func CheckForLSPDiagnostics() []PendingDiagnosticSet {
	diagnosticRegistry.Lock()
	defer diagnosticRegistry.Unlock()
	if len(diagnosticRegistry.pending) == 0 {
		return nil
	}
	allFiles := []DiagnosticFile{}
	serverNames := map[string]struct{}{}
	for _, pending := range diagnosticRegistry.pending {
		allFiles = append(allFiles, pending.Files...)
		if strings.TrimSpace(pending.ServerName) != "" {
			serverNames[pending.ServerName] = struct{}{}
		}
	}
	clear(diagnosticRegistry.pending)
	files := deduplicateDiagnosticFilesLocked(allFiles)
	files = limitDiagnosticFiles(files)
	if len(files) == 0 {
		return nil
	}
	for _, file := range files {
		if _, ok := diagnosticRegistry.delivered[file.URI]; !ok {
			diagnosticRegistry.delivered[file.URI] = map[string]struct{}{}
			diagnosticRegistry.deliveredOrder = append(diagnosticRegistry.deliveredOrder, file.URI)
			trimDeliveredDiagnosticFilesLocked()
		}
		for _, diagnostic := range file.Diagnostics {
			diagnosticRegistry.delivered[file.URI][diagnosticKey(diagnostic)] = struct{}{}
		}
	}
	return []PendingDiagnosticSet{{
		ServerName: strings.Join(sortedKeys(serverNames), ", "),
		Files:      files,
	}}
}

func CheckForLSPDiagnosticsForURI(uri string) []PendingDiagnosticSet {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return nil
	}
	diagnosticRegistry.Lock()
	defer diagnosticRegistry.Unlock()
	if len(diagnosticRegistry.pending) == 0 {
		return nil
	}
	matchedFiles := []DiagnosticFile{}
	serverNames := map[string]struct{}{}
	for id, pending := range diagnosticRegistry.pending {
		remaining := make([]DiagnosticFile, 0, len(pending.Files))
		for _, file := range pending.Files {
			if file.URI == uri {
				matchedFiles = append(matchedFiles, file)
				if strings.TrimSpace(pending.ServerName) != "" {
					serverNames[pending.ServerName] = struct{}{}
				}
				continue
			}
			remaining = append(remaining, file)
		}
		if len(remaining) == 0 {
			delete(diagnosticRegistry.pending, id)
		} else {
			pending.Files = remaining
			diagnosticRegistry.pending[id] = pending
		}
	}
	files := deduplicateDiagnosticFilesLocked(matchedFiles)
	files = limitDiagnosticFiles(files)
	if len(files) == 0 {
		return nil
	}
	for _, file := range files {
		if _, ok := diagnosticRegistry.delivered[file.URI]; !ok {
			diagnosticRegistry.delivered[file.URI] = map[string]struct{}{}
			diagnosticRegistry.deliveredOrder = append(diagnosticRegistry.deliveredOrder, file.URI)
			trimDeliveredDiagnosticFilesLocked()
		}
		for _, diagnostic := range file.Diagnostics {
			diagnosticRegistry.delivered[file.URI][diagnosticKey(diagnostic)] = struct{}{}
		}
	}
	return []PendingDiagnosticSet{{
		ServerName: strings.Join(sortedKeys(serverNames), ", "),
		Files:      files,
	}}
}

func ClearAllLSPDiagnostics() {
	diagnosticRegistry.Lock()
	defer diagnosticRegistry.Unlock()
	clear(diagnosticRegistry.pending)
}

func ResetAllLSPDiagnosticState() {
	diagnosticRegistry.Lock()
	defer diagnosticRegistry.Unlock()
	clear(diagnosticRegistry.pending)
	clear(diagnosticRegistry.delivered)
	diagnosticRegistry.deliveredOrder = nil
}

func ClearDeliveredDiagnosticsForFile(uri string) {
	diagnosticRegistry.Lock()
	defer diagnosticRegistry.Unlock()
	delete(diagnosticRegistry.delivered, uri)
	for index, candidate := range diagnosticRegistry.deliveredOrder {
		if candidate == uri {
			diagnosticRegistry.deliveredOrder = append(diagnosticRegistry.deliveredOrder[:index], diagnosticRegistry.deliveredOrder[index+1:]...)
			return
		}
	}
}

func PendingLSPDiagnosticCount() int {
	diagnosticRegistry.Lock()
	defer diagnosticRegistry.Unlock()
	return len(diagnosticRegistry.pending)
}

func deduplicateDiagnosticFilesLocked(files []DiagnosticFile) []DiagnosticFile {
	byURI := map[string]map[string]struct{}{}
	outputByURI := map[string]*DiagnosticFile{}
	order := []string{}
	for _, file := range files {
		uri := strings.TrimSpace(file.URI)
		if uri == "" {
			continue
		}
		if _, ok := byURI[uri]; !ok {
			byURI[uri] = map[string]struct{}{}
			outputByURI[uri] = &DiagnosticFile{URI: uri}
			order = append(order, uri)
		}
		delivered := diagnosticRegistry.delivered[uri]
		for _, diagnostic := range file.Diagnostics {
			key := diagnosticKey(diagnostic)
			if _, ok := byURI[uri][key]; ok {
				continue
			}
			if delivered != nil {
				if _, ok := delivered[key]; ok {
					continue
				}
			}
			byURI[uri][key] = struct{}{}
			outputByURI[uri].Diagnostics = append(outputByURI[uri].Diagnostics, diagnostic)
		}
	}
	output := make([]DiagnosticFile, 0, len(order))
	for _, uri := range order {
		file := outputByURI[uri]
		if len(file.Diagnostics) == 0 {
			continue
		}
		output = append(output, *file)
	}
	return output
}

func limitDiagnosticFiles(files []DiagnosticFile) []DiagnosticFile {
	total := 0
	limited := make([]DiagnosticFile, 0, len(files))
	for _, file := range files {
		sort.SliceStable(file.Diagnostics, func(i, j int) bool {
			return diagnosticSeverity(file.Diagnostics[i]) < diagnosticSeverity(file.Diagnostics[j])
		})
		if len(file.Diagnostics) > maxDiagnosticsPerFile {
			file.Diagnostics = file.Diagnostics[:maxDiagnosticsPerFile]
		}
		remaining := maxTotalDiagnostics - total
		if remaining <= 0 {
			break
		}
		if len(file.Diagnostics) > remaining {
			file.Diagnostics = file.Diagnostics[:remaining]
		}
		if len(file.Diagnostics) == 0 {
			continue
		}
		total += len(file.Diagnostics)
		limited = append(limited, file)
	}
	return limited
}

func diagnosticSeverity(raw json.RawMessage) int {
	var decoded struct {
		Severity any `json:"severity"`
	}
	if json.Unmarshal(raw, &decoded) != nil {
		return 4
	}
	switch severity := decoded.Severity.(type) {
	case float64:
		if severity >= 1 && severity <= 4 {
			return int(severity)
		}
	case string:
		switch strings.ToLower(severity) {
		case "error":
			return 1
		case "warning", "warn":
			return 2
		case "info", "information":
			return 3
		case "hint":
			return 4
		}
	}
	return 4
}

func diagnosticKey(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var decoded struct {
		Message  string          `json:"message"`
		Severity any             `json:"severity"`
		Range    json.RawMessage `json:"range"`
		Source   string          `json:"source"`
		Code     any             `json:"code"`
	}
	if json.Unmarshal(raw, &decoded) != nil || decoded.Message == "" {
		return string(raw)
	}
	key, err := json.Marshal(map[string]any{
		"message":  decoded.Message,
		"severity": decoded.Severity,
		"range":    json.RawMessage(decoded.Range),
		"source":   decoded.Source,
		"code":     decoded.Code,
	})
	if err != nil {
		return string(raw)
	}
	return string(key)
}

func trimDeliveredDiagnosticFilesLocked() {
	for len(diagnosticRegistry.deliveredOrder) > maxDeliveredDiagnosticFiles {
		oldest := diagnosticRegistry.deliveredOrder[0]
		diagnosticRegistry.deliveredOrder = diagnosticRegistry.deliveredOrder[1:]
		delete(diagnosticRegistry.delivered, oldest)
	}
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func randomDiagnosticID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return time.Now().Format(time.RFC3339Nano)
	}
	return hex.EncodeToString(raw[:])
}
