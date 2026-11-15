package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleCreateReport records a complaint about a message or a user.
func (s *Server) handleCreateReport(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	// 举报体只有三样：目标类型、目标 id、原因。
	var req struct {
		TargetKind string `json:"target_kind"`
		TargetID   string `json:"target_id"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 只认两种目标：一条消息，或者一个人。
	if req.TargetKind != "message" && req.TargetKind != "user" {
		writeErr(w, http.StatusBadRequest, "bad_target", "target_kind must be message or user")
		return
	}
	// 空目标没法处置：连「举报的是哪条」都不知道。
	if req.TargetID == "" {
		writeErr(w, http.StatusBadRequest, "bad_target", "target_id is required")
		return
	}
	id, err := s.DB.CreateReport(r.Context(), c.ID, u.ID, req.TargetKind, req.TargetID, req.Reason)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "report_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "pending"})
}

// handleReports lists the pending reports of one community.
func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	c, ok := s.communityOf(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	if !s.communityAdmin(w, r, c, u.ID) {
		return
	}
	// 只列待决的：已决的留在库里做审计，不占队列。
	list, err := s.DB.ReportsOf(r.Context(), c.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	// 字段名就是线上契约：与其余接口一致。
	out := make([]map[string]any, 0, len(list))
	for _, rep := range list {
		out = append(out, map[string]any{
			"id": rep.ID, "reporter": rep.Reporter, "target_kind": rep.TargetKind,
			"target_id": rep.TargetID, "reason": rep.Reason, "created_at": rep.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": out})
}

// handleDecideReport closes one report and records the action taken.
func (s *Server) handleDecideReport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "report id must be a positive integer")
		return
	}
	// 举报属于某个社群：从举报行反查社群，再用社群管理员这道门。
	u := userFrom(r.Context())
	var req struct {
		Status string `json:"status"`
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 只有两种收尾：处置了，或者驳回。中间态不该出现在对外接口里。
	if req.Status != "actioned" && req.Status != "dismissed" {
		writeErr(w, http.StatusBadRequest, "bad_status", "status must be actioned or dismissed")
		return
	}
	if err := s.DB.DecideReport(r.Context(), id, u.ID, req.Status, req.Action, req.Note); err != nil {
		writeErr(w, http.StatusConflict, "decide_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": req.Status})
}

// handleReportActions returns what was done about one report.
func (s *Server) handleReportActions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "report id must be a positive integer")
		return
	}
	// 处置记录是「为什么」的来源：被处置者看到的就是这几行。
	actions, err := s.DB.ActionsOf(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": actions})
}
