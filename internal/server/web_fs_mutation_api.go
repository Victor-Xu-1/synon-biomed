package server

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"synon-go/internal/tools/fileevents"
	"synon-go/internal/tools/fileops"
)

func (s *Server) handleWebFSTemp(w http.ResponseWriter, r *http.Request, userID string) {
	var input struct {
		FileName string `json:"file_name"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	if !validWebFSBaseName(input.FileName) {
		writeWebFSError(w, fmt.Errorf("%w: file_name must be a plain file name", errWebFSInvalid))
		return
	}
	root, err := s.webFSTempRoot(userID)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	extension := filepath.Ext(input.FileName)
	baseName := strings.TrimSuffix(input.FileName, extension)
	baseName = strings.ReplaceAll(baseName, "*", "_")
	file, err := os.CreateTemp(root, baseName+"-*"+extension)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, filepath.Clean(path))
}

func (s *Server) handleWebFSWrite(w http.ResponseWriter, r *http.Request, userID string) {
	var input struct {
		Path      string `json:"path"`
		Data      string `json:"data"`
		Workspace string `json:"workspace,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	if len(input.Data) > maxWebFSReadBytes {
		writeWebFSError(w, errWebFSTooLarge)
		return
	}
	access, err := s.resolveWebFSPath(userID, input.Path, input.Workspace, true, false, false)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	if _, err := fileops.Write(access.Root, access.Target, input.Data, "utf8", true); err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, true)
}

func (s *Server) handleWebFSMetadata(w http.ResponseWriter, r *http.Request, userID string, allowLocalRead bool) {
	var input struct {
		Path      string `json:"path"`
		Workspace string `json:"workspace,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	access, err := s.resolveWebFSPath(userID, input.Path, input.Workspace, false, true, allowLocalRead)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	info, err := os.Lstat(access.Target)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		writeWebFSError(w, errWebFSUnsupported)
		return
	}
	entryType := "directory"
	if !info.IsDir() {
		entryType = strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(info.Name()))))
		if entryType == "" {
			entryType = "application/octet-stream"
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"name": info.Name(), "path": access.Target, "size": info.Size(), "type": entryType,
		"lastModified": info.ModTime().UnixMilli(), "isDirectory": info.IsDir(),
	})
}

func (s *Server) handleWebFSCopy(w http.ResponseWriter, r *http.Request, userID string, allowLocalRead bool) {
	var input struct {
		FilePaths  []string `json:"file_paths"`
		Workspace  string   `json:"workspace"`
		SourceRoot string   `json:"source_root,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	if len(input.FilePaths) == 0 || len(input.FilePaths) > maxWebFSCopySources {
		writeWebFSError(w, fmt.Errorf("%w: file_paths must contain 1 to %d paths", errWebFSInvalid, maxWebFSCopySources))
		return
	}
	destination, err := s.resolveWebFSPath(userID, input.Workspace, input.Workspace, true, true, false)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	destinationInfo, err := os.Stat(destination.Target)
	if err != nil || !destinationInfo.IsDir() {
		writeWebFSError(w, fmt.Errorf("%w: workspace must be an existing directory", errWebFSInvalid))
		return
	}
	var sourceRoot string
	if strings.TrimSpace(input.SourceRoot) != "" {
		rootAccess, err := s.resolveWebFSPath(userID, input.SourceRoot, "", false, true, allowLocalRead)
		if err != nil {
			writeWebFSError(w, err)
			return
		}
		rootInfo, err := os.Stat(rootAccess.Target)
		if err != nil || !rootInfo.IsDir() {
			writeWebFSError(w, fmt.Errorf("%w: source_root must be an existing directory", errWebFSInvalid))
			return
		}
		sourceRoot = rootAccess.Target
	}

	copied := make([]string, 0, len(input.FilePaths))
	failed := make([]map[string]string, 0)
	state := &webFSCopyState{}
	for _, requested := range input.FilePaths {
		source, err := s.resolveWebFSPath(userID, requested, "", false, true, allowLocalRead)
		if err != nil {
			failed = append(failed, map[string]string{"path": requested, "error": err.Error()})
			continue
		}
		relative := filepath.Base(source.Target)
		if sourceRoot != "" {
			relative, err = filepath.Rel(sourceRoot, source.Target)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				failed = append(failed, map[string]string{"path": requested, "error": "source is outside source_root"})
				continue
			}
			if relative == "." {
				relative = filepath.Base(source.Target)
			}
		}
		targetPath := filepath.Join(destination.Target, relative)
		target, err := resolveWebFSTarget(destination.Root, targetPath, false)
		if err == nil {
			err = copyWebFSAtomic(source.Target, target, state)
		}
		if err != nil {
			failed = append(failed, map[string]string{"path": requested, "error": err.Error()})
			continue
		}
		copied = append(copied, target)
		fileevents.NotifyChanged(target)
	}
	response := map[string]any{"copied_files": copied}
	if len(failed) > 0 {
		response["failed_files"] = failed
	}
	writeWorkspaceJSON(w, http.StatusOK, response)
}

func (s *Server) handleWebFSRemove(w http.ResponseWriter, r *http.Request, userID string) {
	var input struct {
		Path      string `json:"path"`
		Workspace string `json:"workspace,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	access, err := s.resolveWebFSPath(userID, input.Path, input.Workspace, true, true, false)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	if _, err := fileops.Delete(access.Root, access.Target, true); err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, nil)
}

func (s *Server) handleWebFSRename(w http.ResponseWriter, r *http.Request, userID string) {
	var input struct {
		Path      string `json:"path"`
		NewName   string `json:"new_name"`
		Workspace string `json:"workspace,omitempty"`
	}
	if err := decodeWebFSJSON(r, &input); err != nil {
		writeWebFSError(w, err)
		return
	}
	if !validWebFSBaseName(input.NewName) {
		writeWebFSError(w, fmt.Errorf("%w: new_name must be a plain file name", errWebFSInvalid))
		return
	}
	access, err := s.resolveWebFSPath(userID, input.Path, input.Workspace, true, true, false)
	if err != nil {
		writeWebFSError(w, err)
		return
	}
	target := filepath.Join(filepath.Dir(access.Target), input.NewName)
	if _, err := resolveWebFSTarget(access.Root, target, false); err != nil {
		writeWebFSError(w, err)
		return
	}
	if _, err := fileops.Move(access.Root, access.Target, target, false); err != nil {
		writeWebFSError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"new_path": target})
}
