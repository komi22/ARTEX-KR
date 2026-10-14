package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**취약점 ID 구분**: finding_id는 독립적인 취약점 기록의 ID이고 finding_node_id는 탐색 노드의 ID입니다. list_findings, list_task_findings, node_detail, get_task_node_detail의 id는 탐색 노드 ID일 수 있습니다. 반환값의 finding_id를 별도로 확인하세요. get_finding_traffic과 bind_finding_traffic에는 독립적인 finding_id를, update_finding_report의 finding_id 인수에는 finding_node_id를 전달하세요."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\n기본적으로 보고 에이전트가 보고서 작성 전에 트래픽을 확인하고 연결합니다. 보고자는 evidence에 검증 명령, 주요 출력, 이미 확인한 실제 트래픽 ID와 용도를 남겨 보고 에이전트가 실행 기록과 대조할 수 있게 하세요. 연결을 위해 패킷을 추가로 조회할 필요는 없습니다. 즉시 연결하려면 traffic_refs 또는 evidence_hint_id에 확인한 참조를 전달할 수 있습니다. 후자는 이 작업의 힌트에 저장된 구조화된 참조를 읽습니다. 잘못된 참조가 하나라도 있으면 보고 전체가 실패합니다. TCP 또는 패킷이 없는 취약점에는 이 인수가 필요하지 않습니다. 반환되는 finding_id는 독립 기록, finding_node_id는 탐색 노드의 ID입니다."
		case "add_hint", "add_task_hint":
			note = "\n확인된 취약점을 인계할 때 힌트의 traffic_refs에 검증된 트래픽의 ID, 용도, 설명, 순서를 유지하세요. 단일 힌트는 최상위 인수에, 일괄 힌트는 해당 hints 항목에 넣으세요. text에는 트래픽이 입증하는 구체적인 취약점을 적으세요. 기존 참조를 누락하거나 검증되지 않은 후보를 증거로 전달하지 마세요."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**트래픽 증거 인계(선택 사항)**: 기본적으로 취약점이 기록된 뒤 보고 에이전트가 보고서 작성 전에 트래픽을 연결합니다. 보고자는 evidence에 검증 명령, 주요 출력, 확인한 실제 트래픽 ID와 용도를 남기고 작업의 intent_id를 포함해 추적할 수 있게 하세요. 연결만을 위해 패킷을 추가로 조회할 필요는 없습니다. Auto나 Planner가 작업자를 대신해 보고할 때 기존 참조를 버리지 마세요. add_hint와 add_task_hint의 traffic_refs로 인계할 수 있고, report_finding의 traffic_refs 또는 evidence_hint_id로 즉시 연결할 수도 있습니다. TCP이거나 패킷이 없으면 그대로 보고하세요. ID를 추측하거나 보조 패킷을 맞추기 위해 반복 검색하지 마세요."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\n플랫폼 대화에 작업 컨텍스트가 없다면 report_finding을 직접 호출하지 마세요. add_task_hint로 해당 작업에 정보를 인계하고 작업 에이전트가 기록하게 한 뒤 list_task_findings로 결과를 확인하세요."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\n목표 달성을 판정하기 전에 기존 증거의 보고 또는 인계를 마치세요. 텍스트로 취약점을 기록했다는 이유만으로 인계 전에 작업을 종료하거나 작업자를 취소하지 마세요. 패킷이 없으면 기다리거나 억지로 수집할 필요는 없습니다."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**보고 전 트래픽 자동 연결(사용 중)**: 보고서 작성 전에 이번 취약점의 트래픽을 확인하고 연결하세요. report_finding의 반환 JSON이나 get_task_node_detail, list_task_findings에서 finding_id와 finding_node_id를 확인하세요. 취약점 상세, 해당 실행 계획의 기록, 기존 증거를 읽고 보고자가 전달한 실제 ID를 우선 사용하세요. HTTP 검증이며 트래픽 도구를 쓸 수 있다면 traffic_search로 후보를 찾고 traffic_get으로 각 요청과 응답이 실제로 취약점을 입증하는지 확인하세요. 도메인과 시간은 후보를 좁힐 때만 사용하며 소유 관계의 증거가 아닙니다. 확인된 참조를 순서대로 모아 baseline, proof, verification, supporting 중 용도를 정하고 bind_finding_traffic(finding_id, traffic_refs)로 연결하세요. 이번 취약점만 처리하고 취약점을 다시 만들거나 대상을 재탐색하지 마세요. 연결 후 get_finding_traffic으로 새 버전을 읽고 update_finding_report에 실제로 읽은 버전을 evidence_version으로 전달하세요. 이때 update_finding_report의 finding_id 인수에는 finding_node_id를 사용합니다. 이미 연결된 증거를 중복 추가하지 마세요. TCP이거나 패킷이 없거나 도구를 사용할 수 없거나 정확한 일치가 없다면 자동 연결을 건너뛰고 텍스트와 명령 증거로 보고서를 작성하며 이유를 설명하세요. 트래픽 ID를 추측해서 채우지 마세요. 연결에 실패했다면 성공했다고 말하지 말고 기존 증거를 유지하세요."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "선택 사항입니다. 이 힌트의 취약점을 입증하는 실제 트래픽 참조를 확인한 순서대로 넣으세요. 인계 후 report_finding에 evidence_hint_id를 전달하면 참조를 사용할 수 있습니다.", "items": obj(map[string]any{"traffic_id": str("실제 트래픽 ID"), "role": str("baseline / proof / verification / supporting"), "note": str("이 트래픽이 입증하는 내용")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d는 이 작업의 힌트 노드여야 합니다(상속된 힌트는 바인딩에 직접 사용할 수 없습니다)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
