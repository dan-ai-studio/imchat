package ws

import "encoding/json"

type Frame struct {
	Type           string `json:"type"`
	Device         string `json:"device,omitempty"`
	ClientMsgID    string `json:"client_msg_id,omitempty"`
	ConversationID int64  `json:"conversation_id,omitempty"`
	Seq            int64  `json:"seq,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Body           string `json:"body,omitempty"`
	ID             string `json:"id,omitempty"`
	SenderID       int64  `json:"sender_id,omitempty"`
	CreatedAt      int64  `json:"created_at,omitempty"`
	UserID         int64  `json:"user_id,omitempty"`
	Count          int    `json:"count,omitempty"`
	Code           string `json:"code,omitempty"`
	Message        string `json:"message,omitempty"`
}

// Encode marshals a frame. A marshal failure yields an empty object rather than
// a hand-written fallback string, which keeps the wire contract JSON-only.
func Encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
