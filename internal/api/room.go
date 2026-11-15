package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"imchat/internal/store"
)

// roomOf resolves a room id and proves the caller belongs to its community.
// A room outside the caller's communities answers 404, same as a conversation.
func (s *Server) roomOf(w http.ResponseWriter, r *http.Request) (*store.Conversation, bool) {
	u := userFrom(r.Context())
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "room not found")
		return nil, false
	}
	// 先取会话再判房间：不是房间的对象在这里就当作不存在。
	c, err := s.DB.ConversationByID(r.Context(), id)
	if err != nil || !c.IsRoom {
		writeErr(w, http.StatusNotFound, "not_found", "room not found")
		return nil, false
	}
	// 存在性门禁与会话一致：不在这个社群的人不该知道房间存在。
	if !s.conversationVisible(w, r, c.ID, u.ID) {
		return nil, false
	}
	return c, true
}

// handleCreateRoom creates a room inside one community and enters it.
func (s *Server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	// 标题可以为空：空标题会被补成默认名，不值得为此拒绝一次开房。
	var req struct {
		Title    string `json:"title"`
		MinLevel int    `json:"min_level"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 没起名字就给个默认的：空标题不值得拒绝一次开房。
	if req.Title == "" {
		req.Title = "新房间"
	}
	// 门槛可以为 0：不设门槛的房间任何人都能进。
	if req.MinLevel < 0 {
		req.MinLevel = 0
	}
	room, err := s.DB.CreateRoomIn(r.Context(), c.ID, u.ID, req.Title)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	// 门槛在开房时一并落库，之后可以改。
	if req.MinLevel > 0 {
		if err := s.DB.SetRoomMinLevel(r.Context(), room.ID, u.ID, req.MinLevel); err != nil {
			writeErr(w, http.StatusInternalServerError, "gate_failed", err.Error())
			return
		}
	}
	// 房主自动在场：开完房就应该能看到自己这间房。
	if err := s.DB.EnterRoom(r.Context(), room.ID, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "enter_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": room.ID, "kind": room.Kind, "title": room.Title, "community_id": room.CommunityID})
}

// handleRooms lists the rooms of one community, newest first.
func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	list, err := s.DB.RoomsOf(r.Context(), c.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	// 字段名就是线上契约：与其余接口一致。
	out := make([]map[string]any, 0, len(list))
	for _, room := range list {
		occupants, _ := s.DB.RoomOccupants(r.Context(), room.ID)
		// 门槛一起给出去：前端要在房间卡片上标「需要 Lv.N」。
		minLevel, _ := s.DB.RoomGate(r.Context(), room.ID, 0)
		out = append(out, map[string]any{
			"id": room.ID, "kind": room.Kind, "title": room.Title,
			"owner_id": room.OwnerID, "online": len(occupants), "next_seq": room.NextSeq,
			"min_level": minLevel,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": out})
}

// handleEnterRoom marks the caller present in a room.
func (s *Server) handleEnterRoom(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	// 门槛只是等级比较：低于它进不来，达到就能进，不涉及任何管理动作。
	minLevel, err := s.DB.RoomGate(r.Context(), room.ID, u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "gate_failed", err.Error())
		return
	}
	// 门槛为 0 表示不设限，连等级都不必查。
	if minLevel > 0 {
		lv, _ := s.DB.LevelOf(r.Context(), u.ID)
		if lv == nil || lv.Level < minLevel {
			writeErr(w, http.StatusForbidden, "level_too_low", "这间房需要更高的等级")
			return
		}
	}
	if err := s.DB.EnterRoom(r.Context(), room.ID, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "enter_failed", err.Error())
		return
	}
	// 回执里带当前人数：前端进场后不用再拉一次。
	// 进房算一次在场经验：同一小时只记一次，避免挂机刷分。
	hour := time.Now().UTC().Format("2006-01-02T15")
	_ = s.DB.AddExp(r.Context(), u.ID, "presence", hour, 2)
	occupants, _ := s.DB.RoomOccupants(r.Context(), room.ID)
	writeJSON(w, http.StatusOK, map[string]any{"id": room.ID, "online": len(occupants)})
}

// handleLeaveRoom removes the caller's presence. Membership stays.
func (s *Server) handleLeaveRoom(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if err := s.DB.LeaveRoom(r.Context(), room.ID, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "leave_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCloseRoom ends a room. Only its owner can close it.
func (s *Server) handleCloseRoom(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if err := s.DB.CloseRoom(r.Context(), room.ID, u.ID); err != nil {
		writeErr(w, http.StatusForbidden, "close_failed", "only the room owner can close this room")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRoomOccupants lists who is present right now.
func (s *Server) handleRoomOccupants(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	occupants, err := s.DB.RoomOccupants(r.Context(), room.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"online": len(occupants), "user_ids": occupants})
}
