package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，注册名 "sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS node (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    partition       TEXT,
    state           TEXT,
    addr            TEXT,
    cpus            INTEGER,
    mem_total_bytes INTEGER,
    devices_json    TEXT,
    labels_json     TEXT,
    agent_version   TEXT,
    last_heartbeat  INTEGER
);`

type sqliteStore struct {
	db *sql.DB
}

// OpenSQLite 打开(或创建)一个 SQLite 库并完成建表。
func OpenSQLite(dsn string) (Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// 单写入者，连接池开 1 可避免 SQLITE_BUSY。
	db.SetMaxOpenConns(1)
	for _, p := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	} {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", p, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) RegisterNode(ctx context.Context, n *Node) (string, error) {
	devices, err := json.Marshal(n.Devices)
	if err != nil {
		return "", err
	}
	labels, err := json.Marshal(n.Labels)
	if err != nil {
		return "", err
	}
	const q = `
INSERT INTO node (id, name, partition, state, addr, cpus, mem_total_bytes, devices_json, labels_json, agent_version, last_heartbeat)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    partition       = excluded.partition,
    state           = excluded.state,
    addr            = excluded.addr,
    cpus            = excluded.cpus,
    mem_total_bytes = excluded.mem_total_bytes,
    devices_json    = excluded.devices_json,
    labels_json     = excluded.labels_json,
    agent_version   = excluded.agent_version,
    last_heartbeat  = excluded.last_heartbeat
RETURNING id;`
	var id string
	err = s.db.QueryRowContext(ctx, q,
		n.ID, n.Name, n.Partition, n.State, n.Addr, n.CPUs, n.MemTotalBytes,
		string(devices), string(labels), n.AgentVersion, n.LastHeartbeat.Unix(),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("register node: %w", err)
	}
	return id, nil
}

func (s *sqliteStore) Heartbeat(ctx context.Context, n *Node, t time.Time) (bool, error) {
	devices, err := json.Marshal(n.Devices)
	if err != nil {
		return false, err
	}
	const q = `
UPDATE node SET state = ?, cpus = ?, mem_total_bytes = ?, devices_json = ?, last_heartbeat = ?
WHERE id = ?;`
	res, err := s.db.ExecContext(ctx, q, StateUp, n.CPUs, n.MemTotalBytes, string(devices), t.Unix(), n.ID)
	if err != nil {
		return false, fmt.Errorf("heartbeat: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *sqliteStore) ListNodes(ctx context.Context) ([]*Node, error) {
	const q = `
SELECT id, name, partition, state, addr, cpus, mem_total_bytes, devices_json, labels_json, agent_version, last_heartbeat
FROM node ORDER BY name;`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()

	var nodes []*Node
	for rows.Next() {
		var (
			n           Node
			devicesJSON string
			labelsJSON  string
			hb          int64
		)
		if err := rows.Scan(&n.ID, &n.Name, &n.Partition, &n.State, &n.Addr,
			&n.CPUs, &n.MemTotalBytes, &devicesJSON, &labelsJSON, &n.AgentVersion, &hb); err != nil {
			return nil, err
		}
		if devicesJSON != "" {
			_ = json.Unmarshal([]byte(devicesJSON), &n.Devices)
		}
		if labelsJSON != "" {
			_ = json.Unmarshal([]byte(labelsJSON), &n.Labels)
		}
		if hb > 0 {
			n.LastHeartbeat = time.Unix(hb, 0)
		}
		nodes = append(nodes, &n)
	}
	return nodes, rows.Err()
}

func (s *sqliteStore) MarkStaleDown(ctx context.Context, olderThan time.Time) (int64, error) {
	const q = `UPDATE node SET state = ? WHERE last_heartbeat < ? AND state != ?;`
	res, err := s.db.ExecContext(ctx, q, StateDown, olderThan.Unix(), StateDown)
	if err != nil {
		return 0, fmt.Errorf("mark stale down: %w", err)
	}
	return res.RowsAffected()
}

func (s *sqliteStore) Close() error { return s.db.Close() }
