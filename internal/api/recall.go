package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"imchat/internal/store"
)

type recallReq struct {
	Seq int64 `json:"seq"`
}

func (s *Server) handleRecall(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	var req recallReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.Seq <= 0 {
		writeErr(w, 400, "bad_json", "seq is required")
		return
	}
	members, err := s.DB.Members(r.Context(), cid)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	m, err := s.DB.RecallMessage(r.Context(), cid, u.ID, req.Seq, members)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotSender):
		writeErr(w, 403, "not_your_message", "you can only recall your own messages")
		return
	case errors.Is(err, store.ErrAlreadyRecalled):
		writeErr(w, 409, "already_recalled", "this message was already recalled")
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not_found", "message not found")
		return
	default:
		writeErr(w, 500, "recall_failed", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{
		"seq": m.Seq, "recall_of": m.RecallOf, "conversation_id": m.ConversationID,
	})
}
