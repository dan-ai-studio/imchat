package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"imchat/internal/store"
)

// communityOf resolves the {id} path parameter and proves the caller belongs to
// it. Every route under /api/communities goes through here, so a non-member
// always sees the same 404 as a community that does not exist — the response
// must not reveal that the community exists.
func (s *Server) communityOf(w http.ResponseWriter, r *http.Request) (*store.Community, bool) {
	u := userFrom(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "community not found")
		return nil, false
	}
	// 先证明成员身份，再取社群：两个失败都回同一个 404。
	if _, err := s.DB.CommunityRole(r.Context(), id, u.ID); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "community not found")
		return nil, false
	}
	c, err := s.DB.CommunityByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "community not found")
		return nil, false
	}
	return c, true
}

// conversationVisible is the existence gate every conversation route runs
// first. A conversation outside every community the caller belongs to answers
// 404, not 403: a permission error would confirm that the object exists.
func (s *Server) conversationVisible(w http.ResponseWriter, r *http.Request, convID, userID int64) bool {
	ok, err := s.DB.ConversationInUserCommunity(r.Context(), convID, userID)
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "not_found", "conversation not found")
		return false
	}
	return true
}

// handleCreateCommunity creates a community and makes the caller its owner.
func (s *Server) handleCreateCommunity(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	// 名字是唯一必填项：空白名字建出来的社群在侧栏里无法辨认。
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 去空白后再判空：一个全是空格的名字等于没填。
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name_required", "community name is required")
		return
	}
	// 创建者同时成为 owner：社群与它的第一个管理员在同一个事务里落地。
	c, err := s.DB.CreateCommunity(r.Context(), u.ID, req.Name, req.Description)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// handleCommunities lists the communities the caller belongs to.
func (s *Server) handleCommunities(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	list, err := s.DB.CommunitiesOf(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"communities": list})
}

// handleCommunityConversations lists one user's conversations inside one
// community. The community filter is applied by the scope, not by the handler.
func (s *Server) handleCommunityConversations(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	// 作用域从路径上的社群构造，调用方不能传别的社群进来。
	u := userFrom(r.Context())
	scope, err := s.DB.InCommunity(c.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "scope_failed", err.Error())
		return
	}
	// 列表由作用域给出：调用方拿不到别的社群的会话。
	list, err := scope.ConversationsOf(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

// handleCreateCommunityConversation creates a group conversation inside one
// community. The community is taken from the path and re-checked against the
// caller's membership, never from the body.
func (s *Server) handleCreateCommunityConversation(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	// 社群已由 communityOf 校验过，这里只处理请求体。
	u := userFrom(r.Context())
	var req createConvReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	if req.Kind != "" && req.Kind != "group" {
		writeErr(w, http.StatusBadRequest, "unsupported_kind", "only group conversations can be created explicitly")
		return
	}
	// 没起名字就给个默认的：空的标题不值得拒绝一次建群。
	if req.Title == "" {
		req.Title = "新群聊"
	}
	conv, err := s.DB.CreateGroupIn(r.Context(), c.ID, u.ID, req.Title, req.Members)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": conv.ID, "kind": conv.Kind, "title": conv.Title, "community_id": conv.CommunityID})
}
