package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const jobSchema = `
CREATE TABLE IF NOT EXISTS job (
    id            TEXT PRIMARY KEY,
    name          TEXT,
    owner         TEXT,
    partition     TEXT,
    state         TEXT,
    priority      INTEGER,
    command       TEXT,
    env_json      TEXT,
    workdir       TEXT,
    req_cpus      INTEGER,
    req_mem_bytes INTEGER,
    req_gpus      INTEGER,
    gpu_type      TEXT,
    walltime_sec  INTEGER,
    node_id       TEXT,
    node_name     TEXT,
    devices_json  TEXT,
    exit_code     INTEGER,
    reason        TEXT,
    submit_at     INTEGER,
    start_at      INTEGER,
    end_at        INTEGER
);
CREATE INDEX IF NOT EXISTS idx_job_state ON job(state);
CREATE INDEX IF NOT EXISTS idx_job_node ON job(node_id, state);`

const jobColumns = `id, name, owner, partition, state, priority, command, env_json, workdir,
req_cpus, req_mem_bytes, req_gpus, gpu_type, walltime_sec, node_id, node_name, devices_json,
exit_code, reason, submit_at, start_at, end_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanJob(sc rowScanner) (*Job, error) {
	var (
		j                        Job
		envJSON, devicesJSON     string
		submitAt, startAt, endAt int64
	)
	if err := sc.Scan(&j.ID, &j.Name, &j.Owner, &j.Partition, &j.State, &j.Priority,
		&j.Command, &envJSON, &j.Workdir, &j.ReqCPUs, &j.ReqMemBytes, &j.ReqGPUs, &j.GPUType,
		&j.WalltimeSec, &j.NodeID, &j.NodeName, &devicesJSON, &j.ExitCode, &j.Reason,
		&submitAt, &startAt, &endAt); err != nil {
		return nil, err
	}
	if envJSON != "" {
		_ = json.Unmarshal([]byte(envJSON), &j.Env)
	}
	if devicesJSON != "" {
		_ = json.Unmarshal([]byte(devicesJSON), &j.Devices)
	}
	j.SubmitAt = unixToTime(submitAt)
	j.StartAt = unixToTime(startAt)
	j.EndAt = unixToTime(endAt)
	return &j, nil
}

func unixToTime(u int64) time.Time {
	if u <= 0 {
		return time.Time{}
	}
	return time.Unix(u, 0)
}

func unixOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *sqliteStore) CreateJob(ctx context.Context, j *Job) error {
	env, err := json.Marshal(j.Env)
	if err != nil {
		return err
	}
	devices, err := json.Marshal(j.Devices)
	if err != nil {
		return err
	}
	const q = `INSERT INTO job (` + jobColumns + `)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?);`
	_, err = s.db.ExecContext(ctx, q,
		j.ID, j.Name, j.Owner, j.Partition, j.State, j.Priority, j.Command, string(env), j.Workdir,
		j.ReqCPUs, j.ReqMemBytes, j.ReqGPUs, j.GPUType, j.WalltimeSec, j.NodeID, j.NodeName, string(devices),
		j.ExitCode, j.Reason, unixOf(j.SubmitAt), unixOf(j.StartAt), unixOf(j.EndAt))
	if err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetJob(ctx context.Context, id string) (*Job, error) {
	const q = `SELECT ` + jobColumns + ` FROM job WHERE id = ?;`
	j, err := scanJob(s.db.QueryRowContext(ctx, q, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get job: %w", err)
	}
	return j, nil
}

func (s *sqliteStore) queryJobs(ctx context.Context, where string, args ...any) ([]*Job, error) {
	q := `SELECT ` + jobColumns + ` FROM job ` + where
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()
	var jobs []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *sqliteStore) ListJobs(ctx context.Context, state, owner string) ([]*Job, error) {
	where := "WHERE 1=1"
	var args []any
	if state != "" {
		where += " AND state = ?"
		args = append(args, state)
	}
	if owner != "" {
		where += " AND owner = ?"
		args = append(args, owner)
	}
	where += " ORDER BY submit_at DESC, id"
	return s.queryJobs(ctx, where, args...)
}

func (s *sqliteStore) ListPendingJobs(ctx context.Context) ([]*Job, error) {
	return s.queryJobs(ctx, "WHERE state = ? ORDER BY priority DESC, submit_at ASC, id", JobPending)
}

func (s *sqliteStore) ListActiveJobs(ctx context.Context) ([]*Job, error) {
	return s.queryJobs(ctx, "WHERE state IN (?, ?)", JobAssigned, JobRunning)
}

func (s *sqliteStore) ListJobsByNodeState(ctx context.Context, nodeID, state string) ([]*Job, error) {
	return s.queryJobs(ctx, "WHERE node_id = ? AND state = ? ORDER BY submit_at ASC, id", nodeID, state)
}

func (s *sqliteStore) AssignJob(ctx context.Context, jobID, nodeID, nodeName string, devices []AllocDevice) error {
	d, err := json.Marshal(devices)
	if err != nil {
		return err
	}
	const q = `UPDATE job SET state = ?, node_id = ?, node_name = ?, devices_json = ?
WHERE id = ? AND state = ?;`
	res, err := s.db.ExecContext(ctx, q, JobAssigned, nodeID, nodeName, string(d), jobID, JobPending)
	if err != nil {
		return fmt.Errorf("assign job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("assign job %s: not in PENDING state", jobID)
	}
	return nil
}

func (s *sqliteStore) MarkJobRunning(ctx context.Context, jobID string, t time.Time) error {
	const q = `UPDATE job SET state = ?, start_at = ? WHERE id = ? AND state = ?;`
	_, err := s.db.ExecContext(ctx, q, JobRunning, unixOf(t), jobID, JobAssigned)
	if err != nil {
		return fmt.Errorf("mark running: %w", err)
	}
	return nil
}

func (s *sqliteStore) FinishJob(ctx context.Context, jobID, state string, exitCode int32, reason string, t time.Time) error {
	const q = `UPDATE job SET state = ?, exit_code = ?, reason = ?, end_at = ? WHERE id = ?;`
	_, err := s.db.ExecContext(ctx, q, state, exitCode, reason, unixOf(t), jobID)
	if err != nil {
		return fmt.Errorf("finish job: %w", err)
	}
	return nil
}

func (s *sqliteStore) CancelJob(ctx context.Context, jobID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM job WHERE id = ?;`, jobID).Scan(&state); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("job %s not found", jobID)
		}
		return "", err
	}

	newState := state
	switch state {
	case JobPending:
		newState = JobCancelled
		if _, err := tx.ExecContext(ctx, `UPDATE job SET state = ?, end_at = ? WHERE id = ?;`,
			JobCancelled, time.Now().Unix(), jobID); err != nil {
			return "", err
		}
	case JobAssigned, JobRunning:
		newState = JobCancelling
		if _, err := tx.ExecContext(ctx, `UPDATE job SET state = ? WHERE id = ?;`, JobCancelling, jobID); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return newState, nil
}
