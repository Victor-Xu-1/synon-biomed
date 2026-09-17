package workspaceimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	workspace "synon-go/internal/persistence/workspace"

	_ "modernc.org/sqlite"
)

var countedTables = []string{
	"projects", "frames", "frame_events", "artifacts", "artifact_versions", "attachments",
	"transcript_streams", "transcript_events", "transcript_runner_attempts", "transcript_delivery_intents",
}

type sqlQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type fileManifest struct {
	Bytes        int64
	Files        int
	SHA256       string
	HasLegacy    bool
	ContainsData bool
}

func Inspect(ctx context.Context, options Options) (Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(options.SourcePath) == "" {
		return Plan{}, errors.New("workspace import source path is required")
	}
	discovered, err := discoverSources(options.SourcePath)
	if err != nil {
		return Plan{}, err
	}
	expected, err := workspace.ExpectedSchemaStatus()
	if err != nil {
		return Plan{}, fmt.Errorf("load compiled workspace schema contract: %w", err)
	}
	plan := Plan{Version: PlanVersion, Sources: []Source{}, Issues: []Issue{}}
	targetDatabase := ""
	if strings.TrimSpace(options.TargetHome) != "" {
		candidate := filepath.Join(options.TargetHome, filepath.FromSlash(WorkspaceDatabaseRelative))
		if canonical, info, targetErr := canonicalExistingPath(candidate); targetErr == nil && info.Mode().IsRegular() {
			targetDatabase = canonical
		}
	}
	seenSourceIDs := map[string]struct{}{}
	for _, candidate := range discovered {
		source, issues := inspectSource(ctx, candidate, expected, options.IncludePaths)
		if _, duplicate := seenSourceIDs[source.ID]; source.ID != "" && duplicate {
			issues = append(issues, Issue{Code: "duplicate_source_identity", SourceID: source.ID})
		}
		seenSourceIDs[source.ID] = struct{}{}
		if targetDatabase != "" && candidate.database == targetDatabase {
			issues = append(issues, Issue{Code: "source_is_target_database", SourceID: source.ID})
		}
		plan.Sources = append(plan.Sources, source)
		plan.Issues = append(plan.Issues, issues...)
		var addErr error
		plan.EstimatedSourceBytes, addErr = checkedAdd(plan.EstimatedSourceBytes, source.DatabaseBytes)
		if addErr == nil {
			plan.EstimatedSourceBytes, addErr = checkedAdd(plan.EstimatedSourceBytes, source.ExternalBytes)
		}
		if addErr != nil {
			plan.Issues = append(plan.Issues, Issue{Code: "source_size_overflow", SourceID: source.ID})
		}
	}
	sort.SliceStable(plan.Sources, func(i, j int) bool { return plan.Sources[i].ID < plan.Sources[j].ID })
	plan.EstimatedStagingBytes, err = checkedStagingBytes(plan.EstimatedSourceBytes)
	if err != nil {
		plan.Issues = append(plan.Issues, Issue{Code: "staging_size_overflow"})
	}
	if strings.TrimSpace(options.TargetHome) == "" {
		plan.Issues = append(plan.Issues, Issue{Code: "target_capacity_not_measured"})
	} else {
		targetManifest, manifestErr := manifestStableDirectory("target", options.TargetHome, false)
		if manifestErr != nil {
			plan.Issues = append(plan.Issues, Issue{Code: "target_snapshot_unavailable"})
		} else {
			plan.TargetSnapshotBytes = targetManifest.Bytes
			plan.TargetSnapshotFiles = targetManifest.Files
			plan.TargetSnapshotSHA256 = targetManifest.SHA256
			plan.EstimatedStagingBytes, err = checkedAdd(plan.EstimatedStagingBytes, targetManifest.Bytes)
			if err != nil {
				plan.Issues = append(plan.Issues, Issue{Code: "staging_size_overflow"})
			}
		}
		if available, measureErr := availableBytes(options.TargetHome); measureErr != nil {
			plan.Issues = append(plan.Issues, Issue{Code: "target_capacity_unavailable"})
		} else {
			plan.TargetCapacityMeasured = true
			plan.TargetAvailableBytes = available
			if available < plan.EstimatedStagingBytes {
				plan.Issues = append(plan.Issues, Issue{Code: "target_capacity_insufficient"})
			}
		}
	}
	plan.Issues = append(plan.Issues, Issue{Code: "target_collision_analysis_pending"})
	sort.Slice(plan.Issues, func(i, j int) bool {
		if plan.Issues[i].SourceID == plan.Issues[j].SourceID {
			return plan.Issues[i].Code < plan.Issues[j].Code
		}
		return plan.Issues[i].SourceID < plan.Issues[j].SourceID
	})
	plan.ReadyForStagedApply = len(plan.Issues) == 0
	digest, err := planDigest(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.PlanSHA256 = digest
	return plan, nil
}

func inspectSource(ctx context.Context, candidate discoveredSource, expected workspace.SchemaStatus, includePaths bool) (Source, []Issue) {
	source := Source{
		Kind: SourceKindUnknown, SchemaTarget: expected.TargetVersion,
		EntityCounts: map[string]int{}, ActiveAuthorities: map[string]int{},
	}
	if includePaths {
		source.DatabasePath = candidate.database
		source.DataRoot = candidate.dataRoot
	}
	digest, bytes, err := hashStableFile(candidate.database)
	if err != nil {
		return source, []Issue{{Code: "source_database_unstable"}}
	}
	source.DatabaseSHA256 = digest
	source.ID = "sha256:" + digest
	source.DatabaseBytes = bytes
	issues := []Issue{}
	if databaseHasSidecar(candidate.database) {
		issues = append(issues, Issue{Code: "source_database_has_uncommitted_sidecar", SourceID: source.ID})
		external, externalErr := manifestExternalData(candidate)
		if externalErr != nil {
			issues = append(issues, Issue{Code: "source_external_tree_invalid", SourceID: source.ID})
		} else {
			applyExternalManifest(&source, external)
		}
		return source, issues
	}

	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(candidate.database))
	if err != nil {
		return source, append(issues, Issue{Code: "source_database_open_failed", SourceID: source.ID})
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return source, append(issues, Issue{Code: "source_database_snapshot_failed", SourceID: source.ID})
	}
	defer tx.Rollback()
	var quickCheck string
	if err := tx.QueryRowContext(ctx, `PRAGMA quick_check(20)`).Scan(&quickCheck); err != nil || quickCheck != "ok" {
		return source, append(issues, Issue{Code: "source_database_integrity_failed", SourceID: source.ID})
	}
	tables, err := databaseTableNames(ctx, tx)
	if err != nil {
		return source, append(issues, Issue{Code: "source_database_catalog_failed", SourceID: source.ID})
	}
	switch {
	case tables["workspace_schema_migrations"]:
		source.Kind = SourceKindCurrent
		issues = append(issues, inspectSchemaJournal(ctx, tx, &source, expected)...)
	case tables["__drizzle_migrations"] && tables["projects"] && tables["frames"] && tables["artifacts"] && tables["artifact_versions"]:
		source.Kind = SourceKindV11
		issues = append(issues, Issue{Code: "source_requires_v11_migration", SourceID: source.ID})
	default:
		issues = append(issues, Issue{Code: "source_schema_unsupported", SourceID: source.ID})
	}
	for _, table := range countedTables {
		if !tables[table] {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdentifier(table)).Scan(&count); err != nil {
			issues = append(issues, Issue{Code: "source_entity_count_failed", SourceID: source.ID})
			continue
		}
		source.EntityCounts[table] = count
	}
	distinctOwners, unresolved, ownerIssues := inspectOwners(ctx, tx, tables, source.ID)
	source.DistinctOwners = distinctOwners
	source.UnresolvedOwners = unresolved
	issues = append(issues, ownerIssues...)
	issues = append(issues, inspectActiveAuthorities(ctx, tx, tables, &source, source.Kind == SourceKindCurrent && source.SchemaVersion == expected.TargetVersion)...)
	if err := tx.Commit(); err != nil {
		issues = append(issues, Issue{Code: "source_database_snapshot_failed", SourceID: source.ID})
	}
	external, externalErr := manifestStableExternalData(candidate)
	if externalErr != nil {
		issues = append(issues, Issue{Code: "source_external_tree_invalid", SourceID: source.ID})
	} else {
		applyExternalManifest(&source, external)
		if external.HasLegacy {
			issues = append(issues, Issue{Code: "source_external_authority_requires_conversion", SourceID: source.ID})
		}
	}
	afterDigest, afterBytes, afterErr := hashStableFile(candidate.database)
	if afterErr != nil || afterDigest != source.DatabaseSHA256 || afterBytes != source.DatabaseBytes || databaseHasSidecar(candidate.database) {
		issues = append(issues, Issue{Code: "source_database_changed_during_inspection", SourceID: source.ID})
	}
	return source, issues
}

