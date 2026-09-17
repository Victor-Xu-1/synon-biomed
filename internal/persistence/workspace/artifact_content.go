package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
)

type ArtifactContentReader interface {
	io.Reader
	io.Seeker
	io.Closer
}

type inlineArtifactReader struct {
	*bytes.Reader
}

func (inlineArtifactReader) Close() error { return nil }

const maxHydratedArtifactVersionBytes = 64 << 20

const artifactVersionJoinSelect = `
	SELECT artifact.id, artifact.project_id, artifact.name, artifact.kind,
		artifact.current_version_number, COALESCE(artifact.folder_id, ''), artifact.priority, artifact.created_at, artifact.updated_at,
		version.id, version.artifact_id, version.version_number,
		COALESCE(version.parent_id, ''), version.content, version.content_sha256,
		COALESCE(version.storage_path, ''),
		CASE WHEN COALESCE(version.storage_path, '') = '' THEN length(version.content) ELSE version.size_bytes END,
		version.created_by, version.created_at
	FROM artifact_versions AS version
	JOIN artifacts AS artifact ON artifact.id = version.artifact_id`

func (s *Store) GetArtifactVersion(versionID string) (Artifact, ArtifactVersion, bool, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, false, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(versionID) == "" {
		return Artifact{}, ArtifactVersion{}, false, errors.New("artifact version id is required")
	}
	artifact, version, found, err := scanArtifactVersionRow(s.db.QueryRowContext(
		context.Background(), artifactVersionJoinSelect+` WHERE version.id = ?`, versionID,
	))
	return s.hydrateArtifactVersion(artifact, version, found, err)
}

func (s *Store) GetCurrentArtifactVersion(artifactID string) (Artifact, ArtifactVersion, bool, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, false, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(artifactID) == "" {
		return Artifact{}, ArtifactVersion{}, false, errors.New("artifact id is required")
	}
	artifact, version, found, err := scanArtifactVersionRow(s.db.QueryRowContext(
		context.Background(),
		artifactVersionJoinSelect+` WHERE artifact.id = ? AND version.version_number = artifact.current_version_number`,
		artifactID,
	))
	return s.hydrateArtifactVersion(artifact, version, found, err)
}

func (s *Store) GetCurrentArtifactVersionMetadata(artifactID string) (Artifact, ArtifactVersion, bool, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, false, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(artifactID) == "" {
		return Artifact{}, ArtifactVersion{}, false, errors.New("artifact id is required")
	}
	return scanArtifactVersionRow(s.db.QueryRowContext(
		context.Background(),
		artifactVersionJoinSelect+` WHERE artifact.id = ? AND version.version_number = artifact.current_version_number`,
		artifactID,
	))
}

func (s *Store) GetArtifactVersionMetadata(versionID string) (Artifact, ArtifactVersion, bool, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, false, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(versionID) == "" {
		return Artifact{}, ArtifactVersion{}, false, errors.New("artifact version id is required")
	}
	return scanArtifactVersionRow(s.db.QueryRowContext(
		context.Background(), artifactVersionJoinSelect+` WHERE version.id = ?`, versionID,
	))
}

func (s *Store) OpenCurrentArtifactContent(artifactID string) (Artifact, ArtifactVersion, ArtifactContentReader, bool, error) {
	artifact, version, found, err := s.GetCurrentArtifactVersionMetadata(artifactID)
	if err != nil || !found {
		return artifact, version, nil, found, err
	}
	reader, err := s.openArtifactVersionContent(version)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, nil, false, err
	}
	return artifact, version, reader, true, nil
}

func (s *Store) OpenArtifactVersionContent(versionID string) (Artifact, ArtifactVersion, ArtifactContentReader, bool, error) {
	if s == nil || s.db == nil {
		return Artifact{}, ArtifactVersion{}, nil, false, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(versionID) == "" {
		return Artifact{}, ArtifactVersion{}, nil, false, errors.New("artifact version id is required")
	}
	artifact, version, found, err := scanArtifactVersionRow(s.db.QueryRowContext(
		context.Background(), artifactVersionJoinSelect+` WHERE version.id = ?`, versionID,
	))
	if err != nil || !found {
		return artifact, version, nil, found, err
	}
	reader, err := s.openArtifactVersionContent(version)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, nil, false, err
	}
	return artifact, version, reader, true, nil
}

