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

func (s *Server) canGovern(ctx context.Context, convID, userID int64) bool {
	role, err := s.DB.RoleOf(ctx, convID, userID)
	if err != nil {
		return false
	}
	return role == "owner" || role == "admin"
}

type createConvReq struct {
	Kind    string  `json:"kind"`
	Title   string  `json:"title"`
	Members []int64 `json:"members"`
}

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

func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request) {
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
	members, err := s.DB.MembersOf(r.Context(), cid)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	now := time.Now().UnixMilli()
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		out = append(out, map[string]any{
			"user_id": m.UserID, "username": m.Username, "display_name": m.DisplayName,
			"role": m.Role, "muted": m.MutedUntil > now, "muted_until": m.MutedUntil,
		})
	}
	writeJSON(w, 200, map[string]any{"members": out})
}

type memberReq struct {
	UserID int64 `json:"user_id"`
	Role   string `json:"role"`
	Minutes int  `json:"minutes"`
}

func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	if !s.canGovern(r.Context(), cid, u.ID) {
		writeErr(w, 403, "forbidden", "only the owner or an admin can add members")
		return
	}
	var req memberReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.UserID <= 0 {
		writeErr(w, 400, "bad_json", "user_id is required")
		return
	}
	if _, err := s.DB.UserByID(r.Context(), req.UserID); err != nil {
		writeErr(w, 404, "no_such_user", "no such user")
		return
	}
	if err := s.DB.AddMember(r.Context(), cid, req.UserID, "member"); err != nil {
		writeErr(w, 400, "add_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
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
	conv, err := s.DB.ConversationByID(r.Context(), cid)
	if err != nil {
		writeErr(w, 404, "not_found", "conversation not found")
		return
	}
	if target == conv.OwnerID {
		writeErr(w, 409, "cannot_remove_owner", "the owner cannot be removed")
		return
	}
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

func (s *Server) handleMuteMember(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
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
	if !s.canGovern(r.Context(), cid, u.ID) {
		writeErr(w, 403, "forbidden", "only the owner or an admin can mute members")
		return
	}
	var req memberReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "body is not valid json")
		return
	}
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