func inspectSchemaJournal(ctx context.Context, db sqlQueryer, source *Source, expected workspace.SchemaStatus) []Issue {
	rows, err := db.QueryContext(ctx, `SELECT version,name,checksum FROM workspace_schema_migrations ORDER BY version`)
	if err != nil {
		return []Issue{{Code: "source_schema_journal_unreadable", SourceID: source.ID}}
	}
	defer rows.Close()
	type identity struct {
		version        int
		name, checksum string
	}
	identities := []identity{}
	hash := sha256.New()
	for rows.Next() {
		var item identity
		if err := rows.Scan(&item.version, &item.name, &item.checksum); err != nil {
			return []Issue{{Code: "source_schema_journal_unreadable", SourceID: source.ID}}
		}
		identities = append(identities, item)
		_, _ = fmt.Fprintf(hash, "%d\x00%s\x00%s\x00", item.version, item.name, item.checksum)
	}
	if err := rows.Err(); err != nil {
		return []Issue{{Code: "source_schema_journal_unreadable", SourceID: source.ID}}
	}
	source.SchemaJournalHash = hex.EncodeToString(hash.Sum(nil))
	if len(identities) > 0 {
		source.SchemaVersion = identities[len(identities)-1].version
	}
	if source.SchemaVersion > expected.TargetVersion {
		return []Issue{{Code: "source_schema_newer_than_binary", SourceID: source.ID}}
	}
	if len(identities) != len(expected.Migrations) {
		return []Issue{{Code: "source_schema_upgrade_required", SourceID: source.ID}}
	}
	for index, item := range identities {
		want := expected.Migrations[index]
		if item.version != want.Version || item.name != want.Name || item.checksum != want.Checksum {
			return []Issue{{Code: "source_schema_identity_mismatch", SourceID: source.ID}}
		}
	}
	return nil
}

