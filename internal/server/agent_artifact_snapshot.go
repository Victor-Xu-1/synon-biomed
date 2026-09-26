package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"synon-go/internal/runtimecontrol"
)

func snapshotAgentSavedArtifact(
	ctx context.Context,
	artifactSource agentSavedArtifactSource,
) (*os.File, int64, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if artifactSource.rootHandle == nil {
		return nil, 0, "", errors.New("saved artifact root authority is unavailable")
	}
	source, err := artifactSource.rootHandle.Open(artifactSource.relativePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, "", err
		}
		return nil, 0, "", errors.Join(errAgentSavedArtifactPathUnauthorized, err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return nil, 0, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, "", errAgentSavedArtifactNotRegular
	}
	snapshot, err := os.CreateTemp("", "synon-save-artifact-*")
	if err != nil {
		return nil, 0, "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = snapshot.Close()
			_ = os.Remove(snapshot.Name())
		}
	}()
	hasher := sha256.New()
	reader := &contextReader{ctx: ctx, reader: source}
	// The measured source length fences a finite snapshot, not an arbitrary
	// product limit. Growing or shrinking files are retried after their producer
	// finishes; a constantly growing log must not consume disk indefinitely.
	written, err := io.CopyN(io.MultiWriter(runtimecontrol.DiskCapacityWriter(ctx, snapshot, snapshot.Name()), hasher), reader, info.Size())
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, 0, "", errAgentSavedArtifactSourceChanged
		}
		return nil, 0, "", err
	}
	var extra [1]byte
	if n, err := reader.Read(extra[:]); n != 0 || err == nil {
		return nil, 0, "", errAgentSavedArtifactSourceChanged
	} else if !errors.Is(err, io.EOF) {
		return nil, 0, "", err
	}
	after, err := source.Stat()
	if err != nil {
		return nil, 0, "", err
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, 0, "", errAgentSavedArtifactSourceChanged
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return nil, 0, "", err
	}
	keep = true
	return snapshot, written, hex.EncodeToString(hasher.Sum(nil)), nil
}

func digestAgentSavedArtifactSnapshot(snapshot *os.File) (int64, string, error) {
	if snapshot == nil {
		return 0, "", errors.New("saved artifact snapshot is unavailable")
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	hasher := sha256.New()
	sizeBytes, err := io.Copy(hasher, snapshot)
	if err != nil {
		return 0, "", err
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	return sizeBytes, hex.EncodeToString(hasher.Sum(nil)), nil
}
