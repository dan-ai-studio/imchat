package api

// ── 阅读导览 · 第 2 站（第 4 站也住在这一份文件里）────────
// 上一站：站1 进程装配
// 下一站：站3 帧协议

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
	"imchat/internal/media"
	"imchat/internal/store"
	"imchat/internal/ws"
)

// Server is the HTTP surface. It owns the router, the auth manager, the hub and
// the dispatcher; main wires all four in, and this type only uses them.
type Server struct {
	DB         *store.DB
	Auth       *auth.Manager
	Hub        *ws.Hub
	Dispatcher *delivery.Dispatcher
	FilesDir   string
	// Bus carries ephemeral state (read cursors, presence) that must not be
	// persisted: losing it is harmless, the next fetch restores it.
	Bus *bus.Bus
	// Media owns the room peer connections. Nil means "this instance has no
	// media": the RTC endpoints then answer 503 instead of pretending.
	Media *media.Manager
	// Limiter is the per-user send budget. Nil disables the check.
	Limiter *sendLimiter
}

// ctxKey is a private type for request-context keys, so no other package can
// collide with ours by accident.
type ctxKey string

const userKey ctxKey = "imchat.user"

// writeJSON sets the content type before the status: headers freeze once the
// body starts, and forgetting the type is the classic way to ship text/plain.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr is the one error shape this API speaks: a machine code plus a
// human sentence.
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// Router wires every route. Two groups: public (the auth endpoints) and
// authenticated (everything else), with admin routes one level deeper.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Get("/healthz", s.handleHealth)
	r.Post("/api/register", s.handleRegister)
	r.Post("/api/login", s.handleLogin)
	r.Post("/api/logout", s.handleLogout)

	// 公开路由：注册 / 登录 / 登出 / 健康检查，不需要会话。
	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/api/me", s.handleMe)
		r.Post("/api/upload", s.handleUpload)
		r.Get("/files/{name}", s.handleFile)
		r.Get("/api/users", s.handleUsers)
		// 社群是隔离边界：列表与建会话都从路径上的社群出发，
		// 而不是从客户端传来的任何 id 出发。
		r.Post("/api/communities", s.handleCreateCommunity)
		r.Get("/api/communities", s.handleCommunities)
		// 加入社群的两条路：一次性邀请码，或提交申请等管理员审批。
		r.Post("/api/communities/{id}/invites", s.handleCreateInvite)
		r.Post("/api/join/{code}", s.handleJoinByInvite)
		r.Post("/api/communities/{id}/requests", s.handleRequestJoin)
		r.Get("/api/communities/{id}/requests", s.handleJoinRequests)
		// 社群治理：改信息、移出成员、看审计。三者都要求社群管理员。
		r.Patch("/api/communities/{id}", s.handleUpdateCommunity)
		r.Delete("/api/communities/{id}/members/{uid}", s.handleRemoveCommunityMember)
		r.Get("/api/communities/{id}/audit", s.handleAudit)
		r.Post("/api/communities/{id}/requests/{rid}", s.handleDecideJoinRequest)
		// 房间：以在场为核心的会话。列表与建房门都从社群出发。
		r.Post("/api/communities/{id}/rooms", s.handleCreateRoom)
		r.Get("/api/communities/{id}/rooms", s.handleRooms)
		r.Post("/api/rooms/{id}/enter", s.handleEnterRoom)
		r.Post("/api/rooms/{id}/leave", s.handleLeaveRoom)
		// 关房是房主动作；进入与退房只要求是社群成员。
		r.Post("/api/rooms/{id}/close", s.handleCloseRoom)
		// 媒体信令：一次 offer/answer 建连，之后服务端主动 offer 重协商。
		r.Post("/api/rooms/{id}/rtc/offer", s.handleRTCOffer)
		r.Post("/api/rooms/{id}/rtc/answer", s.handleRTCAnswer)
		r.Post("/api/rooms/{id}/rtc/renegotiate", s.handleRTRenegotiate)
		r.Post("/api/rooms/{id}/rtc/leave", s.handleRTCLeave)
		// 麦位：举手、邀请、批准、下麦。麦位是资源，不是权限。
		// 麦位列表对房间内所有人可见，举手与下麦由本人发起。
		r.Get("/api/rooms/{id}/seats", s.handleSeats)
		r.Post("/api/rooms/{id}/seats/request", s.handleRequestSeat)
		r.Post("/api/rooms/{id}/seats/invite", s.handleInviteSeat)
		r.Post("/api/rooms/{id}/seats/{uid}/approve", s.handleApproveSeat)
		// 下麦用 DELETE：麦位行被删掉，不留历史。
		r.Delete("/api/rooms/{id}/seats/{uid}", s.handleLeaveSeat)
		r.Get("/api/rooms/{id}/occupants", s.handleRoomOccupants)
		r.Get("/api/communities/{id}/conversations", s.handleCommunityConversations)
		r.Post("/api/communities/{id}/conversations", s.handleCreateCommunityConversation)
		r.Get("/api/conversations", s.handleConversations)
		// 会话与成员相关的路由集中在这几行；凡是落到某个具体会话的，
		// 都会再查一次会话内角色（owner / admin / member）。
		r.Post("/api/conversations", s.handleCreateConversation)
		r.Get("/api/conversations/{id}/members", s.handleMembers)
		r.Post("/api/conversations/{id}/members", s.handleAddMember)
		r.Delete("/api/conversations/{id}/members/{uid}", s.handleRemoveMember)
		r.Post("/api/conversations/{id}/members/{uid}/mute", s.handleMuteMember)
		// 下面几行是「会话里的动作」：消息回补、已读、发送、撤回。
		r.Get("/api/conversations/{id}/messages", s.handleMessages)
		r.Get("/api/conversations/{id}/reads", s.handleReads)
		r.Post("/api/conversations/{id}/send", s.handleSend)
		r.Post("/api/conversations/{id}/recall", s.handleRecall)
		// 等级：只读展示值，不参与任何权限判断。
		// 举报：进社群队列，由社群管理员处置。
		r.Post("/api/communities/{id}/reports", s.handleCreateReport)
		r.Get("/api/communities/{id}/reports", s.handleReports)
		r.Post("/api/reports/{id}/decide", s.handleDecideReport)
		r.Get("/api/reports/{id}/actions", s.handleReportActions)
		// 申诉只走平台：社群管理员看不到其他社群的申诉。
		// 被处置者的一侧：查针对自己的举报，以及自己提过的申诉。
		r.Get("/api/reports/mine", s.handleMyReports)
		r.Get("/api/appeals/mine", s.handleMyAppeals)
		r.Post("/api/reports/{id}/escalate", s.handleEscalateReport)
		r.Post("/api/reports/{id}/appeals", s.handleCreateAppeal)
		// 平台管理员才有申诉与基线的处置权：它们是平台级对象，不是社群级。
		// 运营看板：平台管理员视角的聚合数字。
		r.With(s.requireAdmin).Get("/api/admin/dashboard", s.handleDashboard)
		r.With(s.requireAdmin).Get("/api/appeals", s.handleAppeals)
		r.With(s.requireAdmin).Post("/api/appeals/{id}/decide", s.handleDecideAppeal)
		r.With(s.requireAdmin).Post("/api/baselines", s.handleAddBaseline)
		r.With(s.requireAdmin).Get("/api/baselines", s.handleBaselines)
		// 等级是公开展示值：登录用户都能读，不套管理员门。
		r.Get("/api/levels/{uid}", s.handleLevel)
		r.Get("/api/levels", s.handleLevels)
		r.Get("/api/stats", s.handleStats)
		r.Get("/ws", s.handleWS)

		// 管理台单独一层，并再套一次 requireAdmin：
		// 平台管理员与会话内角色是两套正交的主体，权限不能互相推导。
		r.Route("/api/admin", func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Get("/stats", s.handleAdminStats)
			// 账号与连接：封禁会同时删掉会话并断开全部长连接，
			// 所以对方的下一次请求就是未授权。
			r.Get("/users", s.handleAdminUsers)
			r.Post("/users/{id}/status", s.handleAdminUserStatus)
			r.Post("/users/{id}/kick", s.handleAdminKick)
			r.Get("/conversations", s.handleAdminConversations)
			r.Get("/messages", s.handleAdminMessages)
		})
	})

	return r
}

