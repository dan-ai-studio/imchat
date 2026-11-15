package api

// ── 阅读导览 · 旁路章（治理：平台管理员）─────────────────
// 主线见 docs/READING.md。这一支是"平台级"权力，与会话内角色互不推导。

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

// handleAdminStats is the operator's dashboard: row counts, outbox depth and
// live connections, all in one answer.
func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	counts, err := s.DB.Counts(r.Context())
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	// 投递队列的深度是运维最该先看的一个数：它涨起来就说明有人在丢消息。
	outbox, _ := s.DB.OutboxStats(r.Context())
	// 三项合一：数据量、投递积压、在线连接。
	writeJSON(w, 200, map[string]any{
		"counts":   counts,
		"outbox":   outbox,
		"ws_conns": s.Hub.Connections(),
	})
}

// handleAdminUsers lists accounts with the two things only this surface knows:
// how many conversations each is in, and whether they are online right now.
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.DB.ListUsers(r.Context(), 200)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	// 平台管理员能看到的字段比普通成员列表多：状态、会话数、是否在线。
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		// online 来自连接中心，不来自数据库：它是瞬时事实，不进历史。
		out = append(out, map[string]any{
			"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
			"is_admin": u.IsAdmin, "status": u.Status, "created_at": u.CreatedAt,
			"conversations": u.Convs, "online": s.Hub.Online(u.ID),
		})
	}
	writeJSON(w, 200, map[string]any{"users": out})
}

// statusReq is the body of "change this account's status": active or banned.
type statusReq struct {
	Status string `json:"status"`
}

// handleAdminUserStatus flips an account between active and banned. Banning also
// deletes the sessions and drops the connections, which is why the next request
// from that person is already unauthorized.
func (s *Server) handleAdminUserStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad_id", "user id must be a positive integer")
		return
	}
	// 请求体只有 14 位上限：这里只该出现一个状态词。
	var req statusReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
	if err := s.DB.SetUserStatus(r.Context(), id, req.Status); err != nil {
		writeErr(w, 400, "update_failed", err.Error())
		return
	}
	// 封禁要立刻生效：状态写完之后，把这人所有活着的连接都断掉。
	// 会话行由 SetUserStatus 一并删掉，所以重连也拿不回身份。
	kicked := 0
	if req.Status == "banned" {
		kicked = s.Hub.DisconnectUser(id)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "status": req.Status, "kicked": kicked})
}

// handleAdminKick closes every live connection of one account without touching
// its status. It acts on connections, never on memberships.
func (s *Server) handleAdminKick(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad_id", "user id must be a positive integer")
		return
	}
	// 回一个数字，方便运维确认"确实断了几条"。
	writeJSON(w, 200, map[string]any{"ok": true, "kicked": s.Hub.DisconnectUser(id)})
}

// handleAdminConversations lists conversations with their size and sequence
// watermark — the shape an operator needs to spot a stuck conversation.
func (s *Server) handleAdminConversations(w http.ResponseWriter, r *http.Request) {
	convs, err := s.DB.AdminConversations(r.Context(), 200)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	// next_seq 是这一会话的序号水位：它和 messages 行数对不上就说明有问题。
	out := make([]map[string]any, 0, len(convs))
	for _, c := range convs {
		out = append(out, map[string]any{
			"id": c.ID, "kind": c.Kind, "title": c.Title, "members": c.Members,
			"messages": c.Messages, "next_seq": c.NextSeq, "created_at": c.CreatedAt,
		})
	}
	writeJSON(w, 200, map[string]any{"conversations": out})
}

// handleAdminMessages searches message bodies through the fts5 index. It is the
// one admin surface that reads content rather than counters.
func (s *Server) handleAdminMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if len(q) < 1 {
		writeErr(w, 400, "missing_query", "q is required")
		return
	}
	msgs, err := s.DB.SearchMessagesFTS(r.Context(), q, 100)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	// 带上发送者名字：搜索结果的可用性全靠它，光有 sender_id 没法读。
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

// handleAdminOutbox reports the delivery queue: how much is pending, in flight,
// or waiting for the next attempt.
func (s *Server) handleAdminOutbox(w http.ResponseWriter, r *http.Request) {
	stats, err := s.DB.OutboxStats(r.Context())
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"outbox": stats})
}
