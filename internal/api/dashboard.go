package api

import (
	"net/http"
)

// handleDashboard returns the platform-wide operations snapshot.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.DB.DashboardStats(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "dashboard_failed", err.Error())
		return
	}
	// 字段名就是线上契约：与其余接口一致。
	writeJSON(w, http.StatusOK, map[string]any{
		"rooms": d.Rooms, "room_online": d.RoomOnline,
		"messages": d.Messages, "messages_24h": d.MessagesDay,
		"communities": d.Communities, "users": d.Users,
		"reports_open": d.ReportsOpen, "appeals_open": d.AppealsOpen,
		"level_buckets": d.LevelBuckets,
	})
}
