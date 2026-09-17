package transcript

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryValidateContractRejectsIncompleteV23V24Schema(t *testing.T) {
	repo, db, _ := newTranscriptRepository(t)
	if err := repo.ValidateContract(context.Background()); err != nil {
		t.Fatalf("valid contract: %v", err)
	}
	if _, err := db.Exec(`DROP INDEX transcript_artifact_refs_version`); err != nil {
		t.Fatal(err)
	}
	if err := repo.ValidateContract(context.Background()); !errors.Is(err, ErrSchemaUnavailable) {
		t.Fatalf("missing v24 reverse index error=%v", err)
	}
	if _, err := db.Exec(`CREATE INDEX transcript_artifact_refs_version
		ON transcript_artifact_refs(artifact_id,version_id,stream_uid,runner_attempt,source_event_id)`); err != nil {
		t.Fatal(err)
	}
	if err := repo.ValidateContract(context.Background()); err != nil {
		t.Fatalf("repaired contract: %v", err)
	}
}

func TestRepositoryValidateContractRejectsCascadingArtifactSourceRetention(t *testing.T) {
	_, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`ALTER TABLE transcript_artifact_refs RENAME TO transcript_artifact_refs_v24`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE transcript_artifact_refs (
		stream_uid TEXT NOT NULL,
		runner_attempt INTEGER NOT NULL,
		source_event_id INTEGER NOT NULL,
		ordinal INTEGER NOT NULL,
		artifact_id TEXT NOT NULL,
		version_id TEXT NOT NULL,
		relation TEXT NOT NULL,
		availability TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		FOREIGN KEY(stream_uid,runner_attempt,source_event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE CASCADE
	)`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	if err := repo.ValidateContract(context.Background()); !errors.Is(err, ErrSchemaUnavailable) {
		t.Fatalf("cascading retention error=%v", err)
	}
}

func TestRepositoryValidateContractRejectsIndexesBoundToSupersededTable(t *testing.T) {
	_, db, _ := newTranscriptRepository(t)
	if _, err := db.Exec(`ALTER TABLE transcript_artifact_refs RENAME TO transcript_artifact_refs_v24`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE transcript_artifact_refs (
		stream_uid TEXT NOT NULL,
		runner_attempt INTEGER NOT NULL,
		source_event_id INTEGER NOT NULL,
		ordinal INTEGER NOT NULL,
		artifact_id TEXT NOT NULL,
		version_id TEXT NOT NULL,
		relation TEXT NOT NULL,
		availability TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		FOREIGN KEY(stream_uid,runner_attempt,source_event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE RESTRICT
	)`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	if err := repo.ValidateContract(context.Background()); !errors.Is(err, ErrSchemaUnavailable) {
		t.Fatalf("superseded index binding error=%v", err)
	}
}

func TestRepositoryValidateContractRejectsMalformedBranchAuthority(t *testing.T) {
	tests := []struct {
		name           string
		oldFragment    string
		newFragment    string
		extraStatement string
	}{
		{
			name: "parent branch crosses the lineage key",
			oldFragment: "FOREIGN KEY(stream_uid,parent_branch_id)\n\t\t\tREFERENCES transcript_branches(stream_uid,branch_id)\n\t\t\t" +
				"ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED",
			newFragment: "FOREIGN KEY(stream_uid,parent_branch_id)\n\t\t\tREFERENCES transcript_branches(stream_uid,client_mutation_id)\n\t\t\t" +
				"ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED",
		},
		{
			name: "active branch crosses the lineage key",
			oldFragment: "FOREIGN KEY(stream_uid,active_branch_id)\n\t\t\tREFERENCES transcript_branches(stream_uid,branch_id)\n\t\t\t" +
				"ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED",
			newFragment: "FOREIGN KEY(stream_uid,active_branch_id)\n\t\t\tREFERENCES transcript_branches(stream_uid,client_mutation_id)\n\t\t\t" +
				"ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED",
		},
		{
			name: "fork event cascades",
			oldFragment: "FOREIGN KEY(stream_uid,fork_event_id)\n\t\t\tREFERENCES transcript_events(stream_uid,event_id)\n\t\t\t" +
				"ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED",
			newFragment: "FOREIGN KEY(stream_uid,fork_event_id)\n\t\t\tREFERENCES transcript_events(stream_uid,event_id) ON DELETE CASCADE",
		},
		{
			name: "membership does not cascade with its branch",
			oldFragment: "FOREIGN KEY(stream_uid,branch_id)\n\t\t\tREFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,\n\t\t" +
				"FOREIGN KEY(stream_uid,event_id)",
			newFragment: "FOREIGN KEY(stream_uid,branch_id)\n\t\t\tREFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION,\n\t\t" +
				"FOREIGN KEY(stream_uid,event_id)",
		},
		{
			name: "membership references publication sequence",
			oldFragment: "FOREIGN KEY(stream_uid,event_id)\n\t\t\tREFERENCES transcript_events(stream_uid,event_id)\n\t\t\t" +
				"ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED",
			newFragment: "FOREIGN KEY(stream_uid,event_id)\n\t\t\tREFERENCES transcript_events(stream_uid,publication_seq)\n\t\t\t" +
				"ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED",
		},
		{
			name:        "branch primary key order drifts",
			oldFragment: "PRIMARY KEY(stream_uid,branch_id)",
			newFragment: "PRIMARY KEY(branch_id,stream_uid)",
		},
		{
			name:        "event lookup index order drifts",
			oldFragment: "ON transcript_branch_events(stream_uid,event_id,branch_id)",
			newFragment: "ON transcript_branch_events(stream_uid,branch_id,event_id)",
		},
		{
			name:           "mutation uniqueness is partial",
			oldFragment:    "UNIQUE(stream_uid,client_mutation_id),",
			newFragment:    "CHECK(length(client_mutation_id)>0),",
			extraStatement: "CREATE UNIQUE INDEX malformed_partial_branch_mutation ON transcript_branches(stream_uid,client_mutation_id) WHERE kind='base'",
		},
		{
			name:           "membership uniqueness is partial",
			oldFragment:    "UNIQUE(stream_uid,branch_id,event_id),",
			newFragment:    "CHECK(event_id>0),",
			extraStatement: "CREATE UNIQUE INDEX malformed_partial_branch_membership ON transcript_branch_events(stream_uid,branch_id,event_id) WHERE ordinal>1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newMutatedBranchContract(t, test.oldFragment, test.newFragment, test.extraStatement)
			if err := repo.ValidateContract(context.Background()); !errors.Is(err, ErrSchemaUnavailable) {
				t.Fatalf("malformed branch contract error=%v", err)
			}
		})
	}
}

