package store

import (
	"context"
	"strings"
)

// SearchMessagesFTS runs a phrase query against the fts5 index. The query is
// quoted so user input cannot inject fts5 operators.
func (d *DB) SearchMessagesFTS(ctx context.Context, q string, limit int) ([]*AdminMessage, error) {
	phrase := "\"" + strings.ReplaceAll(q, "\"", "") + "\""
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT m.conversation_id, m.seq, m.sender_id, u.display_name, m.body, m.created_at FROM messages_fts JOIN messages m ON m.conversation_id = messages_fts.conv AND m.seq = messages_fts.seq JOIN users u ON u.id = m.sender_id WHERE messages_fts MATCH ? ORDER BY rank LIMIT ?",
		phrase, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AdminMessage
	for rows.Next() {
		m := &AdminMessage{}
		if err := rows.Scan(&m.ConversationID, &m.Seq, &m.SenderID, &m.Sender, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
