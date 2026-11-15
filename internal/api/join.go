package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"imchat/internal/store"
)

// communityAdmin proves the caller is the owner or an administrator of this
// community. It is the second gate after membership: belonging is not enough
// to hand out invites or decide who joins.
func (s *Server) communityAdmin(w http.ResponseWriter, r *http.Request, c *store.Community, userID int64) bool {
	role, err := s.DB.CommunityRole(r.Context(), c.ID, userID)
	if err != nil || (role != "owner" && role != "admin") {
		writeErr(w, http.StatusForbidden, "forbidden", "community admin only")
		return false
	}
	return true
}

// handleCreateInvite mints a one-time invite code for this community.
func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	// 只有社群管理员能发码：普通成员不能扩大成员池。
	if !s.communityAdmin(w, r, c, u.ID) {
		return
	}
	code, err := s.DB.CreateInvite(r.Context(), c.ID, u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "invite_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"code": code, "community_id": c.ID})
}

// handleJoinByInvite consumes a code and joins the caller to its community.
func (s *Server) handleJoinByInvite(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	code := chi.URLParam(r, "code")
	communityID, err := s.DB.JoinByInvite(r.Context(), code, u.ID)
	if err != nil {
		// 码不存在与码已用掉回同一个 404：不告诉猜码的人他猜中过。
		writeErr(w, http.StatusNotFound, "invite_unusable", "invite code is not usable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"community_id": communityID})
}

// handleRequestJoin records a pending request to join this community. It is the
// door for people who do not have a code.
func (s *Server) handleRequestJoin(w http.ResponseWriter, r *http.Request) {
	// 申请人还不是成员，所以不能用 communityOf（它要求成员身份）；
	// 这里只确认社群存在，剩下的交给申请记录。
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "community not found")
		return
	}
	c, err := s.DB.CommunityByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "community not found")
		return
	}
	u := userFrom(r.Context())
	// 已经是成员就没必要申请；直接告诉调用方结果。
	if _, err := s.DB.CommunityRole(r.Context(), c.ID, u.ID); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "member"})
		return
	}
	if err := s.DB.RequestJoin(r.Context(), c.ID, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "request_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "pending"})
}

// handleJoinRequests lists the pending requests of this community.
func (s *Server) handleJoinRequests(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if !s.communityAdmin(w, r, c, u.ID) {
		return
	}
	// 只列待审的：已决的记录留在库里做审计，不占这个列表。
	list, err := s.DB.JoinRequests(r.Context(), c.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	// 审批列表只需要辨认「谁申请了」，不暴露其他字段。
	out := make([]map[string]any, 0, len(list))
	for _, j := range list {
		out = append(out, map[string]any{"id": j.ID, "user_id": j.UserID, "username": j.Username, "display_name": j.DisplayName, "created_at": j.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

// handleDecideJoinRequest approves or rejects one request. The decision body is
// {"approve": true|false}; approval also creates the membership.
func (s *Server) handleDecideJoinRequest(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if !s.communityAdmin(w, r, c, u.ID) {
		return
	}
	// 请求 id 来自路径，先解析再交给存储层；非法 id 是 400，不是 404。
	rid, err := strconv.ParseInt(chi.URLParam(r, "rid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "request id must be a positive integer")
		return
	}
	// 决定只有两种：批准或拒绝；缺省的 false 就是拒绝，不会误批。
	var req struct {
		Approve bool `json:"approve"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 批准会同时写入成员行；拒绝只改状态。两件事都在存储层同一个事务里。
	if err := s.DB.DecideJoinRequest(r.Context(), rid, u.ID, req.Approve); err != nil {
		writeErr(w, http.StatusBadRequest, "decide_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": req.Approve})
}