func TestRepositoryValidateContractRejectsMalformedHistoryClassificationAuthority(t *testing.T) {
	tests := []struct {
		name, object, oldFragment, newFragment string
	}{
		{
			name: "run primary key", object: "transcript_history_classification_runs",
			oldFragment: "run_id BLOB PRIMARY KEY", newFragment: "run_id BLOB UNIQUE",
		},
		{
			name: "run branch retention", object: "transcript_history_classification_runs",
			oldFragment: "REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE",
			newFragment: "REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION",
		},
		{
			name: "candidate primary key order", object: "transcript_history_classification_candidates",
			oldFragment: "PRIMARY KEY(run_id,candidate_id)", newFragment: "PRIMARY KEY(candidate_id,run_id)",
		},
		{
			name: "candidate run retention", object: "transcript_history_classification_candidates",
			oldFragment: "REFERENCES transcript_history_classification_runs(run_id) ON DELETE CASCADE",
			newFragment: "REFERENCES transcript_history_classification_runs(run_id) ON DELETE NO ACTION",
		},
		{
			name: "candidate disposition", object: "transcript_history_classification_candidates",
			oldFragment: "'native_v1','eligible','poison','conflict'", newFragment: "'native_v1','eligible','poison','migration'",
		},
		{
			name: "candidate count semantic cap", object: "transcript_history_classification_runs",
			oldFragment: "candidate_count INTEGER NOT NULL CHECK(candidate_count >= 0)",
			newFragment: "candidate_count INTEGER NOT NULL CHECK(candidate_count >= 0 AND candidate_count <= 256)",
		},
		{
			name: "tool use id semantic cap", object: "transcript_history_classification_candidates",
			oldFragment: "tool_use_id TEXT NOT NULL,",
			newFragment: "tool_use_id TEXT NOT NULL CHECK(length(tool_use_id) <= 512),",
		},
		{
			name: "state json semantic cap", object: "transcript_history_classification_candidates",
			oldFragment: "state_json BLOB CHECK(state_json IS NULL OR json_valid(state_json))",
			newFragment: "state_json BLOB CHECK(state_json IS NULL OR (json_valid(state_json) AND length(state_json) <= 65536))",
		},
		{
			name: "candidate evidence semantic cap", object: "transcript_history_classification_candidates",
			oldFragment: "evidence_json BLOB NOT NULL CHECK(json_valid(evidence_json))",
			newFragment: "evidence_json BLOB NOT NULL CHECK(json_valid(evidence_json) AND length(evidence_json) <= 262144)",
		},
		{
			name: "shadow evidence semantic cap", object: "transcript_history_shadow_comparisons",
			oldFragment: "evidence_json BLOB NOT NULL CHECK(json_valid(evidence_json))",
			newFragment: "evidence_json BLOB NOT NULL CHECK(json_valid(evidence_json) AND length(evidence_json) <= 262144)",
		},
		{
			name: "shadow primary key order", object: "transcript_history_shadow_comparisons",
			oldFragment: "PRIMARY KEY(run_id,dimension)", newFragment: "PRIMARY KEY(dimension,run_id)",
		},
		{
			name: "shadow run retention", object: "transcript_history_shadow_comparisons",
			oldFragment: "REFERENCES transcript_history_classification_runs(run_id) ON DELETE CASCADE",
			newFragment: "REFERENCES transcript_history_classification_runs(run_id) ON DELETE NO ACTION",
		},
		{
			name: "shadow verdict", object: "transcript_history_shadow_comparisons",
			oldFragment: "'match','mismatch','blocked'", newFragment: "'match','mismatch','unknown'",
		},
		{
			name: "latest order", object: "transcript_history_classification_runs_latest",
			oldFragment: "through_publication_seq DESC", newFragment: "through_publication_seq ASC",
		},
		{
			name: "findings order", object: "transcript_history_classification_findings",
			oldFragment: "disposition,reason_code,run_id,candidate_id", newFragment: "reason_code,disposition,run_id,candidate_id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newMutatedHistoryContract(t, test.object, test.oldFragment, test.newFragment)
			if err := repo.ValidateContract(context.Background()); !errors.Is(err, ErrSchemaUnavailable) {
				t.Fatalf("malformed history contract error=%v", err)
			}
		})
	}
}

