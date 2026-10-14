package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// 목표 관리를 위한 수동 CRUD 인터페이스 개요입니다. 에이전트 측의 set_goals 도구와 동일한 목표 노드 배치를 작성합니다.
// 하지만 입구는 인간이 UI에서 직접 추가, 삭제, 수정할 수 있도록 되어 있습니다. 추가/수정 후 "부활 작업" 논리가 재사용됩니다(admitTask 재개:
// 최종 상태 → 실행 중, 일시정지 해제, 필요 시 대기열), 부활 없이 삭제(제품 결정에 따름). 모든 변경 핸들러는 간다
// startTaskOperation/decInflight, 작업 삭제로 경합 상태를 방지합니다(의도 CRUD와 일치).

// listGoals는 대상이 카드 렌더링을 관리할 수 있도록 이 작업의 모든 목표(text/vulnclass/state가 분해됨)를 반환합니다.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err := t.Store.ListByKind(db.KindGoal, 10000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

// addGoal은 수동으로 목표를 추가합니다. 라이브러리에 드롭(작업 루트 생성 아래에 매달림) → "새 목표"를 기록하여 깨우기를 트리거합니다.
// 플래너 → 작업을 부활시켜 플래너가 새로운 목표를 기반으로 달성되었는지 여부를 다시 판단할 수 있도록 합니다.
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "작업이 삭제되는 중이므로 새 대상을 추가할 수 없습니다")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "대상 콘텐츠는 비워둘 수 없습니다")
		return
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(body.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	id, err := t.Store.AddGoal(payload, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _ := t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // 플래너를 실행하고 깨우려면 "사람들이 새 목표를 추가했습니다:..."를 기억하세요.
	s.reviveTask(t)              // 완료/일시 중지된 작업을 다시 실행 상태로 되돌리고 계속 실행
	node, _ := t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, "대상 쓰기 후 읽기 실패")
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal은 대상 텍스트(및 vulnclass)를 수동으로 수정합니다. 데이터베이스를 변경하고 → "사용자가 대상을 이전에서 새 것으로 수정했습니다"를 기록합니다.
// 깨우기 플래너 실행 → 작업을 부활시켜 플래너가 새로운 목표에 따라 방향을 조정할 수 있도록 합니다.
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "작업을 삭제하는 중이므로 대상을 수정할 수 없습니다")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "대상 콘텐츠는 비워둘 수 없습니다")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "대상이 존재하지 않습니다")
		return
	}
	oldText := goalDTO(node).Text
	if err := t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // 플래너를 실행하고 깨우려면 "그 사람이 목표를 이전에서 새 것으로 수정했습니다"를 기억하십시오.
	s.reviveTask(t)                   // 새로운 추가 사항에 따라 부활 임무가 새로운 목표에 따라 재심사됩니다.
	updated, _ := t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, "대상 업데이트 후 읽기 실패")
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal이 목표를 수동으로 삭제합니다(하드 삭제, 계단식 가장자리/앵커 삭제): 데이터베이스 삭제 → "사용자가 목표 X를 삭제했습니다"라고 기록합니다.
// 그에 따라 나머지 목표를 다시 판단하기 위해 웨이크업 플래너를 트리거합니다. 제품 결정에 따라 [부활 아님] 작업을 삭제합니다.
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "작업을 삭제하는 중이므로 대상을 삭제할 수 없습니다")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "대상이 존재하지 않습니다")
		return
	}
	text := goalDTO(node).Text
	if err := t.Store.DeleteGoal(gid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // 참고 "대상을 삭제한 사람:..."은 플래너를 트리거하고 깨우며(작업을 부활시키지 않음)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
