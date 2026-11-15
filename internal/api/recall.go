package api

// ── 阅读导览 · 第 10 站 ───────────────────────────────────
// 上一站：站9 断线补齐
// 下一站：（终点）主线之后还有三条旁路

import (
	"encoding/json"
	"errors"
	"net/http"

	"imchat/internal/store"
)

// recallReq is the HTTP body of a recall: only the sequence being voided.
type recallReq struct {
	Seq int64 `json:"seq"`
}

// handleRecall voids one message by appending a tombstone. It never edits the
// original row, so history stays append-only (ADR-0005).
func (s *Server) handleRecall(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	cid, ok := s.conversationID(r)
	if !ok {
		writeErr(w, 400, "bad_id", "conversation id must be a positive integer")
		return
	}
	// 请求体只有 14 位上限：撤回请求就该只有一个数字。
	var req recallReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req); err != nil || req.Seq <= 0 {
		writeErr(w, 400, "bad_json", "seq is required")
		return
	}
	// 先取接收者名单，墓碑也要投给这些人——否则有人永远看不到它被撤回。
	members, err := s.DB.Members(r.Context(), cid)
	if err != nil {
		writeErr(w, 500, "query_failed", err.Error())
		return
	}
	m, err := s.DB.RecallMessage(r.Context(), cid, u.ID, req.Seq, members)
	// 四种失败分开回：不是你的、已经撤回过、消息不存在，客户端要能分辨。
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotSender):
		writeErr(w, 403, "not_your_message", "you can only recall your own messages")
		return
	// 已经撤回过：这不是服务器出错，用 409 告诉客户端这条已是终态。
	case errors.Is(err, store.ErrAlreadyRecalled):
		writeErr(w, 409, "already_recalled", "this message was already recalled")
		return
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not_found", "message not found")
		return
	// 其余错误一律 500：调用方不该靠猜是哪一类。
	default:
		writeErr(w, 500, "recall_failed", err.Error())
		return
	}
	// 回执带上新墓碑的 seq 与被撤回的 seq：客户端拿它就能立刻更新界面。
	writeJSON(w, 201, map[string]any{
		"seq": m.Seq, "recall_of": m.RecallOf, "conversation_id": m.ConversationID,
	})
}
