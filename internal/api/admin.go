package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// requireAdmin gates the platform-admin surface. Platform admins act on
// accounts, connections and storage; they never act inside a conversation.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r.Context())
		if u == nil || !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "forbidden", "platform admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	counts, err := s.DB.Counts(r.Context())
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	outbox, _ := s.DB.OutboxStats(r.Context())
	writeJSON(w, 200, map[string]any{
		"counts":   counts,
		"outbox":   outbox,
		"ws_conns": s.Hub.Connections(),
	})
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.DB.ListUsers(r.Context(), 200)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		out = append(out, map[string]any{
			"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
			"is_admin": u.IsAdmin, "status": u.Status, "created_at": u.CreatedAt,
			"conversations": u.Convs, "online": s.Hub.Online(u.ID),
		})
	}
	writeJSON(w, 200, map[string]any{"users": out})
}

type statusReq struct {
	Status string `json:"status"`
}

func (s *Server) handleAdminUserStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad_id", "user id must be a positive integer")
		return
	}
	var req statusReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
	if err := s.DB.SetUserStatus(r.Context(), id, req.Status); err != nil {
		writeErr(w, 400, "update_failed", err.Error())
		return
	}
	kicked := 0
	if req.Status == "banned" {
		kicked = s.Hub.DisconnectUser(id)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "status": req.Status, "kicked": kicked})
}

func (s *Server) handleAdminKick(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad_id", "user id must be a positive integer")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "kicked": s.Hub.DisconnectUser(id)})
}

func (s *Server) handleAdminConversations(w http.ResponseWriter, r *http.Request) {
	convs, err := s.DB.AdminConversations(r.Context(), 200)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(convs))
	for _, c := range convs {
		out = append(out, map[string]any{
			"id": c.ID, "kind": c.Kind, "title": c.Title, "members": c.Members,
			"messages": c.Messages, "next_seq": c.NextSeq, "created_at": c.CreatedAt,
		})
	}
	writeJSON(w, 200, map[string]any{"conversations": out})
}

func (s *Server) handleAdminMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) < 1 {
		writeErr(w, 400, "missing_query", "q is required")
		return
	}
	msgs, err := s.DB.SearchMessages(r.Context(), q, 100)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{
			"conversation_id": m.ConversationID, "seq": m.Seq,
			"sender_id": m.SenderID, "sender": m.Sender,
			"body": m.Body, "created_at": m.CreatedAt,
		})
	}
	writeJSON(w, 200, map[string]any{"messages": out})
}

func (s *Server) handleAdminOutbox(w http.ResponseWriter, r *http.Request) {
	stats, err := s.DB.OutboxStats(r.Context())
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"outbox": stats})
}
