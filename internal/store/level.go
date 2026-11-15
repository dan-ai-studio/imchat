package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// LevelStep is how much experience one level costs. A flat step keeps the
// curve explainable: level = 1 + exp/LevelStep.
const LevelStep = 100

// Level is one account's growth state.
type Level struct {
	UserID int64
	Level  int
	Exp    int
}

// LevelOf returns the account's level, or a level-1 row if they never earned
// anything. Reading never writes: an untouched account simply has no row.
func (d *DB) LevelOf(ctx context.Context, userID int64) (*Level, error) {
	l := &Level{UserID: userID, Level: 1}
	err := d.rdb.QueryRowContext(ctx,
		"SELECT level, exp FROM user_levels WHERE user_id = ?", userID).Scan(&l.Level, &l.Exp)
	if errors.Is(err, sql.ErrNoRows) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

// AddExp records one experience event and recomputes the level in the same
// transaction. The unique key on (user_id, kind, ref) makes the whole call
// idempotent: replaying the same message id adds nothing.
func (d *DB) AddExp(ctx context.Context, userID int64, kind, ref string, amount int) error {
	if amount <= 0 {
		return nil
	}
	return d.Write(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// 先尝试记事件；冲突说明这一分已经记过，直接返回。
		// 唯一键是幂等的落点：重复上报在这里被吃掉，不会重复加分。
		res, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO exp_events (user_id, kind, ref, exp, created_at) VALUES (?, ?, ?, ?, ?)",
			userID, kind, ref, amount, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		// 影响行数为 0 就是「这一分已经记过」，整条调用直接成功返回。
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		// 事件是新记的，才把总分加上去并重算等级。
		// 总分从事件表重算，而不是累加：事件表才是真相源。
		var total int
		if err := tx.QueryRowContext(ctx,
			"SELECT COALESCE(SUM(exp), 0) FROM exp_events WHERE user_id = ?", userID).Scan(&total); err != nil {
			return err
		}
		// 曲线是一句话：每 100 分一级，没有隐藏系数。
		level := 1 + total/LevelStep
		_, err = tx.ExecContext(ctx,
			"INSERT INTO user_levels (user_id, level, exp, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(user_id) DO UPDATE SET level = excluded.level, exp = excluded.exp, updated_at = excluded.updated_at",
			userID, level, total, time.Now().UnixMilli())
		return err
	})
}

// LevelsOf returns the levels of several users in one query, so a member list
// does not fan out into N round trips.
func (d *DB) LevelsOf(ctx context.Context, userIDs []int64) (map[int64]*Level, error) {
	out := make(map[int64]*Level, len(userIDs))
	for _, id := range userIDs {
		out[id] = &Level{UserID: id, Level: 1}
	}
	if len(userIDs) == 0 {
		return out, nil
	}
	// 用 IN 一次取回；参数个数由调用方决定，成员列表通常只有几十个。
	// 拼一段 IN (?,?,?)：参数个数由调用方决定，成员列表通常只有几十个。
	placeholders := ""
	args := make([]any, 0, len(userIDs))
	for i, id := range userIDs {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}
	// 一次查询取回所有有行的账号；没有行的由上面的默认值兜住。
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT user_id, level, exp FROM user_levels WHERE user_id IN ("+placeholders+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 逐行覆盖默认值：没有行的账号保持 1 级。
	for rows.Next() {
		l := &Level{}
		if err := rows.Scan(&l.UserID, &l.Level, &l.Exp); err != nil {
			return nil, err
		}
		out[l.UserID] = l
	}
	return out, rows.Err()
}
