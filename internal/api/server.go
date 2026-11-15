package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"imchat/internal/auth"
	"imchat/internal/bus"
	"imchat/internal/delivery"
	"imchat/internal/store"
	"imchat/internal/ws"
)

type Server struct {
	DB         *store.DB
	Auth       *auth.Manager
	Hub        *ws.Hub
	Dispatcher *delivery.Dispatcher
	FilesDir   string
	// Bus carries ephemeral state (read cursors, presence) that must not be
	// persisted: losing it is harmless, the next fetch restores it.
	Bus *bus.Bus
}

type ctxKey string

const userKey ctxKey = "imchat.user"

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Get("/healthz", s.handleHealth)
	r.Post("/api/register", s.handleRegister)
	r.Post("/api/login", s.handleLogin)
	r.Post("/api/logout", s.handleLogout)

	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/api/me", s.handleMe)
		r.Post("/api/upload", s.handleUpload)
		r.Get("/files/{name}", s.handleFile)
		r.Get("/api/users", s.handleUsers)
		r.Get("/api/conversations", s.handleConversations)
		r.Post("/api/conversations", s.handleCreateConversation)
		r.Get("/api/conversations/{id}/members", s.handleMembers)
		r.Post("/api/conversations/{id}/members", s.handleAddMember)
		r.Delete("/api/conversations/{id}/members/{uid}", s.handleRemoveMember)
		r.Post("/api/conversations/{id}/members/{uid}/mute", s.handleMuteMember)
		r.Get("/api/conversations/{id}/messages", s.handleMessages)
		r.Get("/api/conversations/{id}/reads", s.handleReads)
		r.Post("/api/conversations/{id}/send", s.handleSend)
		r.Post("/api/conversations/{id}/recall", s.handleRecall)
		r.Get("/api/stats", s.handleStats)
		r.Get("/ws", s.handleWS)

		r.Route("/api/admin", func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Get("/stats", s.handleAdminStats)
			r.Get("/users", s.handleAdminUsers)
			r.Post("/users/{id}/status", s.handleAdminUserStatus)
			r.Post("/users/{id}/kick", s.handleAdminKick)
			r.Get("/conversations", s.handleAdminConversations)
			r.Get("/messages", s.handleAdminMessages)
		})
	})

	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	stats, _ := s.DB.OutboxStats(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"bus":        "nats-inprocess",
		"delivery":   "at-least-once",
		"schema":     store.SchemaVersion,
		"outbox":     stats,
		"ws_conns":   s.Hub.Connections(),
	})
}

type creds struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Display  string `json:"display_name"`
}

const cookieName = "imchat_session"

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var c creds
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&c); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
	u, err := s.Auth.Register(r.Context(), strings.TrimSpace(c.Username), c.Display, c.Password, false)
	if err != nil {
		writeErr(w, 400, "register_failed", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c creds
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&c); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
	token, u, err := s.Auth.Login(r.Context(), strings.TrimSpace(c.Username), c.Password)
	if err != nil {
		writeErr(w, 401, "login_failed", err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(s.Auth.TTL()),
	})
	writeJSON(w, 200, map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "is_admin": u.IsAdmin})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.Auth.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) token(r *http.Request) string {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	// Browsers attach the cookie automatically, but non-browser clients cannot
	// set headers on a websocket handshake, so a query token is accepted here.
	if v := r.URL.Query().Get("token"); v != "" {
		return v
	}
	return ""
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.Auth.Resolve(r.Context(), s.token(r))
		if err != nil {
			writeErr(w, 401, "unauthorized", "login required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey).(*store.User)
	return u
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	writeJSON(w, 200, map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "is_admin": u.IsAdmin})
}

func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cs, err := s.DB.ConversationsOf(r.Context(), u.ID)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		out = append(out, map[string]any{
			"id": c.ID, "kind": c.Kind, "title": c.Title, "next_seq": c.NextSeq,
		})
	}
	writeJSON(w, 200, map[string]any{"conversations": out})
}

func (s *Server) conversationID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	if _, err := s.DB.RoleOf(r.Context(), cid, u.ID); err != nil {
		writeErr(w, 403, "not_a_member", "you are not in this conversation")
		return
	}
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	msgs, err := s.DB.MessagesAfter(r.Context(), cid, after, limit)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{
			"conversation_id": m.ConversationID, "seq": m.Seq, "id": m.ID,
			"client_msg_id": m.ClientMsgID, "sender_id": m.SenderID,
			"kind": m.Kind, "body": m.Body, "meta": m.Meta, "recall_of": m.RecallOf, "created_at": m.CreatedAt,
		})
	}
	writeJSON(w, 200, map[string]any{"messages": out})
}

type sendReq struct {
	ClientMsgID string `json:"client_msg_id"`
	Kind        string `json:"kind"`
	Body        string `json:"body"`
}

