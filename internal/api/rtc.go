package api

import (
	"encoding/json"
	"net/http"

	"github.com/pion/webrtc/v4"

	"imchat/internal/media"
)

// handleRTCOffer is the single signalling round trip for a room: the client
// publishes its microphone as an offer, and the answer carries every track
// already published in the room.
func (s *Server) handleRTCOffer(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	if s.Media == nil {
		writeErr(w, http.StatusServiceUnavailable, "media_disabled", "media is not configured on this instance")
		return
	}
	u := userFrom(r.Context())
	// SDP 可以很大（多路音频 + ICE），上限放到 1MB 而不是消息的 64KB。
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 一次往返：服务端在 answer 里带上房间里已有的全部音轨。
	answer, err := s.Media.Room(room.ID).Join(u.ID, webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.SDP})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "offer_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": "answer", "sdp": answer.SDP, "room_id": room.ID})
}

// handleRTRenegotiate is the client-initiated counterpart: it adds a track
// (video, a second microphone) to an already-open connection.
func (s *Server) handleRTRenegotiate(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	if s.Media == nil {
		writeErr(w, http.StatusServiceUnavailable, "media_disabled", "media is not configured on this instance")
		return
	}
	u := userFrom(r.Context())
	// 追加轨道（视频、第二路麦克风）时客户端重新出 offer，服务端只答不建连。
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 连接已经不在了（关标签页、被踢）：409 而不是 500，客户端可以重进房间。
	answer, err := s.Media.Room(room.ID).Renegotiate(u.ID, webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.SDP})
	if err != nil {
		writeErr(w, http.StatusConflict, "renegotiate_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": "answer", "sdp": answer.SDP})
}

// handleRTCAnswer applies the answer a client sent back after the server
// renegotiated (a new speaker joined or left).
func (s *Server) handleRTCAnswer(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	if s.Media == nil {
		writeErr(w, http.StatusServiceUnavailable, "media_disabled", "media is not configured on this instance")
		return
	}
	u := userFrom(r.Context())
	// 重协商的 answer 与首次 offer 同形，所以解码路径共用一套。
	var req struct {
		SDP string `json:"sdp"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 应用 answer 之后，这条连接开始收服务端转发过来的音轨。
	if err := s.Media.Room(room.ID).AcceptAnswer(u.ID, webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: req.SDP}); err != nil {
		writeErr(w, http.StatusBadRequest, "answer_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRTCLeave closes one participant's media connection. It is separate from
// leaving the room: closing the tab must not leave a peer connection behind.
func (s *Server) handleRTCLeave(w http.ResponseWriter, r *http.Request) {
	room, ok := s.roomOf(w, r)
	if !ok {
		return
	}
	if s.Media == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	// 退媒体与会话退场分开：关标签页不该留下一条半死的 PeerConnection。
	u := userFrom(r.Context())
	s.Media.Room(room.ID).Leave(u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// NewMediaManager builds the SFU manager, pushing signalling frames through the
// same bus the message fan-out uses.
func NewMediaManager(push func(userID int64, payload []byte) error) *media.Manager {
	return media.NewManager(push)
}
