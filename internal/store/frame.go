package store

import "encoding/json"

// FrameJSON renders the wire frame for one due delivery.
func FrameJSON(e *DueEntry) []byte {
	b, err := json.Marshal(map[string]any{
		"type":            "msg",
		"conversation_id": e.ConversationID,
		"seq":             e.Seq,
		"id":              e.MessageID,
		"sender_id":       e.SenderID,
		"kind":            e.Kind,
		"body":            e.Body,
		"created_at":      e.CreatedAt,
	})
	if err != nil {
		return []byte("{}")
	}
	return b
}