func inspectOwners(ctx context.Context, db sqlQueryer, tables map[string]bool, sourceID string) (int, int64, []Issue) {
	owners := map[string]struct{}{}
	var unresolved int64
	tableNames := make([]string, 0, len(tables))
	for table := range tables {
		tableNames = append(tableNames, table)
	}
	sort.Strings(tableNames)
	for _, table := range tableNames {
		columns, err := ownerColumns(ctx, db, table)
		if err != nil {
			return 0, unresolved, []Issue{{Code: "source_owner_scan_failed", SourceID: sourceID}}
		}
		for _, column := range columns {
			rows, err := db.QueryContext(ctx, `SELECT `+quoteIdentifier(column)+` FROM `+quoteIdentifier(table))
			if err != nil {
				return 0, unresolved, []Issue{{Code: "source_owner_scan_failed", SourceID: sourceID}}
			}
			for rows.Next() {
				var owner sql.NullString
				if err := rows.Scan(&owner); err != nil {
					rows.Close()
					return 0, unresolved, []Issue{{Code: "source_owner_scan_failed", SourceID: sourceID}}
				}
				value := strings.TrimSpace(owner.String)
				if !owner.Valid || value == "" {
					unresolved++
					continue
				}
				owners[value] = struct{}{}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return 0, unresolved, []Issue{{Code: "source_owner_scan_failed", SourceID: sourceID}}
			}
			if err := rows.Close(); err != nil {
				return 0, unresolved, []Issue{{Code: "source_owner_scan_failed", SourceID: sourceID}}
			}
		}
	}
	issues := []Issue{}
	if unresolved > 0 {
		issues = append(issues, Issue{Code: "source_owner_unresolved", SourceID: sourceID})
	}
	return len(owners), unresolved, issues
}

func ownerColumns(ctx context.Context, db sqlQueryer, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+quoteIdentifier(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var cid int
		var name, declaredType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "user_id", "owner_id", "owner_user_id":
			result = append(result, name)
		}
	}
	return result, rows.Err()
}

type authorityQuery struct {
	key      string
	table    string
	query    string
	required bool
}

