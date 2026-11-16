package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// ErrReportDecided is returned when a report has already been handled.
var ErrReportDecided = errors.New("store: report already decided")

// Report is one complaint, joined with the reporter's name so the queue needs
// no second query.
type Report struct {
	ID          int64
	CommunityID int64
	ReporterID  int64
	Reporter    string
	TargetKind  string
	TargetID    string
	Reason      string
	Status      string
	CreatedAt   int64
}

// CreateReport records one complaint. Duplicate reports are allowed: five
// people complaining about the same message is information, not noise.
func (d *DB) CreateReport(ctx context.Context, communityID, reporterID int64, targetKind, targetID, reason string) (int64, error) {
	var id int64
	err := d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO reports (community_id, reporter_id, target_kind, target_id, reason, status, created_at) VALUES (?, ?, ?, ?, ?, 'pending', ?)",
			communityID, reporterID, targetKind, targetID, reason, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		// id 回填给调用方，前端用它跳到自己刚提交的那条。
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// ReportsOf lists the pending reports of one community, oldest first.
func (d *DB) ReportsOf(ctx context.Context, communityID int64) ([]*Report, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT r.id, r.community_id, r.reporter_id, u.display_name, r.target_kind, r.target_id, r.reason, r.status, r.created_at FROM reports r JOIN users u ON u.id = r.reporter_id WHERE r.community_id = ? AND r.status = 'pending' ORDER BY r.id",
		communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Report
	// 逐行扫成结构体；顺序由 SQL 的 ORDER BY 决定。
	for rows.Next() {
		r := &Report{}
		if err := rows.Scan(&r.ID, &r.CommunityID, &r.ReporterID, &r.Reporter, &r.TargetKind, &r.TargetID, &r.Reason, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DecideReport closes one report and records what was done. Action is free
// text on purpose: delete_message, mute, remove_member, ban and dismiss all
// share this one row shape.
func (d *DB) DecideReport(ctx context.Context, reportID, actorID int64, status, action, note string) error {
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var communityID int64
		var current string
		if err := tx.QueryRowContext(ctx,
			"SELECT community_id, status FROM reports WHERE id = ?", reportID).Scan(&communityID, &current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// 已决的举报不能再决一次：处置要可追溯，不能反复改口。
		if current != "pending" {
			return ErrReportDecided
		}
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx,
			"UPDATE reports SET status = ?, decided_at = ?, decided_by = ? WHERE id = ?",
			status, now, actorID, reportID); err != nil {
			return err
		}
		// 处置动作单独一行：被处置者据此看到「为什么」。
		_, err := tx.ExecContext(ctx,
			"INSERT INTO moderation_actions (report_id, community_id, actor_id, action, note, created_at) VALUES (?, ?, ?, ?, ?, ?)",
			reportID, communityID, actorID, action, note, now)
		return err
	})
}

// ReportsAgainstMe lists the decided reports whose target is this user. It is
// how a moderated person finds out what happened and where to appeal: the
// community queue is for admins, this one is for the person on the other side.
func (d *DB) ReportsAgainstMe(ctx context.Context, userID int64) ([]*Report, error) {
	// target_id 存的是字符串：用户目标的 id 就是 user_id 的十进制写法。
	// 两种目标都算「针对我」：直接举报我，或者举报了我发的一条消息。
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT r.id, r.community_id, r.reporter_id, u.display_name, r.target_kind, r.target_id, r.reason, r.status, r.created_at FROM reports r JOIN users u ON u.id = r.reporter_id WHERE r.status IN ('actioned','escalated','dismissed') AND ((r.target_kind = 'user' AND r.target_id = ?) OR (r.target_kind = 'message' AND r.target_id IN (SELECT id FROM messages WHERE sender_id = ?))) ORDER BY r.id DESC",
		strconv.FormatInt(userID, 10), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Report
	// 逐行扫成结构体；顺序由 SQL 的 ORDER BY 决定。
	for rows.Next() {
		r := &Report{}
		if err := rows.Scan(&r.ID, &r.CommunityID, &r.ReporterID, &r.Reporter, &r.TargetKind, &r.TargetID, &r.Reason, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AppealsOfUser lists the appeals this user filed, newest first.
func (d *DB) AppealsOfUser(ctx context.Context, userID int64) ([]*Appeal, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT a.id, a.report_id, a.user_id, u.display_name, a.reason, a.status, a.created_at FROM appeals a JOIN users u ON u.id = a.user_id WHERE a.user_id = ? ORDER BY a.id DESC",
		userID)
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

// ActionsOf returns the moderation actions taken on one report.
func (d *DB) ActionsOf(ctx context.Context, reportID int64) ([]map[string]any, error) {
	// 按 id 升序：第一条就是最初的处置，后面的都是补充。
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT id, actor_id, action, note, created_at FROM moderation_actions WHERE report_id = ? ORDER BY id", reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	// 处置动作字段少，直接扫成 map 交给前端。
	for rows.Next() {
		var (
			id      int64
			actor   int64
			action  string
			note    string
			created int64
		)
		// 五个字段都很短，逐个扫再拼 map，比定义结构体更直接。
		if err := rows.Scan(&id, &actor, &action, &note, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "actor_id": actor, "action": action, "note": note, "created_at": created})
	}
	return out, rows.Err()
}