// handleHealth answers "is this instance alive, and what is it made of" in one
// object, without touching the session machinery.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// 健康检查必须反映依赖状态，而不是永远回 200：
	// 数据库读不出来就是 unhealthy，其余字段照常给出，方便一眼看出是哪一层坏了。
	stats, err := s.DB.OutboxStats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false, "error": "database unreachable", "schema": store.SchemaVersion,
		})
		return
	}
	// 数据库读得出来就算健康；媒体是可选依赖，没配就是 disabled。
	mediaState := "disabled"
	if s.Media != nil {
		mediaState = "ok"
	}
	// 一次给出四组数：依赖状态、投递深度、连接数、schema 版本。
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"bus":      "nats-inprocess",
		"delivery": "at-least-once",
		"media":    mediaState,
		"schema":   store.SchemaVersion,
		"outbox":   stats,
		"ws_conns": s.Hub.Connections(),
	})
}

// creds is the shared request body of register and login.
type creds struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Display  string `json:"display_name"`
}

const cookieName = "imchat_session"

// handleRegister creates a normal account. It can never grant platform admin:
// that only comes from the command line or the first-run bootstrap.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var c creds
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&c); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
	// 公开注册只能建普通账号：平台管理员不从这条路来。
	u, err := s.Auth.Register(r.Context(), strings.TrimSpace(c.Username), c.Display, c.Password, false)
	if err != nil {
		writeErr(w, 400, "register_failed", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName})
}

