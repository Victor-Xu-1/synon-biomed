package workspace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	ComputeJobPageDefault = 100
	ComputeJobPageMax     = 200
)

type ComputeJobPage struct {
	Jobs       []ComputeJob
	NextCursor string
}

type computeJobCursor struct {
	StartedAt string `json:"startedAt"`
	JobID     string `json:"jobId"`
}

// ListActiveComputeJobsPage projects the v1.1 runningJobs collection using a
// stable (started_at DESC, job_id ASC) cursor. Terminal history remains
// available through GetComputeJob but is not mixed into the workbench list.
func (s *Store) ListActiveComputeJobsPage(userID, projectID, cursor string, limit int) (ComputeJobPage, error) {
	if s == nil || s.db == nil {
		return ComputeJobPage{}, errors.New("workspace store is closed")
	}
	userID, projectID = strings.TrimSpace(userID), strings.TrimSpace(projectID)
	if userID == "" || projectID == "" {
		return ComputeJobPage{}, errors.New("compute job owner and project are required")
	}
	if limit <= 0 {
		limit = ComputeJobPageDefault
	}
	if limit > ComputeJobPageMax {
		return ComputeJobPage{}, errors.New("compute job page limit exceeds 200")
	}

	query := `SELECT ` + computeJobColumns + ` FROM compute_workbench_jobs
		WHERE owner_user_id=? AND project_id=?
			AND state IN ('pending','staging','queued','running','harvesting')`
	arguments := []any{userID, projectID}
	if strings.TrimSpace(cursor) != "" {
		startedAt, jobID, err := decodeComputeJobCursor(cursor)
		if err != nil {
			return ComputeJobPage{}, err
		}
		query += ` AND (started_at<? OR (started_at=? AND job_id>?))`
		arguments = append(arguments, startedAt, startedAt, jobID)
	}
	query += ` ORDER BY started_at DESC,job_id ASC LIMIT ?`
	arguments = append(arguments, limit+1)
	rows, err := s.db.QueryContext(context.Background(), query, arguments...)
	if err != nil {
		return ComputeJobPage{}, err
	}
	defer rows.Close()
	jobs := make([]ComputeJob, 0, limit+1)
	for rows.Next() {
		job, err := scanComputeJob(rows)
		if err != nil {
			return ComputeJobPage{}, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return ComputeJobPage{}, err
	}
	page := ComputeJobPage{Jobs: jobs}
	if len(jobs) > limit {
		page.Jobs = jobs[:limit]
		last := page.Jobs[len(page.Jobs)-1]
		page.NextCursor = encodeComputeJobCursor(last.StartedAt, last.JobID)
	}
	return page, nil
}

func encodeComputeJobCursor(startedAt time.Time, jobID string) string {
	raw, _ := json.Marshal(computeJobCursor{StartedAt: startedAt.UTC().Format(time.RFC3339Nano), JobID: jobID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeComputeJobCursor(value string) (time.Time, string, error) {
	if len(value) > 512 {
		return time.Time{}, "", errors.New("invalid compute job cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, "", errors.New("invalid compute job cursor")
	}
	var cursor computeJobCursor
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || strings.TrimSpace(cursor.JobID) == "" {
		return time.Time{}, "", errors.New("invalid compute job cursor")
	}
	startedAt, err := time.Parse(time.RFC3339Nano, cursor.StartedAt)
	if err != nil {
		return time.Time{}, "", errors.New("invalid compute job cursor")
	}
	return startedAt.UTC(), strings.TrimSpace(cursor.JobID), nil
}
