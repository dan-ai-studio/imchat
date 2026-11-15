package store

import "encoding/json"

// FrameJSON renders the wire frame for one due delivery.
func FrameJSON(e *DueEntry) []byte {
	payload := map[string]any{
		"type":            "msg",
		"conversation_id": e.ConversationID,
		"seq":             e.Seq,
		"id":              e.MessageID,
		"sender_id":       e.SenderID,
		"kind":            e.Kind,
		"body":            e.Body,
		"meta":            e.Meta,
		"created_at":      e.CreatedAt,
	}
	if e.RecallOf > 0 {
		payload["recall_of"] = e.RecallOf
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return b
}
