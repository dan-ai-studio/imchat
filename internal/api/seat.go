package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleSeats lists the seat table of one room: who is on the mic, who was
// invited, who raised a hand.
func (s *Server) handleSeats(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	list, err := s.DB.SeatsOf(r.Context(), room.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	// 字段名就是线上契约：与其余接口一致。
	out := make([]map[string]any, 0, len(list))
	for _, st := range list {
		out = append(out, map[string]any{
			"user_id": st.UserID, "username": st.Username, "display_name": st.DisplayName,
			"position": st.Position, "state": st.State,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"seats": out, "max": 16})
}

// handleRequestSeat records a raised hand.
func (s *Server) handleRequestSeat(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if err := s.DB.RequestSeat(r.Context(), room.ID, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "request_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": "requested"})
}

// handleInviteSeat puts an invitation on the table. Only the room owner may do
// it: inviting is a room-level action, not a community one.
func (s *Server) handleInviteSeat(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if room.OwnerID != u.ID {
		writeErr(w, http.StatusForbidden, "forbidden", "only the room owner can invite")
		return
	}
	// 被邀请人由请求体指定：邀请是房主指向某个人的动作。
	var req struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 邀请只是把状态写成 invited：对方接不接受由他自己决定。
	if err := s.DB.InviteSeat(r.Context(), room.ID, req.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, "invite_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": "invited"})
}

// handleApproveSeat promotes a requested or invited person onto the mic.
func (s *Server) handleApproveSeat(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if room.OwnerID != u.ID {
		writeErr(w, http.StatusForbidden, "forbidden", "only the room owner can approve")
		return
	}
	// 被批准的人来自路径；id 非法是 400，不是「找不到」。
	uid, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "user id must be a positive integer")
		return
	}
	// 满员是 409 不是 500：这是可预期的业务状态。
	// 批准即上麦；满员由存储层拒绝，这里只负责翻译状态码。
	if err := s.DB.TakeSeat(r.Context(), room.ID, uid); err != nil {
		writeErr(w, http.StatusConflict, "seat_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": "on_mic"})
}

// handleLeaveSeat removes one person from the seat table. The owner may remove
// anyone; everyone else may only remove themselves.
func (s *Server) handleLeaveSeat(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	uid, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "user id must be a positive integer")
		return
	}
	// 自己下麦随时可以；抱别人下麦只有房主能做。
	if uid != u.ID && room.OwnerID != u.ID {
		writeErr(w, http.StatusForbidden, "forbidden", "only the room owner can remove someone else")
		return
	}
	if err := s.DB.LeaveSeat(r.Context(), room.ID, uid); err != nil {
		writeErr(w, http.StatusInternalServerError, "leave_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