func (s *Store) hydrateArtifactVersion(artifact Artifact, version ArtifactVersion, found bool, err error) (Artifact, ArtifactVersion, bool, error) {
	if err != nil || !found || version.StoragePath == "" {
		return artifact, version, found, err
	}
	if version.SizeBytes > maxHydratedArtifactVersionBytes {
		return Artifact{}, ArtifactVersion{}, false, fmt.Errorf(
			"artifact version is %d bytes; use streaming content access above %d bytes",
			version.SizeBytes, maxHydratedArtifactVersionBytes,
		)
	}
	reader, openErr := s.openArtifactVersionContent(version)
	if openErr != nil {
		return Artifact{}, ArtifactVersion{}, false, openErr
	}
	defer reader.Close()
	content, readErr := io.ReadAll(io.LimitReader(reader, maxHydratedArtifactVersionBytes+1))
	if readErr != nil {
		return Artifact{}, ArtifactVersion{}, false, fmt.Errorf("read artifact version blob: %w", readErr)
	}
	version.Content = content
	return artifact, version, true, nil
}

func (s *Store) openArtifactVersionContent(version ArtifactVersion) (ArtifactContentReader, error) {
	available, found, err := s.ArtifactVersionContentAvailable(version.ID)
	if err != nil {
		return nil, err
	}
	if found && !available {
		return nil, ErrArtifactContentPruned
	}
	if version.StoragePath == "" {
		return inlineArtifactReader{Reader: bytes.NewReader(version.Content)}, nil
	}
	absolute, err := s.blobAbsolute(version.StoragePath)
	if err != nil {
		return nil, err
	}
	file, err := openRegularFile(absolute)
	if err != nil {
		return nil, fmt.Errorf("open artifact version blob: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat artifact version blob: %w", err)
	}
	if info.Size() != version.SizeBytes {
		_ = file.Close()
		return nil, fmt.Errorf("artifact version blob size mismatch: got %d, want %d", info.Size(), version.SizeBytes)
	}
	return file, nil
}

func (s *Store) CopyArtifact(sourceArtifactID, targetProjectID, newArtifactID, name, createdBy string) (Artifact, ArtifactVersion, error) {
	return s.copyArtifact(context.Background(), sourceArtifactID, targetProjectID, newArtifactID, name, createdBy, "")
}

func (s *Store) CopyArtifactRealtime(ctx context.Context, sourceArtifactID, targetProjectID, newArtifactID, name, createdBy, ownerUserID string) (Artifact, ArtifactVersion, error) {
	return s.copyArtifact(ctx, sourceArtifactID, targetProjectID, newArtifactID, name, createdBy, ownerUserID)
}

func (s *Store) copyArtifact(ctx context.Context, sourceArtifactID, targetProjectID, newArtifactID, name, createdBy, ownerUserID string) (Artifact, ArtifactVersion, error) {
	if strings.TrimSpace(sourceArtifactID) == "" || strings.TrimSpace(targetProjectID) == "" ||
		strings.TrimSpace(newArtifactID) == "" {
		return Artifact{}, ArtifactVersion{}, errors.New("source artifact, target project, and new artifact ids are required")
	}
	source, sourceVersion, content, found, err := s.OpenCurrentArtifactContent(sourceArtifactID)
	if err != nil {
		return Artifact{}, ArtifactVersion{}, err
	}
	if !found {
		return Artifact{}, ArtifactVersion{}, fmt.Errorf("artifact %q has no current version", sourceArtifactID)
	}
	defer content.Close()
	if strings.TrimSpace(name) == "" {
		name = source.Name + " Copy"
	}
	input := SaveArtifactVersionReaderInput{
		ArtifactID: newArtifactID, ProjectID: targetProjectID, Name: name,
		Kind: source.Kind, Content: content, CreatedBy: createdBy,
	}
	if strings.TrimSpace(ownerUserID) != "" {
		return s.WriteArtifactVersionRealtime(ctx, WriteArtifactVersionInput{
			ArtifactID: input.ArtifactID, ProjectID: input.ProjectID, Name: input.Name,
			ContentType: input.Kind, Content: input.Content, CreatedBy: input.CreatedBy,
			ProvenanceSourceID: sourceVersion.ID, ReadSourceProjectID: source.ProjectID,
		}, ownerUserID)
	}
	return s.SaveArtifactVersionFromReader(ctx, input)
}

type artifactVersionRow interface {
	Scan(dest ...any) error
}

func scanArtifactVersionRow(row artifactVersionRow) (Artifact, ArtifactVersion, bool, error) {
	var artifact Artifact
	var version ArtifactVersion
	err := row.Scan(
		&artifact.ID, &artifact.ProjectID, &artifact.Name, &artifact.Kind,
		&artifact.CurrentVersionNumber, &artifact.FolderID, &artifact.Priority, &artifact.CreatedAt, &artifact.UpdatedAt,
		&version.ID, &version.ArtifactID, &version.VersionNumber, &version.ParentID,
		&version.Content, &version.ContentSHA256, &version.StoragePath, &version.SizeBytes,
		&version.CreatedBy, &version.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ArtifactVersion{}, false, nil
	}
	if err != nil {
		return Artifact{}, ArtifactVersion{}, false, fmt.Errorf("get artifact version: %w", err)
	}
	return artifact, version, true, nil
}
