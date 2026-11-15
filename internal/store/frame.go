package store

// ── 阅读导览 · 第 9 站 ────────────────────────────────────
// 上一站：站8 最后一跳与确认
// 下一站：站10 撤回

import "encoding/json"

// FrameJSON renders the wire frame for one due delivery.
func FrameJSON(e *DueEntry) []byte {
	// 字段顺序写死在这里，等于把线上格式钉在代码上：
	// 客户端认的是这些键名，不是 Go 的结构体。
	payload := map[string]any{
		"type":            "msg",
		"conversation_id": e.ConversationID,
		"seq":             e.Seq,
		"id":              e.MessageID,
		"sender_id":       e.SenderID,
		"kind":            e.Kind,
		"body":            e.Body,
		"meta":            e.Meta, // 提醒等结构化信息放这里，不为它新增字段
		// recall_of 不在这组默认字段里：它是可选的，见下面那个 if。
		"created_at": e.CreatedAt,
	}
	// 只有墓碑才带 recall_of：普通消息不上线这个字段，省得客户端到处判空。
	if e.RecallOf > 0 {
		payload["recall_of"] = e.RecallOf
	}
	// 编码失败返回空对象而不是编造一个兜底字符串：线上格式永远只有 JSON。
	b, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return b
}
