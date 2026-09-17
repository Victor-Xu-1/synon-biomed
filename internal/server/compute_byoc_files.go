package server

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/compute"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) listBYOCRemoteDirectory(ctx context.Context, userID, providerName, requestedPath string) (compute.RemoteDirectory, error) {
	runtimeSpec, err := s.byocRuntimeForUser(userID, providerName)
	if err != nil {
		return compute.RemoteDirectory{}, err
	}
	root, remotePath, landing, err := parseBYOCVolumePath(requestedPath)
	if err != nil {
		return compute.RemoteDirectory{}, &compute.RemoteError{Kind: "outside_roots", Message: err.Error()}
	}
	entries := []compute.RemoteEntry{}
	if landing {
		result, err := s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
			Runtime: runtimeSpec, Operation: "list_volumes", Request: map[string]any{},
		})
		if err != nil {
			return compute.RemoteDirectory{}, err
		}
		for _, raw := range anySliceValue(result["volumes"]) {
			item := mapValue(raw)
			name := strings.TrimSpace(stringValue(item["name"]))
			if name == "" || strings.ContainsAny(name, "/\\\x00\r\n") {
				continue
			}
			entries = append(entries, compute.RemoteEntry{Name: name, IsDirectory: true, MTime: int64(numberValue(item["created_at"]))})
		}
	} else {
		result, err := s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
			Runtime: runtimeSpec, Operation: "list_dir",
			Request: map[string]any{"root": root, "path": remotePath, "limit": compute.RemoteDirectoryLimit},
		})
		if err != nil {
			return compute.RemoteDirectory{}, err
		}
		for _, raw := range anySliceValue(result["entries"]) {
			item := mapValue(raw)
			name := strings.TrimSpace(stringValue(item["name"]))
			if name == "" || strings.ContainsAny(name, "/\\\x00\r\n") {
				continue
			}
			entries = append(entries, compute.RemoteEntry{
				Name: name, IsDirectory: stringValue(item["type"]) == "dir",
				Size: int64(numberValue(item["size"])), MTime: int64(numberValue(item["mtime"])),
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDirectory != entries[j].IsDirectory {
			return entries[i].IsDirectory
		}
		return entries[i].Name < entries[j].Name
	})
	resolved := "/"
	if !landing {
		resolved = "/" + root + strings.TrimSuffix(remotePath, "/")
	}
	return compute.RemoteDirectory{
		Entries: entries, Truncated: len(entries) >= compute.RemoteDirectoryLimit,
		Roots: map[string]any{"provider": providerName, "volumes": true}, ResolvedPath: resolved,
	}, nil
}

func (s *Server) fetchBYOCRemoteFile(ctx context.Context, userID, providerName, requestedPath string, capBytes int64) (compute.RemoteDownload, error) {
	if capBytes <= 0 {
		return compute.RemoteDownload{}, &compute.RemoteError{Kind: "too_large", Message: "download cap is invalid"}
	}
	runtimeSpec, err := s.byocRuntimeForUser(userID, providerName)
	if err != nil {
		return compute.RemoteDownload{}, err
	}
	root, remotePath, landing, err := parseBYOCVolumePath(requestedPath)
	if err != nil || landing || remotePath == "/" {
		return compute.RemoteDownload{}, &compute.RemoteError{Kind: "not_a_file", Message: "remote path must name a volume file"}
	}
	temporary, err := os.MkdirTemp("", "synon-byoc-download-")
	if err != nil {
		return compute.RemoteDownload{}, errors.New("BYOC download staging is unavailable")
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return compute.RemoteDownload{}, err
	}
	filename := path.Base(remotePath)
	target := filepath.Join(temporary, filename)
	_, err = s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
		Runtime: runtimeSpec, Operation: "read_file",
		Request: map[string]any{"root": root, "path": remotePath, "cap_bytes": capBytes},
		Collect: func(stage string, response map[string]any) error {
			declared := int64(numberValue(response["size"]))
			if declared < 0 || declared > capBytes {
				return errors.New("BYOC provider returned an invalid file size")
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			written, copyErr := kernelruntime.CopyProviderOperationFile(stage, "out.bin", file, capBytes)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil || written != declared {
				return errors.New("BYOC provider file failed bounded copy validation")
			}
			return nil
		},
	})
	if err != nil {
		_ = os.RemoveAll(temporary)
		return compute.RemoteDownload{}, err
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Size() > capBytes {
		_ = os.RemoveAll(temporary)
		return compute.RemoteDownload{}, errors.New("BYOC downloaded file failed validation")
	}
	return compute.RemoteDownload{Path: target, Filename: filename, Size: info.Size()}, nil
}

func (s *Server) byocRuntimeForUser(userID, providerName string) (kernelruntime.ProviderRuntimeSpec, error) {
	if s == nil || s.providerOperationRunner == nil || s.workspaceStore == nil {
		return kernelruntime.ProviderRuntimeSpec{}, errors.New("BYOC provider operation runtime is unavailable")
	}
	providerID := strings.TrimPrefix(strings.TrimSpace(providerName), "byoc:")
	authority, err := s.agentComputeProviderAuthority(workspace.KernelFrameAccess{UserID: strings.TrimSpace(userID)}, providerID, true)
	if err != nil {
		return kernelruntime.ProviderRuntimeSpec{}, err
	}
	return authority.Definition, nil
}

func parseBYOCVolumePath(value string) (root, remotePath string, landing bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return "", "/", true, nil
	}
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\x00\r\n") {
		return "", "", false, errors.New("BYOC path must be absolute")
	}
	clean := path.Clean(value)
	if clean == "/" {
		return "", "/", true, nil
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return "", "", false, errors.New("BYOC volume name is invalid")
	}
	root = parts[0]
	remotePath = "/"
	if len(parts) > 1 {
		remotePath += strings.Join(parts[1:], "/")
	}
	return root, remotePath, false, nil
}
