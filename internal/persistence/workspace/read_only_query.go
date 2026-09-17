package workspace

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxReadOnlyQueryRows  = 1000
	maxReadOnlyQueryBytes = 100 << 10
)

type ReadOnlyQueryInput struct {
	UserID    string
	ProjectID string
	SQL       string
	Params    []any
	Limit     int
	Scope     string
}

type ReadOnlyQueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"row_count"`
	Truncated bool     `json:"truncated"`
}

var readOnlyQueryForbidden = regexp.MustCompile(`(?i)\b(?:insert|update|delete|replace|create|drop|alter|attach|detach|vacuum|pragma|reindex|analyze|transaction|savepoint|release|load_extension|readfile|writefile)\b`)

var readOnlyQueryViews = map[string]func(userID, projectID, scope string) string{
	"projects": func(userID, projectID, scope string) string {
		filter := "user_id=" + querySQLLiteral(userID)
		if scope == "project" {
			filter += " AND id=" + querySQLLiteral(projectID)
		}
		return "SELECT * FROM main.projects WHERE " + filter
	},
	"frames": func(userID, projectID, scope string) string {
		filter := "p.user_id=" + querySQLLiteral(userID)
		if scope == "project" {
			filter += " AND f.project_id=" + querySQLLiteral(projectID)
		}
		return "SELECT f.* FROM main.frames f JOIN main.projects p ON p.id=f.project_id WHERE " + filter
	},
	"artifacts": func(userID, projectID, scope string) string {
		filter := "p.user_id=" + querySQLLiteral(userID)
		if scope == "project" {
			filter += " AND a.project_id=" + querySQLLiteral(projectID)
		}
		return "SELECT a.* FROM main.artifacts a JOIN main.projects p ON p.id=a.project_id WHERE " + filter
	},
	"artifact_versions": func(userID, projectID, scope string) string {
		filter := "p.user_id=" + querySQLLiteral(userID)
		if scope == "project" {
			filter += " AND a.project_id=" + querySQLLiteral(projectID)
		}
		return "SELECT v.* FROM main.artifact_versions v JOIN main.artifacts a ON a.id=v.artifact_id JOIN main.projects p ON p.id=a.project_id WHERE " + filter
	},
	"artifact_dependencies": func(userID, projectID, scope string) string {
		filter := "p.user_id=" + querySQLLiteral(userID)
		if scope == "project" {
			filter += " AND a.project_id=" + querySQLLiteral(projectID)
		}
		return "SELECT d.* FROM main.artifact_dependencies d JOIN main.artifact_versions v ON v.id=d.output_version_id JOIN main.artifacts a ON a.id=v.artifact_id JOIN main.projects p ON p.id=a.project_id WHERE " + filter
	},
	"execution_log": func(userID, projectID, scope string) string {
		filter := "p.user_id=" + querySQLLiteral(userID)
		if scope == "project" {
			filter += " AND f.project_id=" + querySQLLiteral(projectID)
		}
		return "SELECT e.* FROM main.execution_log e JOIN main.frames f ON f.id=e.frame_id JOIN main.projects p ON p.id=f.project_id WHERE " + filter
	},
	"compute_usage": func(userID, projectID, scope string) string {
		filter := "p.user_id=" + querySQLLiteral(userID)
		if scope == "project" {
			filter += " AND c.project_id=" + querySQLLiteral(projectID)
		}
		return "SELECT c.* FROM main.compute_usage c JOIN main.projects p ON p.id=c.project_id WHERE " + filter
	},
	"notifications": func(userID, projectID, scope string) string {
		filter := "p.user_id=" + querySQLLiteral(userID)
		if scope == "project" {
			filter += " AND f.project_id=" + querySQLLiteral(projectID)
		}
		return "SELECT n.* FROM main.notifications n JOIN main.frames f ON f.id=n.root_frame_id JOIN main.projects p ON p.id=f.project_id WHERE " + filter
	},
}

func (s *Store) ReadOnlyQuery(ctx context.Context, input ReadOnlyQueryInput) (ReadOnlyQueryResult, error) {
	if s == nil || s.db == nil {
		return ReadOnlyQueryResult{}, errors.New("workspace store is closed")
	}
	input.UserID = strings.TrimSpace(input.UserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.SQL = strings.TrimSpace(input.SQL)
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	if input.Scope == "" {
		input.Scope = "project"
	}
	if input.UserID == "" || input.ProjectID == "" || (input.Scope != "project" && input.Scope != "global") {
		return ReadOnlyQueryResult{}, errors.New("query owner, project, and scope are required")
	}
	if input.SQL == "" || len(input.SQL) > 64<<10 || len(input.Params) > 100 {
		return ReadOnlyQueryResult{}, errors.New("query SQL or parameters exceed the bounded contract")
	}
	if err := validateReadOnlySQL(input.SQL); err != nil {
		return ReadOnlyQueryResult{}, err
	}
	if input.Limit <= 0 {
		input.Limit = 200
	}
	if input.Limit > maxReadOnlyQueryRows {
		input.Limit = maxReadOnlyQueryRows
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return ReadOnlyQueryResult{}, err
	}
	defer conn.Close()
	defer resetReadOnlyQueryConnection(conn)
	_, _ = conn.ExecContext(ctx, `PRAGMA query_only=OFF`)
	available, denied, err := installReadOnlyQueryViews(ctx, conn, input.UserID, input.ProjectID, input.Scope)
	if err != nil {
		return ReadOnlyQueryResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA query_only=ON`); err != nil {
		return ReadOnlyQueryResult{}, err
	}
	if err := rejectReadOnlyQueryTables(input.SQL, available, denied); err != nil {
		return ReadOnlyQueryResult{}, err
	}
	wrapped := "SELECT * FROM (" + strings.TrimSuffix(strings.TrimSpace(input.SQL), ";") + ") LIMIT " + fmt.Sprint(input.Limit+1)
	rows, err := conn.QueryContext(ctx, wrapped, input.Params...)
	if err != nil {
		return ReadOnlyQueryResult{}, fmt.Errorf("read-only query: %w", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return ReadOnlyQueryResult{}, err
	}
	result := ReadOnlyQueryResult{Columns: columns, Rows: make([][]any, 0, input.Limit)}
	encodedBytes := 0
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return ReadOnlyQueryResult{}, err
		}
		for index, value := range values {
			values[index] = normalizeReadOnlyQueryValue(value)
		}
		if len(result.Rows) >= input.Limit {
			result.Truncated = true
			break
		}
		raw, _ := json.Marshal(values)
		if encodedBytes+len(raw) > maxReadOnlyQueryBytes {
			result.Truncated = true
			break
		}
		encodedBytes += len(raw)
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return ReadOnlyQueryResult{}, err
	}
	result.RowCount = len(result.Rows)
	return result, nil
}

