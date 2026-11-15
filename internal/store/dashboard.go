package store

import (
	"context"
)

// Dashboard is the platform-wide snapshot the operations screen reads. Every
// number is a COUNT or a GROUP BY; nothing here walks rows in application code.
type Dashboard struct {
	Online       int64
	Rooms        int64
	RoomOnline   int64
	Messages     int64
	MessagesDay  int64
	Communities  int64
	Users        int64
	ReportsOpen  int64
	AppealsOpen  int64
	LevelBuckets map[int]int64
}

// DashboardStats collects the platform snapshot. A failure in any single query
// fails the whole call: a dashboard with a silently missing number is worse than
// an error.
func (d *DB) DashboardStats(ctx context.Context) (*Dashboard, error) {
	out := &Dashboard{LevelBuckets: map[int]int64{}}
	// 八条单行聚合：每条对应看板上的一个数字。
	queries := []struct {
		sql string
		dst *int64
	}{
		{"SELECT COUNT(*) FROM room_presence", &out.RoomOnline},
		{"SELECT COUNT(*) FROM conversations WHERE is_room = 1 AND closed_at = 0", &out.Rooms},
		{"SELECT COUNT(*) FROM messages", &out.Messages},
		{"SELECT COUNT(*) FROM messages WHERE created_at > (strftime('%s','now')-86400)*1000", &out.MessagesDay},
		// 上面四个是房间与消息，下面四个是社群、用户与待办队列。
		{"SELECT COUNT(*) FROM communities", &out.Communities},
		{"SELECT COUNT(*) FROM users", &out.Users},
		{"SELECT COUNT(*) FROM reports WHERE status = 'pending'", &out.ReportsOpen},
		{"SELECT COUNT(*) FROM appeals WHERE status = 'pending'", &out.AppealsOpen},
	}
	// 逐条跑：每条都是独立聚合，顺序不影响结果。
	// 每条都是单行聚合，出错就整轮失败，不返回半张看板。
	for _, q := range queries {
		if err := d.rdb.QueryRowContext(ctx, q.sql).Scan(q.dst); err != nil {
			return nil, err
		}
	}
	// 等级分布：按 level 分组，前端画柱状图。
	rows, err := d.rdb.QueryContext(ctx, "SELECT level, COUNT(*) FROM user_levels GROUP BY level ORDER BY level")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 每个等级一个桶；没有账号的等级不出现。
	for rows.Next() {
		var level int
		var n int64
		if err := rows.Scan(&level, &n); err != nil {
			return nil, err
		}
		out.LevelBuckets[level] = n
	}
	return out, rows.Err()
}
