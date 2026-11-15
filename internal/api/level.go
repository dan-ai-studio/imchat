package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleLevel returns one account's level. It is public to signed-in users: a
// level is a display value, not private data.
func (s *Server) handleLevel(w http.ResponseWriter, r *http.Request) {
	uid, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "user id must be a positive integer")
		return
	}
	l, err := s.DB.LevelOf(r.Context(), uid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "level_failed", err.Error())
		return
	}
	// 下一级还差多少：前端进度条要用它，不自己算。
	next := (l.Level) * 100
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": l.UserID, "level": l.Level, "exp": l.Exp, "next_at": next,
	})
}

// handleLevels returns several accounts' levels in one call, so a member list
// does not fan out into N requests.
func (s *Server) handleLevels(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query()["uid"]
	parsed := make([]int64, 0, len(ids))
	for _, raw := range ids {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			parsed = append(parsed, v)
		}
	}
	// 一次批量取回：成员列表要显示所有人的等级，不能 N 次往返。
	levels, err := s.DB.LevelsOf(r.Context(), parsed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "levels_failed", err.Error())
		return
	}
	// 数组而不是 map：JSON 的 map 键只能是字符串，数组更容易被前端消费。
	out := make([]map[string]any, 0, len(levels))
	for _, l := range levels {
		out = append(out, map[string]any{"user_id": l.UserID, "level": l.Level, "exp": l.Exp})
	}
	writeJSON(w, http.StatusOK, map[string]any{"levels": out})
}
