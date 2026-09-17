package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxWebDocumentBytes = 64 << 20

type webDocumentConversionResult struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (s *Server) handleWebDocumentConvert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebFSError(w, fmt.Errorf("%w: POST required", errWebFSMethod))
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var input struct {
		FilePath      string `json:"file_path,omitempty"`
		FilePathCamel string `json:"filePath,omitempty"`
		ArtifactID    string `json:"artifact_id,omitempty"`
		VersionID     string `json:"version_id,omitempty"`
		To            string `json:"to"`
		Workspace     string `json:"workspace,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebOfficeError(w, err)
		return
	}
	targetFormat := strings.TrimSpace(input.To)
	if targetFormat != "markdown" && targetFormat != "excel-json" && targetFormat != "ppt-json" {
		writeWebOfficeError(w, fmt.Errorf("%w: to must be markdown, excel-json, or ppt-json", errWebFSInvalid))
		return
	}
	filePath := firstNonEmpty(input.FilePath, input.FilePathCamel)
	artifactID, versionID := strings.TrimSpace(input.ArtifactID), strings.TrimSpace(input.VersionID)
	if artifactID != "" {
		if strings.TrimSpace(filePath) != "" {
			writeWebOfficeError(w, fmt.Errorf("%w: provide file_path or artifact_id, not both", errWebFSInvalid))
			return
		}
		resolvedPath, err := s.resolveOwnedArtifactOfficePath(r.Context(), userID, artifactID, versionID)
		if err != nil {
			writeWebOfficeError(w, err)
			return
		}
		filePath = resolvedPath
	} else {
		if versionID != "" {
			writeWebOfficeError(w, fmt.Errorf("%w: version_id requires artifact_id", errWebFSInvalid))
			return
		}
		allowLocalRead := (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) && isLoopbackRequest(r)
		access, err := s.resolveWebFSPath(
			userID, filePath, input.Workspace, false, true, allowLocalRead,
		)
		if err != nil {
			writeWebOfficeError(w, err)
			return
		}
		filePath = access.Target
	}
	info, err := os.Stat(filePath)
	if err != nil {
		writeWebOfficeError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeWebOfficeError(w, fmt.Errorf("%w: document source must reference a regular file", errWebFSUnsupported))
		return
	}
	if info.Size() < 0 || info.Size() > maxWebDocumentBytes {
		writeWebOfficeError(w, errWebFSTooLarge)
		return
	}
	result := convertWebDocument(filePath, targetFormat)
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"to": targetFormat, "result": result})
}

func convertWebDocument(path string, targetFormat string) webDocumentConversionResult {
	var data any
	var err error
	extension := strings.ToLower(filepath.Ext(path))
	switch targetFormat {
	case "markdown":
		switch extension {
		case ".md", ".markdown", ".txt":
			var raw []byte
			raw, err = readWebFSFile(path, maxWebFSReadBytes)
			data = string(raw)
		case ".docx":
			data, err = convertWebDOCXToMarkdown(path)
		default:
			err = fmt.Errorf("markdown conversion does not support %s files", extension)
		}
	case "excel-json":
		switch extension {
		case ".xlsx", ".csv":
			data, err = convertWebSpreadsheetToJSON(path)
		default:
			err = fmt.Errorf("spreadsheet conversion does not support %s files", extension)
		}
	case "ppt-json":
		if extension == ".pptx" {
			data, err = convertWebPPTXToJSON(path)
		} else {
			err = fmt.Errorf("presentation conversion does not support %s files", extension)
		}
	}
	if err != nil {
		return webDocumentConversionResult{Success: false, Error: err.Error()}
	}
	return webDocumentConversionResult{Success: true, Data: data}
}
