package workspace

import (
	"context"
	"io"
)

type SaveArtifactVersionReaderInput struct {
	ArtifactID      string
	ProjectID       string
	Name            string
	Kind            string
	Content         io.Reader
	CreatedBy       string
	MaxBytes        int64
	ParentVersionID string
}

func (s *Store) SaveArtifactVersionFromReader(ctx context.Context, input SaveArtifactVersionReaderInput) (Artifact, ArtifactVersion, error) {
	return s.WriteArtifactVersion(ctx, WriteArtifactVersionInput{
		ArtifactID: input.ArtifactID, ProjectID: input.ProjectID, Name: input.Name,
		ContentType: input.Kind, Content: input.Content, CreatedBy: input.CreatedBy,
		MaxBytes: input.MaxBytes, ParentVersionID: input.ParentVersionID,
	})
}

func (s *Store) SaveArtifactVersionFromReaderRealtime(ctx context.Context, input SaveArtifactVersionReaderInput, ownerUserID string) (Artifact, ArtifactVersion, error) {
	return s.WriteArtifactVersionRealtime(ctx, WriteArtifactVersionInput{
		ArtifactID: input.ArtifactID, ProjectID: input.ProjectID, Name: input.Name,
		ContentType: input.Kind, Content: input.Content, CreatedBy: input.CreatedBy,
		MaxBytes: input.MaxBytes, ParentVersionID: input.ParentVersionID,
	}, ownerUserID)
}