// handleLogin verifies the password and plants the session cookie: HttpOnly,
// SameSite=Lax. The browser never hands the token to script.
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
	// 令牌放进 HttpOnly 的 cookie：脚本读不到它，XSS 就偷不走会话。
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

// handleLogout deletes the server-side session and expires the cookie. Both
// halves matter: expiring only the cookie would leave a live row behind.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.Auth.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// token finds the session token in the three places a client may put it: the
// cookie (browsers), the Authorization header (scripts), and the query string
// (websocket handshakes, where custom headers are impossible).
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

// requireAuth resolves the token once and puts the user on the request context.
// Downstream handlers read it with userFrom and never touch the token again.
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

// userFrom returns the authenticated user or nil. Routes behind requireAuth may
// assume non-nil; anything else must check.
func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey).(*store.User)
	return u
}

// handleMe echoes the identity behind the current cookie.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	writeJSON(w, 200, map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "is_admin": u.IsAdmin})
}

// handleConversations lists the conversations this user belongs to.
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cs, err := s.DB.ConversationsOf(r.Context(), u.ID)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	// 只回列表需要的字段，加上会话的序号水位——前端靠它判断自己缺不缺消息。
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		out = append(out, map[string]any{
			"id": c.ID, "kind": c.Kind, "title": c.Title, "next_seq": c.NextSeq,
		})
	}
	writeJSON(w, 200, map[string]any{"conversations": out})
}

// conversationID parses the {id} path parameter and rejects anything that is
// not a positive integer, so handlers never see a zero id.
func (s *Server) conversationID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// handleMessages is the gap filler: everything after a given sequence number,
// capped by limit. Membership is checked before a single row is read.
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	// 先过存在性这道门：跨社群的会话一律当作不存在。
	if !s.conversationVisible(w, r, cid, u.ID) {
		return
	}
	if _, err := s.DB.RoleOf(r.Context(), cid, u.ID); err != nil {
		writeErr(w, 403, "not_a_member", "you are not in this conversation")
		return
	}
	// 成员确认过了。下面两件事都很小：一个可选的起点，一个夹紧的上限。
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
	// after 缺省 0，limit 夹在 1..500：谁也不能一次把整个会话拖走。
	msgs, err := s.DB.MessagesAfter(r.Context(), cid, after, limit)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	// 逐条摊平成 JSON。recall_of 也带上，客户端才知道哪条是墓碑、指向谁。
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