func newMutatedHistoryContract(t *testing.T, object, oldFragment, newFragment string) *Repository {
	t.Helper()
	path := filepath.Join(t.TempDir(), "malformed-history.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(transcriptExternalAuthorityFixture); err != nil {
		t.Fatal(err)
	}
	replacements := 0
	for index, statement := range SchemaContractStatements() {
		if (strings.Contains(statement, "CREATE TABLE "+object) || strings.Contains(statement, "CREATE INDEX "+object)) &&
			strings.Contains(statement, oldFragment) {
			statement = strings.Replace(statement, oldFragment, newFragment, 1)
			replacements++
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("schema statement %d: %v", index, err)
		}
	}
	if replacements != 1 {
		t.Fatalf("history schema mutation replacements=%d", replacements)
	}
	return NewRepository(db)
}

func newMutatedBranchContract(t *testing.T, oldFragment, newFragment, extraStatement string) *Repository {
	t.Helper()
	path := filepath.Join(t.TempDir(), "malformed-branch.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(transcriptExternalAuthorityFixture); err != nil {
		t.Fatal(err)
	}
	replacements := 0
	for index, statement := range SchemaContractStatements() {
		if replacements == 0 && strings.Contains(statement, oldFragment) {
			statement = strings.Replace(statement, oldFragment, newFragment, 1)
			replacements++
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("schema statement %d: %v", index, err)
		}
	}
	if replacements != 1 {
		t.Fatalf("schema mutation replacements=%d", replacements)
	}
	if extraStatement != "" {
		if _, err := db.Exec(extraStatement); err != nil {
			t.Fatal(err)
		}
	}
	return NewRepository(db)
}
