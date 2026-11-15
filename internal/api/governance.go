package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// handleUpdateCommunity changes the name and description of one community.
// Only a community administrator may do it; platform admin is not implied.
func (s *Server) handleUpdateCommunity(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if !s.communityAdmin(w, r, c, u.ID) {
		return
	}
	// 名字必填：空名字会让侧栏里的社群无法辨认。
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
	// 改信息是社群级动作：平台管理员不能替社群改，社群管理员也不能改别的社群。
	if err := s.DB.UpdateCommunity(r.Context(), c.ID, req.Name, req.Description); err != nil {
		writeErr(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	// 改名是治理动作，进审计：谁把社群从什么改成了什么。
	_ = s.DB.AppendAudit(r.Context(), c.ID, u.ID, "community.update", c.Name, req.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRemoveCommunityMember removes one member from the community. The owner
// cannot be removed; the store rejects that case.
func (s *Server) handleRemoveCommunityMember(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if !s.communityAdmin(w, r, c, u.ID) {
		return
	}
	// 被移出的人来自路径；id 非法是 400，不是「找不到」。
	uid, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "user id must be a positive integer")
		return
	}
	// 群主不能被移出，这条由存储层拒绝；这里只负责把错误翻成 400。
	if err := s.DB.RemoveCommunityMember(r.Context(), c.ID, uid); err != nil {
		writeErr(w, http.StatusBadRequest, "remove_failed", err.Error())
		return
	}
	_ = s.DB.AppendAudit(r.Context(), c.ID, u.ID, "community.member.remove", strconv.FormatInt(uid, 10), "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAudit lists recent audit entries for one community, newest first.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if !s.communityAdmin(w, r, c, u.ID) {
		return
	}
	// 最近一百条：审计是追责用的，不是全量导出。
	list, err := s.DB.AuditOf(r.Context(), c.ID, 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	// 字段名就是线上契约：和其余接口一样用小写下划线。
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		out = append(out, map[string]any{"id": e.ID, "actor_id": e.ActorID, "action": e.Action, "target": e.Target, "detail": e.Detail, "created_at": e.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": out})
}
