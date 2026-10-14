package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
	actool "github.com/Autumn-27/norma/tool"
)

func (s *Server) seedFindingWorkflowTools() {
	const hostSearchDescriptionFlag = "finding_workflow_tools_v3_host_search_description"
	if value, _, _ := s.m.pg.GetSetting(hostSearchDescriptionFlag); value != "true" {
		// Only replace the original built-in text. A user-edited description is
		// authoritative and must survive upgrades.
		legacy := "\u67e5\u8be2\u8bb0\u5f55\u4ee3\u7406\u5df2\u6293\u53d6\u7684\u76ee\u6807\u6d41\u91cf\uff08\u5fc5\u987b\u6307\u5b9a host\uff0c\u53ef\u518d\u6309 URL \u5b50\u4e32\u6216\u6b63\u6587\u5173\u952e\u8bcd\u8fc7\u6ee4\uff09\u3002body_contains \u4f1a\u5728\u5df2\u6293\u53d6\u7684\u8bf7\u6c42/\u54cd\u5e94\u5934\u4e0e\u6b63\u6587\u4e2d\u505a\u5168\u6587\u641c\u7d22\uff0c\u652f\u6301\u4efb\u610f\u5b50\u4e32\u548c\u4e2d\u6587\uff08\u81f3\u5c11 3 \u4e2a\u5b57\u7b26\uff09\uff0c\u53ef\u7528\u6765\u627e\u54cd\u5e94\u91cc\u7684\u5bc6\u7801\u3001\u5bc6\u94a5\u3001\u62a5\u9519\u3001\u5185\u7f51\u5730\u5740\u7b49\u3002\u4ec5\u8fd4\u56de\u6781\u8f7b\u91cf\u7d22\u5f15(id/method/url/status/resp_len)\uff0c\u4e0d\u542b\u4efb\u4f55\u54cd\u5e94\u5185\u5bb9\u3002\u9ed8\u8ba4\u53ea\u8fd4\u56de 3 \u6761\u3001\u6bcf\u9875\u6700\u591a 10 \u6761\uff1b\u7ed3\u679c\u591a\u65f6\u7528 page \u7ffb\u9875\uff08page=0 \u8d77\uff09\uff1b\u8981\u770b\u67d0\u6761\u7684\u8bf7\u6c42/\u54cd\u5e94\u539f\u6587\u7528 traffic_get(id)\u3002\u56de\u770b\u5df2\u8bbf\u95ee\u8d44\u6e90\u3001\u627e\u7aef\u70b9\u5148\u7528\u5b83\uff0c\u907f\u514d\u91cd\u590d curl \u540c\u4e00 URL\u3002"
		if _, err := s.m.pg.Exec(`UPDATE tools SET description=$1,updated_at=now() WHERE key='traffic_search' AND system AND description=$2`, traffic.TrafficSearchDescription, legacy); err != nil {
			// Log and leave the flag unset so the next startup retries; do not
			// return, or a transient error here would also skip the reporter
			// migration below — the two are independent.
			log.Printf("[evidence] upgrade traffic_search description: %v", err)
		} else {
			_ = s.m.pg.SetSetting(hostSearchDescriptionFlag, "true")
		}
	}
	const flag = "finding_workflow_tools_v2_reporter"
	if value, _, _ := s.m.pg.GetSetting(flag); value == "true" {
		return
	}
	for _, key := range []string{"report_finding", "add_hint", "add_task_hint"} {
		row, err := s.m.pg.GetTool(key)
		if err != nil {
			log.Printf("[evidence] load %s: %v", key, err)
			return
		}
		if row == nil || !row.System {
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(row.Schema, &schema); err != nil {
			log.Printf("[evidence] invalid schema for %s: %v", key, err)
			return
		}
		if schema == nil {
			log.Printf("[evidence] missing object schema for %s", key)
			return
		}
		props := objectProperty(schema, "properties")
		if key == "report_finding" {
			if _, exists := props["evidence_hint_id"]; !exists {
				props["evidence_hint_id"] = map[string]any{"type": "integer", "description": "선택 사항: 이 작업의 취약점에 해당하는 힌트 ID입니다. 힌트로 저장된 traffic_refs를 읽고 함께 묶고 프롬프트가 없으면 생략"}
			}
		} else {
			if _, exists := props["traffic_refs"]; !exists {
				props["traffic_refs"] = agent.HintTrafficSchema()
			}
			hints := objectProperty(props, "hints")
			if _, exists := hints["type"]; !exists {
				hints["type"] = "array"
			}
			items := objectProperty(hints, "items")
			if _, ok := items["type"]; !ok {
				items["type"] = "object"
			}
			itemProps := objectProperty(items, "properties")
			for name, value := range map[string]any{"text": strParam("안내 내용"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()} {
				if _, exists := itemProps[name]; !exists {
					itemProps[name] = value
				}
			}
		}
		raw, _ := json.Marshal(schema)
		result, err := s.m.pg.Exec(`UPDATE tools SET schema=$2::jsonb,updated_at=now() WHERE key=$1 AND system AND schema=$3::jsonb`, key, string(raw), string(row.Schema))
		if err != nil {
			log.Printf("[evidence] upgrade %s: %v", key, err)
			return
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return
		} // preserve concurrent user edits
	}
	// Upgrade only the original default binding. Customized lists and enabled
	// flags survive; the one-time flag also preserves future user unbinding.
	readers := `["worker","reporter"]`
	for _, key := range []string{"traffic_search", "traffic_get", "traffic_blob"} {
		if _, err := s.m.pg.Exec(`UPDATE tools SET agents=$2::jsonb WHERE key=$1 AND system AND (agents='["worker"]'::jsonb OR (agents @> '["worker","planner","mainagent","auto","pentest"]'::jsonb AND jsonb_array_length(agents)=5))`, key, readers); err != nil {
			return
		}
	}
	if _, err := s.m.pg.Exec(`UPDATE tools SET agents=$1::jsonb WHERE key='get_finding_traffic' AND system AND agents @> '["auto","reporter"]'::jsonb AND jsonb_array_length(agents)=2`, `["auto","reporter","worker","planner","mainagent","pentest"]`); err != nil {
		return
	}
	// Replace the previous code default only; preserve customized binding lists.
	if _, err := s.m.pg.Exec(`UPDATE tools SET agents='["reporter"]'::jsonb WHERE key='bind_finding_traffic' AND system AND agents @> '["worker","planner","mainagent","auto","pentest"]'::jsonb AND jsonb_array_length(agents)=5`); err != nil {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("reporter", []string{"bind_finding_traffic"}); err != nil {
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

func objectProperty(parent map[string]any, key string) map[string]any {
	value, ok := parent[key].(map[string]any)
	if !ok {
		value = map[string]any{}
		parent[key] = value
	}
	return value
}

func (s *Server) agentFindingTrafficAccess(ctx context.Context, id int64, write bool) error {
	if id <= 0 {
		return errors.New("finding_id는 독립적인 취약점 레코드 ID여야 합니다. 검색 노드 ID가 아님")
	}
	f, err := s.m.pg.GetFinding(id)
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("%w: finding_id=%d. 증거 도구는 독립적인 취약점 기록 ID를 사용합니다. list_task_findings 또는 get_task_node_detail의 finding_id 필드에서 확인하세요. id나 finding_node_id는 전달하지 마세요", db.ErrFindingNotFound, id)
	}
	if ri := agent.RunInfoFrom(ctx); ri.TaskID > 0 {
		task := s.m.ResolveTask(strconv.FormatInt(ri.TaskID, 10))
		if task == nil {
			return errors.New("할 일이 존재하지 않습니다")
		}
		_, inherited, allowed := findingProvenanceInTask(task, f.TaskID)
		if !allowed {
			return errors.New("이 취약점은 현재 작업으로 읽을 수 없습니다")
		}
		if write && inherited {
			return errors.New("상속된 취약점의 트래픽 증거는 읽기 전용입니다. 수정하려면 소스 작업으로 이동하세요.")
		}
	}
	return nil
}

func (s *Server) toolBindFindingTraffic() actool.CoreTool {
	return wrTool("bind_finding_traffic", "등록된 취약점에 대해 인증되고 검증된 HTTP 트래픽을 연결합니다. finding_id는 독립적인 취약점 레코드 ID를 사용합니다. 탐사 노드 ID를 전달하지 마세요. 동일한 배치의 모든 참조가 성공하거나 모두 실패합니다. 반복된 참조에는 기존 지침이 포함되지 않습니다. 패치를 적용하면 기존 보고서에 업데이트가 표시됩니다. 패치를 위해 취약점을 다시 검색하거나 재생성하지 마십시오.",
		objSchema(map[string]any{"finding_id": strParam("독립적인 취약점 레코드 ID, list_task_findings / get_task_node_detail의 finding_id 필드에서 읽음"), "traffic_refs": agent.HintTrafficSchema()}, "finding_id", "traffic_refs"),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			if !s.m.pg.GetBool(settingAgentTrafficBinding, false) {
				return actool.Errorf("에이전트의 트래픽 자동 바인딩이 꺼졌습니다. 시스템 설정에서 활성화하거나 페이지를 사용하여 수동으로 바인딩하세요."), nil
			}
			var args struct {
				FindingID json.RawMessage `json:"finding_id"`
				Refs      []db.TrafficRef `json:"traffic_refs"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := parseProfileID(args.FindingID)
			if err := s.agentFindingTrafficAccess(ctx, id, true); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if len(args.Refs) == 0 {
				return actool.Errorf("리바인딩에는 확인된 traffic_refs가 하나 이상 필요합니다. 트래픽이 없으면 이 도구를 호출할 필요가 없습니다."), nil
			}
			list, err := s.evidenceStore().Bind(ctx, id, args.Refs)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(trafficSummary(list))
		})
}