var currentAuthorityQueries = []authorityQuery{
	{key: "running_runner_attempts", table: "transcript_runner_attempts", query: `SELECT COUNT(*) FROM transcript_runner_attempts WHERE status='running'`, required: true},
	{key: "unsettled_delivery_intents", table: "transcript_delivery_intents", query: `SELECT COUNT(*) FROM transcript_delivery_intents WHERE status IN ('pending','inflight','failed')`, required: true},
	{key: "unsettled_workspace_outbox", table: "workspace_outbox", query: `SELECT COUNT(*) FROM workspace_outbox WHERE status IN ('pending','inflight')`, required: true},
	{key: "queued_user_messages", table: "queued_user_messages", query: `SELECT COUNT(*) FROM queued_user_messages WHERE state='queued'`, required: true},
	{key: "pending_frame_resume_dispatch", table: "frame_events", query: `SELECT COUNT(*) FROM frame_events WHERE event_type='frame_resumed' AND json_extract(payload,'$.dispatch.status') IN ('registered','claimed')`, required: true},
	{key: "active_frame_execution_claims", table: "frame_execution_claims", query: `SELECT COUNT(*) FROM frame_execution_claims WHERE state IN ('claimed','parked')`, required: true},
	{key: "unsettled_artifact_lineage", table: "artifact_lineage_jobs", query: `SELECT COUNT(*) FROM artifact_lineage_jobs WHERE status!='completed'`, required: true},
	{key: "pending_artifact_commits", table: "transcript_artifact_commits", query: `SELECT COUNT(*) FROM transcript_artifact_commits WHERE bound_event_id IS NULL`, required: true},
	{key: "incomplete_attachment_uploads", table: "attachment_uploads", query: `SELECT COUNT(*) FROM attachment_uploads`, required: true},
	{key: "active_compute_jobs", table: "compute_workbench_jobs", query: `SELECT COUNT(*) FROM compute_workbench_jobs WHERE state IN ('pending','staging','queued','running','harvesting')`, required: true},
	{key: "active_managed_endpoints", table: "compute_managed_endpoints", query: `SELECT COUNT(*) FROM compute_managed_endpoints WHERE state!='stopped'`, required: true},
	{key: "active_compute_leases", table: "compute_reconcile_leases", query: `SELECT COUNT(*) FROM compute_reconcile_leases WHERE expires_at>CURRENT_TIMESTAMP`, required: true},
	{key: "active_compute_pollers", table: "poller_lease", query: `SELECT COUNT(*) FROM poller_lease WHERE expires_at>CAST(strftime('%s','now') AS INTEGER)*1000`, required: true},
	{key: "pending_compute_termination", table: "compute_pending_terminate", query: `SELECT COUNT(*) FROM compute_pending_terminate`, required: true},
	{key: "claimed_routine_schedules", table: "routine_schedules", query: `SELECT COUNT(*) FROM routine_schedules WHERE locked_at IS NOT NULL OR COALESCE(claim_token,'')!=''`, required: true},
	{key: "claimed_notifications", table: "notifications", query: `SELECT COUNT(*) FROM notifications WHERE COALESCE(claim_token,'')!=''`, required: true},
	{key: "active_kernel_children", table: "kernel_child_supervision", query: `SELECT COUNT(*) FROM kernel_child_supervision WHERE status NOT IN ('completed','failed','cancelled','canceled')`},
	{key: "pending_kernel_messages", table: "kernel_child_messages", query: `SELECT COUNT(*) FROM kernel_child_messages WHERE consumed_at IS NULL`},
}

func inspectActiveAuthorities(ctx context.Context, db sqlQueryer, tables map[string]bool, source *Source, requireClosedSet bool) []Issue {
	issues := []Issue{}
	missingTable := false
	scanFailed := false
	activeAuthority := false
	for _, item := range currentAuthorityQueries {
		if !tables[item.table] {
			if requireClosedSet && item.required {
				missingTable = true
			}
			continue
		}
		var count int
		if err := db.QueryRowContext(ctx, item.query).Scan(&count); err != nil {
			scanFailed = true
			continue
		}
		source.ActiveAuthorities[item.key] = count
		if count > 0 {
			activeAuthority = true
		}
	}
	if missingTable {
		issues = append(issues, Issue{Code: "source_authority_table_missing", SourceID: source.ID})
	}
	if scanFailed {
		issues = append(issues, Issue{Code: "source_authority_scan_failed", SourceID: source.ID})
	}
	if activeAuthority {
		issues = append(issues, Issue{Code: "source_active_authority_present", SourceID: source.ID})
	}
	return issues
}

func databaseTableNames(ctx context.Context, db sqlQueryer) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

func sqliteReadOnlyDSN(path string) string {
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func hashStableFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return "", 0, errors.New("source database is not a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", 0, err
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", 0, errors.New("source database changed during inspection")
	}
	return hex.EncodeToString(hash.Sum(nil)), after.Size(), nil
}

func databaseHasSidecar(path string) bool {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return true
		}
	}
	return false
}