// sendReq is the HTTP body of a send. The websocket path carries the same
// fields inside a frame instead.
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
	// 没给类型就按文本处理。注意图片消息的 body 是文件名，不能按文本判空。
	// 这里是全仓唯一写消息的地方。在别处再开一条写消息的路，会同时破坏三件事：
	// 取号与待投递记录的同事务（序号可能因此出现空洞）、client_msg_id 的幂等去重、
	// 以及 @ 提醒的解析。三件事都长在这一条函数里，绕开它等于绕开全部。
	// 没给类型就按文本处理；空文本消息在这里被拒，不落到写路径。
	if kind == "" {
		kind = "text"
	}
	if kind == "text" && strings.TrimSpace(body) == "" {
		return nil, errors.New("empty message")
	}
	// 禁言是会话内的权力，不是平台级的：这里查的是会话内角色与到期时间。
	if until, err := s.DB.MutedUntil(ctx, convID, userID); err == nil && until > time.Now().UnixMilli() {
		return nil, errors.New("you are muted in this conversation")
	}
	// 房间与会话共用这一条写路径，但投递承诺不同：房间是广播，不落逐接收者义务。
	conv, err := s.DB.ConversationByID(ctx, convID)
	if err != nil {
		return nil, err
	}
	members, err := s.DB.Members(ctx, convID)
	if err != nil {
		return nil, err
	}
	// 权限、内容、禁言都过了，下面只剩一件事：把消息连同它的投递义务一起写进去。
	meta := "{}"
	if kind == "text" {
		if mlist, err := s.DB.MembersOf(ctx, convID); err == nil {
			// 只在文本里找 @用户名 与 @显示名；命中的用户 id 收集起来，
			// 写进消息的 meta，而不是给消息表加一个新字段。
			mentioned := make([]int64, 0, 2)
			for _, m := range mlist {
				if m.UserID == userID {
					continue
				}
				if strings.Contains(body, "@"+m.Username) || strings.Contains(body, "@"+m.DisplayName) {
					mentioned = append(mentioned, m.UserID)
				}
			}
			// 没人被 @ 就不动 meta：默认值 "{}" 已经是一个合法的空对象。
			if len(mentioned) > 0 {
				if b, err := json.Marshal(map[string]any{"mentions": mentioned}); err == nil {
					meta = string(b)
				}
			}
		}
	}

	// 真正落库的是 store 层：取号、消息行、待投递记录在同一个事务里完成。
	msg, err := s.DB.SendMessage(ctx, store.SendParams{
		ConversationID: convID,
		SenderID:       userID,
		ClientMsgID:    clientMsgID,
		// IsRoom 决定 store 层写不写待投递记录：房间不写。
		Kind:           kind,
		Body:           body,
		Meta:           meta,
		Recipients:     members,
		IsRoom:         conv.IsRoom,
	})
	if err != nil {
		return nil, err
	}
	// 发言频率按等级给预算：等级越高，每分钟能说的话越多。
	if s.Limiter != nil {
		lv, _ := s.DB.LevelOf(ctx, userID)
		if lv == nil {
			lv = &store.Level{Level: 1}
		}
		if !s.Limiter.allow(userID, lv.Level*5) {
			return nil, errors.New("你说话太快了，稍后再试")
		}
	}
	// 发言给经验：ref 用消息 id，重投不会重复加分。
	_ = s.DB.AddExp(ctx, userID, "message", msg.ID, 1)
	// 房间消息不经过投递器：写成功后直接推给此刻在场的人。
	if conv.IsRoom {
		s.broadcastToRoom(ctx, convID, msg)
	}
	return msg, nil
}

// broadcastToRoom pushes one room message to everyone currently present. It goes
// straight onto the bus because a room promises "whoever is here now", not
// per-recipient delivery; nobody present means nobody to push to.
func (s *Server) broadcastToRoom(ctx context.Context, roomID int64, m *store.Message) {
	if s.Bus == nil {
		return
	}
	occupants, err := s.DB.RoomOccupants(ctx, roomID)
	if err != nil {
		return
	}
	payload := store.MessageFrameJSON(m)
	for _, uid := range occupants {
		_ = s.Bus.Publish(fmt.Sprintf("user.%d", uid), payload)
	}
}

