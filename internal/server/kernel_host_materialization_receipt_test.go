package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"
)

type kernelMaterializationTestReader struct {
	*bytes.Reader
	readErr error
}

func (reader *kernelMaterializationTestReader) Read(buffer []byte) (int, error) {
	if reader.readErr != nil {
		return 0, reader.readErr
	}
	return reader.Reader.Read(buffer)
}

func (*kernelMaterializationTestReader) Close() error { return nil }

func TestKernelMaterializationUsesHostReceiptForConstantTimeReuse(t *testing.T) {
	workspace := t.TempDir()
	receiptRoot := t.TempDir()
	content := []byte("immutable attachment bytes")
	digest := sha256.Sum256(content)
	sha := hex.EncodeToString(digest[:])
	first, err := materializeKernelImmutableContent(
		context.Background(), workspace, receiptRoot, "version-one", "input.bin", int64(len(content)), sha,
		&kernelMaterializationTestReader{Reader: bytes.NewReader(content)},
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt := kernelMaterializationReceiptPath(receiptRoot, workspace, "version-one")
	if info, err := os.Stat(receipt); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("materialization receipt is unavailable: info=%v err=%v", info, err)
	}

	readFailure := errors.New("source bytes must not be read on receipt-backed reuse")
	second, err := materializeKernelImmutableContent(
		context.Background(), workspace, receiptRoot, "version-one", "input.bin", int64(len(content)), sha,
		&kernelMaterializationTestReader{Reader: bytes.NewReader(nil), readErr: readFailure},
	)
	if err != nil || second != first {
		t.Fatalf("receipt-backed reuse path=%q err=%v", second, err)
	}

	if err := os.Chmod(first, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(first, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := io.WriteString(file, "modified attachment bytes")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("tamper fixture failed: write=%v close=%v", writeErr, closeErr)
	}
	repaired, err := materializeKernelImmutableContent(
		context.Background(), workspace, receiptRoot, "version-one", "input.bin", int64(len(content)), sha,
		&kernelMaterializationTestReader{Reader: bytes.NewReader(content)},
	)
	if err != nil {
		t.Fatalf("damaged managed cache did not recover from immutable source: %v", err)
	}
	actual, err := os.ReadFile(repaired)
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatal("a modified materialized artifact was accepted through its stale receipt")
	}
}
