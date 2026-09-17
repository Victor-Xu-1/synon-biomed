package detached

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

// ExecutionResultV1 is the durable, cross-platform result contract shared by
// the Linux executor and every controller that verifies its receipt.
type ExecutionResultV1 struct {
	Version     int                    `json:"version"`
	ExecutionID string                 `json:"execution_id"`
	ToolCallID  string                 `json:"tool_call_id"`
	KernelID    string                 `json:"kernel_id"`
	KernelKind  string                 `json:"kernel_kind"`
	Language    string                 `json:"language"`
	Environment string                 `json:"environment"`
	Reused      bool                   `json:"reused"`
	Response    kernelruntime.Response `json:"response"`
	Error       string                 `json:"error,omitempty"`
	TimedOut    bool                   `json:"timed_out"`
	CellIndex   int                    `json:"cell_index"`
	StartedAt   time.Time              `json:"started_at"`
	FinishedAt  time.Time              `json:"finished_at"`
	Generation  uint64                 `json:"generation"`
}

type spoolPointerV1 struct {
	Version     int    `json:"version"`
	ExecutionID string `json:"execution_id"`
	Kind        string `json:"kind"`
	SHA256      string `json:"sha256"`
	Bytes       int    `json:"bytes"`
}

// LoadExecutionResult verifies both the immutable receipt and an optional
// content-addressed spool before returning the exact executor outcome. The
// caller may remove cleanupPath only after canonical result settlement commits.
func LoadExecutionResult(
	spoolDir string,
	receipt workspace.KernelExecutionResultReceipt,
) (result ExecutionResultV1, cleanupPath string, err error) {
	if receipt.ResultJSON == "" || receipt.ResultSHA256 == "" {
		return ExecutionResultV1{}, "", errors.New("detached kernel result receipt is incomplete")
	}
	receiptDigest := sha256.Sum256([]byte(receipt.ResultJSON))
	if hex.EncodeToString(receiptDigest[:]) != receipt.ResultSHA256 {
		return ExecutionResultV1{}, "", errors.New("detached kernel result receipt digest conflicts with content")
	}
	raw := []byte(receipt.ResultJSON)
	if receipt.ResultRef != "" {
		const prefix = "kernel-spool-sha256:"
		if !strings.HasPrefix(receipt.ResultRef, prefix) || len(receipt.ResultRef) != len(prefix)+64 {
			return ExecutionResultV1{}, "", errors.New("detached kernel result reference is unsupported")
		}
		spoolSHA := strings.TrimPrefix(receipt.ResultRef, prefix)
		var pointer spoolPointerV1
		if err := decodeClosedResultJSON(raw, &pointer); err != nil || pointer.Version != 1 ||
			pointer.ExecutionID != receipt.ExecutionID || pointer.Kind != "kernel-spool-v1" ||
			pointer.SHA256 != spoolSHA || pointer.Bytes <= 1048576 {
			return ExecutionResultV1{}, "", errors.New("detached kernel result pointer is invalid")
		}
		spoolDir = filepath.Clean(strings.TrimSpace(spoolDir))
		if !filepath.IsAbs(spoolDir) {
			return ExecutionResultV1{}, "", errors.New("detached kernel result spool is unavailable")
		}
		cleanupPath = filepath.Join(spoolDir, spoolSHA+".json")
		if filepath.Dir(cleanupPath) != spoolDir {
			return ExecutionResultV1{}, "", errors.New("detached kernel result spool path is invalid")
		}
		raw, err = os.ReadFile(cleanupPath)
		if err != nil || len(raw) != pointer.Bytes {
			return ExecutionResultV1{}, "", errors.New("detached kernel result spool is unavailable")
		}
		fullDigest := sha256.Sum256(raw)
		expected, decodeErr := hex.DecodeString(spoolSHA)
		if decodeErr != nil || len(expected) != len(fullDigest) ||
			subtle.ConstantTimeCompare(fullDigest[:], expected) != 1 {
			return ExecutionResultV1{}, "", errors.New("detached kernel result spool digest conflicts with content")
		}
	}
	if err := decodeClosedResultJSON(raw, &result); err != nil || result.Version != 1 ||
		result.ExecutionID != receipt.ExecutionID || result.KernelID == "" || result.ToolCallID == "" ||
		result.StartedAt.IsZero() || result.FinishedAt.Before(result.StartedAt) {
		return ExecutionResultV1{}, "", errors.New("detached kernel result payload is invalid")
	}
	return result, cleanupPath, nil
}

func decodeClosedResultJSON(raw []byte, target any) error {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return errors.New("detached kernel result JSON is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("detached kernel result JSON is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("detached kernel result must contain one JSON object")
	}
	return nil
}