// handleSend is the HTTP twin of the websocket send frame. Both funnel into
// SendMessage, the only place a message is ever written.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	// 请求体限 64KB：一条消息不该把服务端撑爆。
	var req sendReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
	// 与 websocket 那条路走同一个函数：唯一写路径不是口号，是两个入口收敛到一处。
	m, err := s.SendMessage(r.Context(), u.ID, cid, req.ClientMsgID, req.Kind, req.Body)
	if err != nil {
		writeErr(w, 400, "send_failed", err.Error())
		return
	}
	// 回执里给出 seq：客户端拿它立刻能对上界面上的消息，不必等推送回来。
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
	// 已读位点是状态不是历史，所以走总线直发、不进 outbox：丢了下次拉取就回来。
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

// handleReads returns every participant's read cursor for one conversation.
func (s *Server) handleReads(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	// 先过存在性这道门：跨社群的会话一律当作不存在。
	if !s.conversationVisible(w, r, cid, u.ID) {
		return
	}
	if _, err := s.DB.RoleOf(r.Context(), cid, u.ID); err != nil {
		writeErr(w, 403, "not_a_member", "you are not in this conversation")
		return
	}
	// 位点按终端存、按用户取最大值——这就是「已读」在数据库里的真实形状。
	cursors, err := s.DB.ReadCursors(r.Context(), cid)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	// 每个终端一个位点，这里原样给出；谁已读到哪里由调用方自己判断。
	out := make([]map[string]any, 0, len(cursors))
	for _, c := range cursors {
		out = append(out, map[string]any{"user_id": c.UserID, "seq": c.Seq})
	}
	writeJSON(w, 200, map[string]any{"reads": out})
}

// handleStats is the cheap version of the health numbers: outbox depth and live
// connection count.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, _ := s.DB.OutboxStats(r.Context())
	writeJSON(w, 200, map[string]any{"outbox": stats, "ws_conns": s.Hub.Connections()})
}

// handleWS upgrades the request and then owns the connection for its lifetime:
// one writer goroutine, one read loop, and the frame switch that is the whole
// protocol. The user was already resolved by requireAuth — that is exactly why
// this connection can trust its own identity.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	// 升级成 websocket。这里显式关掉了 Origin 校验：客户端可能是任何来源的非浏览器
	// 程序，升级前也没有人给它发过令牌。浏览器一侧的防护由会话 cookie 的
	// SameSite=Lax 承担——跨站的握手带不上这张 cookie，所以拿不到身份。
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		OriginPatterns:     []string{"*"},
	})
	if err != nil {
		return
	}
	// 连接对象记住用户 id：这条长连接从此不必再带令牌。
	conn := ws.NewConn(c, u.ID, "")
	s.Hub.Add(conn)
	defer func() {
		s.Hub.Remove(conn)
		conn.Close()
	}()

	// 一条连接一个可取消的 ctx：连接断开，写循环与读循环一起退出。
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go conn.WriteLoop(ctx)
	conn.Send(ws.Encode(map[string]any{"type": "ready", "user_id": u.ID}))

	// 读循环里就是这个协议的全部：四个上行帧各做什么，见站3 的帧清单。
	conn.ReadLoop(ctx, func(f ws.Frame) {
		switch f.Type {
		case "ping":
			conn.Send(ws.Encode(map[string]any{"type": "pong"}))
		// send 与 HTTP 那条路共用一个写函数；ack 是唯一改状态的帧。
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
		// ack 是整个协议里唯一改状态的帧：它让一条待投递记录消解。
		case "ack":
			_ = s.DB.AckOutbox(ctx, f.ConversationID, f.Seq, u.ID)
		case "read":
			_ = s.DB.SetReadCursor(ctx, f.ConversationID, u.ID, f.Device, f.Seq)
			s.broadcastRead(ctx, f.ConversationID, u.ID, f.Seq)
		}
	})
}
