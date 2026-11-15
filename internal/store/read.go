package store

// ── 阅读导览 · 第 9 站 ────────────────────────────────────
// 上一站：站8 最后一跳与确认
// 下一站：站10 撤回

import "context"

// ReadCursor is one participant's read position in one conversation, already
// collapsed across that participant's devices.
type ReadCursor struct {
	UserID int64
	Seq    int64
}

// ReadCursors returns one row per participant, taking the maximum across that
// participant's devices: a person has one attention, not one per device.
func (d *DB) ReadCursors(ctx context.Context, conversationID int64) ([]*ReadCursor, error) {
	// 按用户分组取最大值：一个人只有一份注意力，不是每台设备一份。
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT user_id, MAX(seq) FROM read_cursors WHERE conversation_id = ? GROUP BY user_id", conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 逐行扫成结构体。这里不需要判空：聚合查询对每个用户必有一行。
	var out []*ReadCursor
	for rows.Next() {
		c := &ReadCursor{}
		if err := rows.Scan(&c.UserID, &c.Seq); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
