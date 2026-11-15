package store

import (
	"context"
	"database/sql"
	"time"
)

// AuditEntry is one recorded action or denied attempt.
type AuditEntry struct {
	ID          int64
	CommunityID int64
	ActorID     int64
	Action      string
	Target      string
	Detail      string
	CreatedAt   int64
}

// AppendAudit records one action or denied attempt. Callers may ignore the
// error: an audit write matters, but failing the main operation because the log
// was busy would be worse. Denied attempts are written on purpose — "who tried"
// is as useful as "who succeeded".
func (d *DB) AppendAudit(ctx context.Context, communityID, actorID int64, action, target, detail string) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO audit_log (community_id, actor_id, action, target, detail, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			communityID, actorID, action, target, detail, time.Now().UnixMilli())
		return err
	})
}

// AuditOf lists recent entries for one community, newest first.
func (d *DB) AuditOf(ctx context.Context, communityID int64, limit int) ([]*AuditEntry, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT id, community_id, actor_id, action, target, detail, created_at FROM audit_log WHERE community_id = ? ORDER BY id DESC LIMIT ?",
		communityID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditEntry
	// 逐行扫成结构体；顺序由 SQL 的 ORDER BY 决定，不在这里排。
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.CommunityID, &e.ActorID, &e.Action, &e.Target, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
