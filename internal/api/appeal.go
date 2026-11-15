package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleEscalateReport moves a report to the platform queue.
func (s *Server) handleEscalateReport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "report id must be a positive integer")
		return
	}
	u := userFrom(r.Context())
	// 上交理由可以不填：平台看到举报原文就能判断。
	var req struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 上交是社群管理员做不了决定时的出口，不是普通成员能按的按钮。
	if err := s.DB.EscalateReport(r.Context(), id, u.ID, req.Note); err != nil {
		writeErr(w, http.StatusConflict, "escalate_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "escalated"})
}

// handleCreateAppeal records an appeal against a moderation decision.
func (s *Server) handleCreateAppeal(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "report id must be a positive integer")
		return
	}
	u := userFrom(r.Context())
	// 申诉理由由被处置者填写：它是平台改判时唯一的一手材料。
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 申诉挂在举报上，不是挂在处置动作上：一条举报只对应一个可申诉的结果。
	appealID, err := s.DB.CreateAppeal(r.Context(), id, u.ID, req.Reason)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "appeal_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": appealID, "status": "pending"})
}

// handleAppeals lists the platform appeal queue. Only a platform admin sees it:
// community admins must not be able to read appeals from other communities.
func (s *Server) handleAppeals(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.AppealsOf(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	// 字段名就是线上契约：与其余接口一致。
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id": a.ID, "report_id": a.ReportID, "user": a.User,
			"reason": a.Reason, "created_at": a.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"appeals": out})
}

// handleDecideAppeal rules on one appeal. Overturning reopens the report as
// dismissed, so the visible outcome follows the ruling.
func (s *Server) handleDecideAppeal(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "appeal id must be a positive integer")
		return
	}
	u := userFrom(r.Context())
	// 裁决只有两种：维持或改判；note 是写给被处置者看的理由。
	var req struct {
		Overturn bool   `json:"overturn"`
		Note     string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 改判会把举报改成 dismissed：可见结果必须跟着裁决走。
	if err := s.DB.DecideAppeal(r.Context(), id, u.ID, req.Overturn, req.Note); err != nil {
		writeErr(w, http.StatusConflict, "decide_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "overturned": req.Overturn})
}

// handleAddBaseline registers one platform-level pattern.
func (s *Server) handleAddBaseline(w http.ResponseWriter, r *http.Request) {
	// 基线是一条模式串：命中它的举报自动上交，社群管理员无权驳回。
	var req struct {
		Pattern string `json:"pattern"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "body is not valid json")
		return
	}
	// 空模式由存储层拒绝：一条空基线会命中所有举报。
	if err := s.DB.AddBaseline(r.Context(), req.Pattern); err != nil {
		writeErr(w, http.StatusBadRequest, "baseline_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// handleBaselines lists every platform-level pattern.
func (s *Server) handleBaselines(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.Baselines(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"baselines": list})
}
