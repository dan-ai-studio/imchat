package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// handleUsers lists active accounts so the client can pick members when
// creating a group. It deliberately exposes nothing beyond identity.
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.DB.ListUsers(r.Context(), 200)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	// 只回身份三件套；被禁用的账号直接从列表里消失，别人也就拉不动他。
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		if u.Status != "active" {
			continue
		}
		out = append(out, map[string]any{
			"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
		})
	}
	writeJSON(w, 200, map[string]any{"users": out})
}

// canGovern answers the conversation-role question: owner or admin, nothing
// else. It never looks at the platform-admin flag — those are different powers,
// and deriving one from the other is exactly what this project forbids.
func (s *Server) canGovern(ctx context.Context, convID, userID int64) bool {
	role, err := s.DB.RoleOf(ctx, convID, userID)
	if err != nil {
		return false
	}
	return role == "owner" || role == "admin"
}

// createConvReq is the body of "create a group": what it is called, and who
// starts inside it.
type createConvReq struct {
	Kind    string  `json:"kind"`
	Title   string  `json:"title"`
	Members []int64 `json:"members"`
}

// handleCreateConversation creates a group. Direct conversations are not made
// here; they come into being elsewhere in the store layer.
func (s *Server) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	var req createConvReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
	if req.Kind != "" && req.Kind != "group" {
		writeErr(w, 400, "unsupported_kind", "only group conversations can be created explicitly")
		return
	}
	// 没起名字就给个默认的：空的标题不值得拒绝一次建群。
	if req.Title == "" {
		req.Title = "新群聊"
	}
	c, err := s.DB.CreateGroup(r.Context(), u.ID, req.Title, req.Members)
	if err != nil {
		writeErr(w, 400, "create_failed", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": c.ID, "kind": c.Kind, "title": c.Title})
}

// handleMembers lists everyone in a conversation with their role and mute state.
func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	// 成员才能看成员列表：先查角色，再决定要不要读数据。
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	// 先过存在性这道门：跨社群的会话一律当作不存在。
	if !s.conversationVisible(w, r, cid, u.ID) {
		return
	}
	// 非成员连列表都不该看到：成员身份是这一站的第二个门。
	if _, err := s.DB.RoleOf(r.Context(), cid, u.ID); err != nil {
		writeErr(w, 403, "not_a_member", "you are not in this conversation")
		return
	}
	members, err := s.DB.MembersOf(r.Context(), cid)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	// muted 是派生的布尔值：前端不该自己拿到期时间和本地时钟去比。
	now := time.Now().UnixMilli()
	out := make([]map[string]any, 0, len(members))
	// 每个成员一行，带上角色与禁言到期时间——管理界面要的就是这些。
	for _, m := range members {
		out = append(out, map[string]any{
			"user_id": m.UserID, "username": m.Username, "display_name": m.DisplayName,
			"role": m.Role, "muted": m.MutedUntil > now, "muted_until": m.MutedUntil,
		})
	}
	writeJSON(w, 200, map[string]any{"members": out})
}

// memberReq is shared by the add and mute endpoints: who, and for how long.
type memberReq struct {
	UserID  int64  `json:"user_id"`
	Role    string `json:"role"`
	Minutes int    `json:"minutes"`
}

// handleAddMember puts one account into the conversation as a plain member. It
// checks the caller's conversation role, never the platform-admin flag.
func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	// 加人是会话内的权力：下面查的是角色，与平台管理员无关。
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	// 过不了这道门就是 403：不是「没找到」，是「你没这个权力」。
	if !s.canGovern(r.Context(), cid, u.ID) {
		writeErr(w, 403, "forbidden", "only the owner or an admin can add members")
		return
	}
	var req memberReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.UserID <= 0 {
		writeErr(w, 400, "bad_json", "user_id is required")
		return
	}
	// 先确认这个人真的存在，再写成员表——否则错误信息分不清「没这人」还是「加不进去」。
	if _, err := s.DB.UserByID(r.Context(), req.UserID); err != nil {
		writeErr(w, 404, "no_such_user", "no such user")
		return
	}
	// 加进来的人一律是 member：owner 在创建时就定了，admin 要另外的入口。
	if err := s.DB.AddMember(r.Context(), cid, req.UserID, "member"); err != nil {
		writeErr(w, 400, "add_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleRemoveMember removes one participant. The owner is the one exception no
// caller can remove, and self-removal (leaving) needs no governing role.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	// 这条路上要分清三个身份：说话的人、被移的人、群主。
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	target, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
	if err != nil || target <= 0 {
		writeErr(w, 400, "bad_id", "member id must be a positive integer")
		return
	}
	// 取出会话本身，只为了一个判断：目标是不是群主。
	conv, err := s.DB.ConversationByID(r.Context(), cid)
	if err != nil {
		writeErr(w, 404, "not_found", "conversation not found")
		return
	}
	// 群主不可被移除：这是这条路上唯一没有例外的规则。
	if target == conv.OwnerID {
		writeErr(w, 409, "cannot_remove_owner", "the owner cannot be removed")
		return
	}
	// 两种合法情形：自己退出（不需要权限），或者有治理角色（移除别人）。
	if u.ID != target && !s.canGovern(r.Context(), cid, u.ID) {
		writeErr(w, 403, "forbidden", "only the owner or an admin can remove members")
		return
	}
	if err := s.DB.RemoveMember(r.Context(), cid, target); err != nil {
		writeErr(w, 400, "remove_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleMuteMember sets when a participant may speak again. It writes a
// timestamp, so an expired mute needs no cleanup job.
func (s *Server) handleMuteMember(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	// 禁言写的是一段时长，不是布尔开关；到期自动失效，不需要清理任务。
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	target, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
	if err != nil || target <= 0 {
		writeErr(w, 400, "bad_id", "member id must be a positive integer")
		return
	}
	// 禁言是会话内的权力：查的是会话内角色，与平台管理员无关。
	if !s.canGovern(r.Context(), cid, u.ID) {
		writeErr(w, 403, "forbidden", "only the owner or an admin can mute members")
		return
	}
	var req memberReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
	// minutes 缺省 0 表示解除禁言；正数才是一个新的到期时间。
	until := int64(0)
	if req.Minutes > 0 {
		until = time.Now().Add(time.Duration(req.Minutes) * time.Minute).UnixMilli()
	}
	if err := s.DB.SetMuted(r.Context(), cid, target, until); err != nil {
		writeErr(w, 400, "mute_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "muted_until": until})
}
