package store

import "context"

type ReadCursor struct {
	UserID int64
	Seq    int64
}

// ReadCursors returns one row per participant, taking the maximum across that
// participant's devices: a person has one attention, not one per device.
func (d *DB) ReadCursors(ctx context.Context, conversationID int64) ([]*ReadCursor, error) {
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT user_id, MAX(seq) FROM read_cursors WHERE conversation_id = ? GROUP BY user_id", conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