func (s *Store) ReadOnlyQuerySchema(ctx context.Context, userID, projectID, scope string) (map[string][]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer resetReadOnlyQueryConnection(conn)
	_, _ = conn.ExecContext(ctx, `PRAGMA query_only=OFF`)
	available, _, err := installReadOnlyQueryViews(ctx, conn, strings.TrimSpace(userID), strings.TrimSpace(projectID), strings.ToLower(strings.TrimSpace(scope)))
	if err != nil {
		return nil, err
	}
	result := map[string][]string{}
	for table := range available {
		rows, err := conn.QueryContext(ctx, `PRAGMA temp.table_info("`+table+`")`)
		if err != nil {
			return nil, err
		}
		columns := []string{}
		for rows.Next() {
			var cid int
			var name, kind string
			var notNull, primaryKey int
			var defaultValue any
			if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return nil, err
			}
			columns = append(columns, name)
		}
		rows.Close()
		result[table] = columns
	}
	return result, nil
}

func validateReadOnlySQL(statement string) error {
	normalized := strings.TrimSpace(statement)
	lower := strings.ToLower(normalized)
	if !strings.HasPrefix(lower, "select ") && !strings.HasPrefix(lower, "select\n") && !strings.HasPrefix(lower, "with ") && !strings.HasPrefix(lower, "with\n") {
		return errors.New("host.query accepts only SELECT or WITH queries")
	}
	trimmed := strings.TrimSuffix(normalized, ";")
	if strings.Contains(trimmed, ";") || strings.Contains(normalized, "--") || strings.Contains(normalized, "/*") || readOnlyQueryForbidden.MatchString(normalized) {
		return errors.New("host.query contains a forbidden statement or comment")
	}
	if strings.Contains(lower, "main.") || strings.Contains(lower, "temp.") || strings.Contains(lower, "sqlite_") {
		return errors.New("host.query schema-qualified and SQLite-internal objects are unavailable")
	}
	return nil
}

func installReadOnlyQueryViews(ctx context.Context, conn *sql.Conn, userID, projectID, scope string) (map[string]bool, []string, error) {
	if scope == "" {
		scope = "project"
	}
	available := map[string]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT name FROM main.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, nil, err
	}
	all := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, nil, err
		}
		all = append(all, name)
	}
	rows.Close()
	for name, build := range readOnlyQueryViews {
		if !stringInList(all, name) {
			continue
		}
		_, _ = conn.ExecContext(ctx, `DROP VIEW IF EXISTS temp."`+name+`"`)
		if _, err := conn.ExecContext(ctx, `CREATE TEMP VIEW "`+name+`" AS `+build(userID, projectID, scope)); err != nil {
			continue
		}
		available[name] = true
	}
	denied := make([]string, 0, len(all))
	for _, name := range all {
		if !available[name] {
			denied = append(denied, name)
		}
	}
	return available, denied, nil
}

func resetReadOnlyQueryConnection(conn *sql.Conn) {
	if conn == nil {
		return
	}
	ctx := context.Background()
	_, _ = conn.ExecContext(ctx, `PRAGMA query_only=OFF`)
	for name := range readOnlyQueryViews {
		_, _ = conn.ExecContext(ctx, `DROP VIEW IF EXISTS temp."`+name+`"`)
	}
}

func rejectReadOnlyQueryTables(statement string, available map[string]bool, denied []string) error {
	for _, name := range denied {
		pattern := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `(?:$|[^A-Za-z0-9_])`)
		if pattern.MatchString(statement) {
			return fmt.Errorf("host.query table %q is not available", name)
		}
	}
	if len(available) == 0 {
		return errors.New("host.query has no available tables")
	}
	return nil
}

func normalizeReadOnlyQueryValue(value any) any {
	if data, ok := value.([]byte); ok {
		if utf8.Valid(data) {
			return string(data)
		}
		return "base64:" + base64.StdEncoding.EncodeToString(data)
	}
	return value
}

func querySQLLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func stringInList(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
