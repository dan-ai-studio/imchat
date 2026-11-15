package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrAppealDecided is returned when an appeal has already been ruled on.
var ErrAppealDecided = errors.New("store: appeal already decided")

// Appeal is one complaint about a moderation decision, joined with the
// appellant's name for the platform queue.
type Appeal struct {
	ID        int64
	ReportID  int64
	UserID    int64
	User      string
	Reason    string
	Status    string
	CreatedAt int64
}

// AddBaseline registers one platform-level pattern. Matching reports are
// escalated automatically and cannot be dismissed by a community admin.
func (d *DB) AddBaseline(ctx context.Context, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return errors.New("store: empty baseline pattern")
	}
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO platform_baselines (pattern, created_at) VALUES (?, ?)",
			pattern, time.Now().UnixMilli())
		return err
	})
}

// Baselines lists every platform-level pattern.
func (d *DB) Baselines(ctx context.Context) ([]string, error) {
	rows, err := d.rdb.QueryContext(ctx, "SELECT pattern FROM platform_baselines ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	// 基线通常只有几条，直接收成字符串切片。
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// EscalateReport moves one report to the platform queue. It is used both when a
// baseline matches and when a community admin cannot decide.
func (d *DB) EscalateReport(ctx context.Context, reportID, actorID int64, note string) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var communityID int64
		if err := tx.QueryRowContext(ctx,
			"SELECT community_id FROM reports WHERE id = ?", reportID).Scan(&communityID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// 只有 pending 能被上交：已决的举报不该再动。
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx,
			"UPDATE reports SET status = 'escalated', decided_at = ?, decided_by = ? WHERE id = ? AND status = 'pending'",
			now, actorID, reportID); err != nil {
			return err
		}
		// 上交本身也留一行：平台要能看到是谁、为什么交上来的。
		_, err := tx.ExecContext(ctx,
			"INSERT INTO moderation_actions (report_id, community_id, actor_id, action, note, created_at) VALUES (?, ?, ?, 'escalate', ?, ?)",
			reportID, communityID, actorID, note, now)
		return err
	})
}

// CreateAppeal records one appeal against a moderation decision.
func (d *DB) CreateAppeal(ctx context.Context, reportID, userID int64, reason string) (int64, error) {
	var id int64
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO appeals (report_id, user_id, reason, status, created_at) VALUES (?, ?, ?, 'pending', ?)",
			reportID, userID, reason, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		// id 回填给调用方，前端用它跟踪这条申诉。
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// AppealsOf lists pending appeals for the platform queue, oldest first.
func (d *DB) AppealsOf(ctx context.Context) ([]*Appeal, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT a.id, a.report_id, a.user_id, u.display_name, a.reason, a.status, a.created_at FROM appeals a JOIN users u ON u.id = a.user_id WHERE a.status = 'pending' ORDER BY a.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Appeal
	// 逐行扫成结构体；顺序由 SQL 的 ORDER BY 决定。
	for rows.Next() {
		a := &Appeal{}
		if err := rows.Scan(&a.ID, &a.ReportID, &a.UserID, &a.User, &a.Reason, &a.Status, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DecideAppeal rules on one appeal. Overturning also reopens the report as
// dismissed, so the visible outcome follows the ruling.
func (d *DB) DecideAppeal(ctx context.Context, appealID, actorID int64, overturn bool, note string) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var reportID int64
		var status string
		if err := tx.QueryRowContext(ctx,
			"SELECT report_id, status FROM appeals WHERE id = ?", appealID).Scan(&reportID, &status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// 已决的申诉不能再决一次：改判要留痕，不能反复改口。
		if status != "pending" {
			return ErrAppealDecided
		}
		// 默认维持原判；只有明确要求改判才翻转。
		next := "upheld"
		if overturn {
			next = "overturned"
		}
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx,
			"UPDATE appeals SET status = ?, decided_at = ?, decided_by = ? WHERE id = ?",
			next, now, actorID, appealID); err != nil {
			return err
		}
		// 改判要把举报本身也改成 dismissed：可见结果要跟着裁决走。
		if overturn {
			if _, err := tx.ExecContext(ctx,
				"UPDATE reports SET status = 'dismissed' WHERE id = ?", reportID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO moderation_actions (report_id, community_id, actor_id, action, note, created_at) VALUES (?, 0, ?, ?, ?, ?)",
			reportID, actorID, "appeal_"+next, note, now)
		return err
	})
}