// SendMessage is the single write path for both HTTP and websocket senders.
func (s *Server) SendMessage(ctx context.Context, userID, convID int64, clientMsgID, kind, body string) (*store.Message, error) {
	if _, err := s.DB.RoleOf(ctx, convID, userID); err != nil {
		return nil, errors.New("not a member of this conversation")
	}
	if kind == "" {
		kind = "text"
	}
	if kind == "text" && strings.TrimSpace(body) == "" {
		return nil, errors.New("empty message")
	}
	if until, err := s.DB.MutedUntil(ctx, convID, userID); err == nil && until > time.Now().UnixMilli() {
		return nil, errors.New("you are muted in this conversation")
	}
	members, err := s.DB.Members(ctx, convID)
	if err != nil {
		return nil, err
	}
	meta := "{}"
	if kind == "text" {
		if mlist, err := s.DB.MembersOf(ctx, convID); err == nil {
			mentioned := make([]int64, 0, 2)
			for _, m := range mlist {
				if m.UserID == userID {
					continue
				}
				if strings.Contains(body, "@"+m.Username) || strings.Contains(body, "@"+m.DisplayName) {
					mentioned = append(mentioned, m.UserID)
				}
			}
			if len(mentioned) > 0 {
				if b, err := json.Marshal(map[string]any{"mentions": mentioned}); err == nil {
					meta = string(b)
				}
			}
		}
	}

	return s.DB.SendMessage(ctx, store.SendParams{
		ConversationID: convID,
		SenderID:       userID,
		ClientMsgID:    clientMsgID,
		Kind:           kind,
		Body:           body,
		Meta:           meta,
		Recipients:     members,
	})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	var req sendReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
	m, err := s.SendMessage(r.Context(), u.ID, cid, req.ClientMsgID, req.Kind, req.Body)
	if err != nil {
		writeErr(w, 400, "send_failed", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": m.ID, "seq": m.Seq, "conversation_id": m.ConversationID, "created_at": m.CreatedAt})
}

// broadcastRead fans a read cursor out to the other participants. It goes
// straight onto the bus instead of the outbox because it is state, not history:
// nothing breaks if this frame is lost.
func (s *Server) broadcastRead(ctx context.Context, convID, userID, seq int64) {
	if s.Bus == nil {
		return
	}
	members, err := s.DB.Members(ctx, convID)
	if err != nil {
		return
	}
	payload := ws.Encode(map[string]any{
		"type": "read", "conversation_id": convID, "user_id": userID, "seq": seq,
	})
	for _, m := range members {
		if m == userID {
			continue
		}
		_ = s.Bus.Publish(fmt.Sprintf("user.%d", m), payload)
	}
}

func (s *Server) handleReads(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	if _, err := s.DB.RoleOf(r.Context(), cid, u.ID); err != nil {
		writeErr(w, 403, "not_a_member", "you are not in this conversation")
		return
	}
	cursors, err := s.DB.ReadCursors(r.Context(), cid)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(cursors))
	for _, c := range cursors {
		out = append(out, map[string]any{"user_id": c.UserID, "seq": c.Seq})
	}
	writeJSON(w, 200, map[string]any{"reads": out})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, _ := s.DB.OutboxStats(r.Context())
	writeJSON(w, 200, map[string]any{"outbox": stats, "ws_conns": s.Hub.Connections()})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		OriginPatterns:     []string{"*"},
	})
	if err != nil {
		return
	}
	conn := ws.NewConn(c, u.ID, "")
	s.Hub.Add(conn)
	defer func() {
		s.Hub.Remove(conn)
		conn.Close()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go conn.WriteLoop(ctx)
	conn.Send(ws.Encode(map[string]any{"type": "ready", "user_id": u.ID}))

	conn.ReadLoop(ctx, func(f ws.Frame) {
		switch f.Type {
		case "ping":
			conn.Send(ws.Encode(map[string]any{"type": "pong"}))
		case "send":
			m, err := s.SendMessage(ctx, u.ID, f.ConversationID, f.ClientMsgID, f.Kind, f.Body)
			if err != nil {
				conn.Send(ws.Encode(map[string]any{"type": "error", "code": "send_failed", "message": err.Error()}))
				return
			}
			conn.Send(ws.Encode(map[string]any{
				"type": "sent", "client_msg_id": m.ClientMsgID,
				"conversation_id": m.ConversationID, "seq": m.Seq, "id": m.ID,
			}))
		case "ack":
			_ = s.DB.AckOutbox(ctx, f.ConversationID, f.Seq, u.ID)
		case "read":
			_ = s.DB.SetReadCursor(ctx, f.ConversationID, u.ID, f.Device, f.Seq)
			s.broadcastRead(ctx, f.ConversationID, u.ID, f.Seq)
		}
	})
}
