package server

import (
	"fmt"
	"net/http"

	"github.com/Autumn-27/artex/db"
)

// 작업 수준 자산 차단/허용 규칙에 대한 CRUD입니다. 규칙은 task_id로 할당되며 이 작업에만 유효합니다.
// action=차단 차단(테스트 비활성화), action=허용(허용 목록). 실행 결정은 db.EvaluateAssetGate를 참조하세요.

type taskInterceptRuleReq struct {
	Enabled bool   `json:"enabled"`
	Action  string `json:"action"` // block | allow
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
	Note    string `json:"note"`
}

// verifyTaskInterceptRuleReq는 정규화하고 확인합니다. 전역 규칙의 종류/패턴 유효성 검사기를 재사용합니다.
func validateTaskInterceptRuleReq(req *taskInterceptRuleReq) error {
	if req.Action == "" {
		req.Action = "block"
	}
	if req.Action != "block" && req.Action != "allow" {
		return fmt.Errorf("작업은 차단 또는 허용이어야 합니다")
	}
	v := assetInterceptRuleReq{Enabled: req.Enabled, Kind: req.Kind, Pattern: req.Pattern, Note: req.Note}
	if err := validateAssetInterceptRuleReq(&v); err != nil {
		return err
	}
	req.Pattern = v.Pattern // 손질된
	return nil
}

// buildTaskInterceptRules는 태스크 생성 시 입력한 태스크 수준 규칙을 검증하고 이를 db 입력 형식으로 변환합니다.
func buildTaskInterceptRules(reqs []taskInterceptRuleReq) ([]db.TaskInterceptRuleInput, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	out := make([]db.TaskInterceptRuleInput, 0, len(reqs))
	for i := range reqs {
		rq := reqs[i]
		if err := validateTaskInterceptRuleReq(&rq); err != nil {
			return nil, err
		}
		out = append(out, db.TaskInterceptRuleInput{
			Enabled: rq.Enabled,
			Action:  rq.Action,
			Kind:    rq.Kind,
			Pattern: rq.Pattern,
			Note:    rq.Note,
		})
	}
	return out, nil
}

func (s *Server) taskInterceptListRules(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	rules, err := pg.Assets().ListTaskInterceptRules(taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rules == nil {
		rules = []db.AssetInterceptRule{}
	}
	writeJSON(w, 200, map[string]any{"rules": rules})
}

func (s *Server) taskInterceptCreateRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	var req taskInterceptRuleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateTaskInterceptRuleReq(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rule, err := pg.Assets().CreateTaskInterceptRule(taskID, req.Action, req.Kind, req.Pattern, req.Note, req.Enabled)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rule)
}

func (s *Server) taskInterceptUpdateRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	ruleID, ok := pathInt(r, "rid")
	if !ok || ruleID <= 0 {
		writeErr(w, 400, "bad rule id")
		return
	}
	var req taskInterceptRuleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateTaskInterceptRuleReq(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rule, err := pg.Assets().UpdateTaskInterceptRule(taskID, ruleID, req.Action, req.Kind, req.Pattern, req.Note, req.Enabled)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rule)
}

func (s *Server) taskInterceptDeleteRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	ruleID, ok := pathInt(r, "rid")
	if !ok || ruleID <= 0 {
		writeErr(w, 400, "bad rule id")
		return
	}
	deleted, err := pg.Assets().DeleteTaskInterceptRule(taskID, ruleID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

func (s *Server) taskInterceptToggleRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	ruleID, ok := pathInt(r, "rid")
	if !ok || ruleID <= 0 {
		writeErr(w, 400, "bad rule id")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.Assets().ToggleTaskInterceptRule(taskID, ruleID, req.Enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": req.Enabled})
}
