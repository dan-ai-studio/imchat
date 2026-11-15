package store

import (
	"context"
	"strings"
)

// SearchMessagesFTS runs a phrase query against the fts5 index. The query is
// quoted so user input cannot inject fts5 operators.
func (d *DB) SearchMessagesFTS(ctx context.Context, q string, limit int) ([]*AdminMessage, error) {
	phrase := "\"" + strings.ReplaceAll(q, "\"", "") + "\""
	// 把用户输入整个包进双引号：fts5 的操作符（AND / OR / NEAR / 星号）因此失效，
	// 注入无从谈起。代价是只能做短语检索，不支持布尔组合。
	rows, err := d.rdb.QueryContext(ctx,
		"SELECT m.conversation_id, m.seq, m.sender_id, u.display_name, m.body, m.created_at FROM messages_fts JOIN messages m ON m.conversation_id = messages_fts.conv AND m.seq = messages_fts.seq JOIN users u ON u.id = m.sender_id WHERE messages_fts MATCH ? ORDER BY rank LIMIT ?",
		phrase, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 回表拿发送者名字：fts 索引里只有正文与定位键，没有人名。
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
