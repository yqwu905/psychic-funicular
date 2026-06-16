package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yqwu905/psychic-funicular/internal/event"
)

const eventSchema = `
CREATE TABLE IF NOT EXISTS event (
    id         TEXT PRIMARY KEY,
    type       TEXT,
    severity   TEXT,
    source     TEXT,
    summary    TEXT,
    dedup_key  TEXT,
    labels_json TEXT,
    ts         INTEGER
);
CREATE INDEX IF NOT EXISTS idx_event_ts ON event(ts);

CREATE TABLE IF NOT EXISTS notification (
    id         TEXT PRIMARY KEY,
    event_id   TEXT,
    event_type TEXT,
    rule       TEXT,
    channel    TEXT,
    recipients TEXT,
    status     TEXT,
    error      TEXT,
    summary    TEXT,
    ts         INTEGER
);
CREATE INDEX IF NOT EXISTS idx_notification_ts ON notification(ts);`

func (s *sqliteStore) CreateEvent(ctx context.Context, e *event.Event) error {
	labels, err := json.Marshal(e.Labels)
	if err != nil {
		return err
	}
	const q = `INSERT INTO event (id, type, severity, source, summary, dedup_key, labels_json, ts)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);`
	if _, err := s.db.ExecContext(ctx, q, e.ID, e.Type, string(e.Severity), e.Source,
		e.Summary, e.DedupKey, string(labels), e.Time.Unix()); err != nil {
		return fmt.Errorf("create event: %w", err)
	}
	return nil
}

func (s *sqliteStore) ListEvents(ctx context.Context, limit int) ([]*event.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `SELECT id, type, severity, source, summary, dedup_key, labels_json, ts
FROM event ORDER BY ts DESC, id LIMIT ?;`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var out []*event.Event
	for rows.Next() {
		var (
			e          event.Event
			sev        string
			labelsJSON string
			ts         int64
		)
		if err := rows.Scan(&e.ID, &e.Type, &sev, &e.Source, &e.Summary, &e.DedupKey, &labelsJSON, &ts); err != nil {
			return nil, err
		}
		e.Severity = event.Severity(sev)
		e.Time = unixToTime(ts)
		if labelsJSON != "" {
			_ = json.Unmarshal([]byte(labelsJSON), &e.Labels)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (s *sqliteStore) CreateNotification(ctx context.Context, n *event.Notification) error {
	const q = `INSERT INTO notification (id, event_id, event_type, rule, channel, recipients, status, error, summary, ts)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`
	if _, err := s.db.ExecContext(ctx, q, n.ID, n.EventID, n.EventType, n.Rule, n.Channel,
		n.Recipients, n.Status, n.Error, n.Summary, n.Time.Unix()); err != nil {
		return fmt.Errorf("create notification: %w", err)
	}
	return nil
}

func (s *sqliteStore) ListNotifications(ctx context.Context, limit int) ([]*event.Notification, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `SELECT id, event_id, event_type, rule, channel, recipients, status, error, summary, ts
FROM notification ORDER BY ts DESC, id LIMIT ?;`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()
	var out []*event.Notification
	for rows.Next() {
		var (
			n  event.Notification
			ts int64
		)
		if err := rows.Scan(&n.ID, &n.EventID, &n.EventType, &n.Rule, &n.Channel,
			&n.Recipients, &n.Status, &n.Error, &n.Summary, &ts); err != nil {
			return nil, err
		}
		n.Time = unixToTime(ts)
		out = append(out, &n)
	}
	return out, rows.Err()
}