func manifestExternalData(source discoveredSource) (fileManifest, error) {
	type root struct {
		role   string
		path   string
		legacy bool
	}
	roots := []root{{role: "workspace-blobs", path: source.database + ".blobs"}}
	if source.dataRoot != "" {
		roots = append(roots,
			root{role: "artifacts", path: filepath.Join(source.dataRoot, "artifacts")},
			root{role: "tool-results", path: filepath.Join(source.dataRoot, "tool-results"), legacy: true},
			root{role: "compaction-history", path: filepath.Join(source.dataRoot, "compaction-history"), legacy: true},
			root{role: "sessions", path: filepath.Join(source.dataRoot, "sessions"), legacy: true},
			root{role: "session-events", path: filepath.Join(source.dataRoot, "session-events"), legacy: true},
			root{role: "task-runs", path: filepath.Join(source.dataRoot, "task_runs"), legacy: true},
		)
	}
	seen := map[string]struct{}{}
	result := fileManifest{}
	digest := sha256.New()
	writeManifestFrame(digest, []byte("synon.workspace-import.external-manifest.v1"))
	for _, candidate := range roots {
		info, err := os.Lstat(candidate.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fileManifest{}, errors.New("external data root is invalid")
		}
		canonical, _, err := canonicalExistingPath(candidate.path)
		if err != nil {
			return fileManifest{}, err
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		manifest, err := manifestDirectory(candidate.role, canonical, candidate.legacy)
		if err != nil {
			return fileManifest{}, err
		}
		result.Bytes, err = checkedAdd(result.Bytes, manifest.Bytes)
		if err != nil {
			return fileManifest{}, err
		}
		result.Files += manifest.Files
		result.HasLegacy = result.HasLegacy || (manifest.HasLegacy && manifest.ContainsData)
		result.ContainsData = result.ContainsData || manifest.ContainsData
		writeManifestFrame(digest, []byte(candidate.role))
		writeManifestFrame(digest, []byte(manifest.SHA256))
	}
	result.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return result, nil
}

func manifestStableExternalData(source discoveredSource) (fileManifest, error) {
	before, err := manifestExternalData(source)
	if err != nil {
		return fileManifest{}, err
	}
	after, err := manifestExternalData(source)
	if err != nil || before != after {
		return fileManifest{}, errors.New("external data changed during inspection")
	}
	return after, nil
}

func manifestStableDirectory(role, path string, legacy bool) (fileManifest, error) {
	before, err := manifestDirectory(role, path, legacy)
	if err != nil {
		return fileManifest{}, err
	}
	after, err := manifestDirectory(role, path, legacy)
	if err != nil || before != after {
		return fileManifest{}, errors.New("directory changed during inspection")
	}
	return after, nil
}

func manifestDirectory(role, path string, legacy bool) (fileManifest, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fileManifest{}, errors.New("manifest root is not a real directory")
	}
	result := fileManifest{HasLegacy: legacy}
	digest := sha256.New()
	writeManifestFrame(digest, []byte("synon.workspace-import.file-manifest.v1"))
	writeManifestFrame(digest, []byte(role))
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("manifest tree contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("manifest tree contains a special file")
		}
		relative, err := filepath.Rel(path, current)
		if err != nil || relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("manifest file escapes its root")
		}
		fileDigest, size, err := hashStableFile(current)
		if err != nil {
			return err
		}
		result.Bytes, err = checkedAdd(result.Bytes, size)
		if err != nil {
			return err
		}
		result.Files++
		result.ContainsData = true
		writeManifestFrame(digest, []byte(filepath.ToSlash(relative)))
		writeManifestFrame(digest, []byte(fmt.Sprintf("%d", size)))
		writeManifestFrame(digest, []byte(fileDigest))
		return nil
	})
	if err != nil {
		return fileManifest{}, err
	}
	result.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return result, nil
}

func writeManifestFrame(target io.Writer, payload []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(payload)))
	_, _ = target.Write(length[:])
	_, _ = target.Write(payload)
}

func applyExternalManifest(source *Source, manifest fileManifest) {
	source.ExternalBytes = manifest.Bytes
	source.ExternalFileCount = manifest.Files
	source.ExternalManifest = manifest.SHA256
}

func checkedAdd(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, errors.New("size overflow")
	}
	return left + right, nil
}

func checkedStagingBytes(sourceBytes int64) (int64, error) {
	doubled, err := checkedAdd(sourceBytes, sourceBytes)
	if err != nil {
		return 0, err
	}
	return checkedAdd(doubled, stagingWorkingSpaceBytes)
}

func planDigest(plan Plan) (string, error) {
	copyPlan := plan
	copyPlan.PlanSHA256 = ""
	copyPlan.TargetAvailableBytes = 0
	copyPlan.Sources = append([]Source(nil), plan.Sources...)
	for index := range copyPlan.Sources {
		copyPlan.Sources[index].DatabasePath = ""
		copyPlan.Sources[index].DataRoot = ""
	}
	encoded, err := json.Marshal(copyPlan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
