package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const kernelMaterializationReceiptSchema = "synon.kernel-materialization-receipt.v1"

type kernelMaterializationReceipt struct {
	Schema           string `json:"schema"`
	VersionID        string `json:"version_id"`
	Filename         string `json:"filename"`
	MaterializedPath string `json:"materialized_path"`
	SizeBytes        int64  `json:"size_bytes"`
	ContentSHA256    string `json:"content_sha256"`
	ModTimeUnixNano  int64  `json:"mod_time_unix_nano"`
	Mode             uint32 `json:"mode"`
}

func kernelMaterializedFileMatches(ctx context.Context, path, receiptPath, versionID, filename string, sizeBytes int64, contentSHA256 string) (bool, error) {
	root := filepath.Dir(filepath.Dir(path))
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	file, err := openAgentWorkspaceRegularFile(root, relative)
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if contentSHA256 != "" && kernelMaterializationReceiptMatches(receiptPath, path, versionID, filename, sizeBytes, contentSHA256, info) {
		return true, nil
	}
	matches, err := kernelMaterializedContentMatches(ctx, file, sizeBytes, contentSHA256)
	if err == nil && matches {
		err = writeKernelMaterializationReceipt(receiptPath, path, versionID, filename, sizeBytes, contentSHA256)
	}
	return matches, err
}

func kernelMaterializationReceiptPath(receiptRoot, workspaceDir, versionID string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(workspaceDir) + "\x00" + versionID))
	return filepath.Join(receiptRoot, hex.EncodeToString(digest[:])+".json")
}

func kernelMaterializationReceiptMatches(
	receiptPath, materializedPath, versionID, filename string,
	sizeBytes int64,
	contentSHA256 string,
	info os.FileInfo,
) bool {
	receiptInfo, err := os.Lstat(receiptPath)
	if err != nil || !receiptInfo.Mode().IsRegular() || receiptInfo.Mode()&os.ModeSymlink != 0 ||
		receiptInfo.Mode().Perm()&0o222 != 0 || receiptInfo.Size() <= 0 || receiptInfo.Size() > 4096 {
		return false
	}
	raw, err := os.ReadFile(receiptPath)
	if err != nil || len(raw) == 0 || len(raw) > 4096 {
		return false
	}
	var receipt kernelMaterializationReceipt
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	return receipt.Schema == kernelMaterializationReceiptSchema &&
		receipt.VersionID == versionID && receipt.Filename == filename &&
		filepath.Clean(receipt.MaterializedPath) == filepath.Clean(materializedPath) &&
		receipt.SizeBytes == sizeBytes && strings.EqualFold(receipt.ContentSHA256, contentSHA256) &&
		receipt.ModTimeUnixNano == info.ModTime().UnixNano() &&
		receipt.Mode == uint32(info.Mode().Perm()) && info.Mode().Perm()&0o222 == 0
}

func writeKernelMaterializationReceipt(
	receiptPath, materializedPath, versionID, filename string,
	sizeBytes int64,
	contentSHA256 string,
) error {
	info, err := os.Lstat(materializedPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != sizeBytes || info.Mode().Perm()&0o222 != 0 {
		return errors.New("materialized artifact receipt source is invalid")
	}
	receipt := kernelMaterializationReceipt{
		Schema: kernelMaterializationReceiptSchema, VersionID: versionID, Filename: filename,
		MaterializedPath: filepath.Clean(materializedPath),
		SizeBytes:        sizeBytes, ContentSHA256: strings.ToLower(strings.TrimSpace(contentSHA256)),
		ModTimeUnixNano: info.ModTime().UnixNano(), Mode: uint32(info.Mode().Perm()),
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(receiptPath), ".materialization-receipt-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o400); err != nil {
		return err
	}
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, receiptPath); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *Server) kernelMaterializationReceiptRoot() (string, error) {
	if s == nil || strings.TrimSpace(s.fileRoot) == "" {
		return "", errors.New("kernel materialization receipt authority is unavailable")
	}
	root, err := canonicalHostDirectory(s.fileRoot)
	if err != nil {
		return "", errors.New("kernel materialization receipt authority is unavailable")
	}
	receiptRoot, err := secureEnsureAgentWorkspaceDirectory(
		filepath.Join(root, "kernel-materialization-receipts"), 0o700,
	)
	info, statErr := os.Lstat(receiptRoot)
	if err != nil || statErr != nil || !managedExecutionPathWithinRoot(root, receiptRoot) ||
		!info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("kernel materialization receipt authority is unsafe")
	}
	return receiptRoot, nil
}
