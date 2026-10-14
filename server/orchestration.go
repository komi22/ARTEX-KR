package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// jsonResult marshals v to a JSON tool result.
func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

//이 문서는 P2 "교차 작업 오케스트레이션 도구 세트"(docs/Benchmark Orchestration §2 P2)를 구현합니다. 호스트 도구는 다음과 같습니다 - 필수
// Manager(모든 작업에 대한 저장소), 엔진(일시 중지) 및 빌드 작업 프로세스에 액세스하여 서버 계층에 상주합니다.
// 읽기 도구는 실행을 위해 "기존 작업별 도구"를 대상 작업 저장소로 리디렉션합니다(임시 도구 세트 생성)
// 해당 도구를 호출하여) 정확히 동일한 논리를 재사용합니다. 컨트롤 클래스(생성/일시 중지)는 Manager/Engine을 직접 호출합니다.
// 트래픽 도구와 같은 도구 테이블에 시드되고 에이전트에 의해 바인딩됩니다(오케스트레이션 에이전트에 바인딩된 경우에만 표시됨).

// hostTools is the runtime host-tool provider fed to ToolAugment: traffic tools
// (gated by capture) + cross-task orchestration tools + user-defined custom tools.
// The second return is the names of custom tools flagged `deferred` (schema
// withheld, routed via SearchExtraTools/ExecuteExtraTool). Per-agent binding still
// decides who actually sees any of them.
//
//nolint:unused // used as the hostTools provider in wireAgentAugment
func (s *Server) hostTools() ([]actool.CoreTool, map[string][]string) {
	tools := append(s.m.HostTools(), s.orchestrationTools()...)
	tools = append(tools, s.findingRetestTools()...)
	tools = append(tools, s.platformTools()...) // 플랫폼 운영 도구(자동 사용을 위한 스킬/도구/MCP 수정)
	custom, err := s.customTools()
	if err != nil {
		log.Printf("[custom-tool] 로드 실패: %v", err)
		return tools, nil
	}
	tools = append(tools, custom...)
	// deferred custom tools → name -> its bound agent keys. ToolAugment turns a
	// name into a deferred entry only for agents it's actually bound to (so we don't
	// advertise a tool the per-agent binding will drop from the callable set).
	deferred := map[string][]string{}
	rows, _ := s.m.pg.ListCustomTools()
	for _, t := range rows {
		if t.Deferred && t.Enabled {
			deferred[t.Key] = t.Agents
		}
	}
	return tools, deferred
}

// orchestrationTools returns the cross-task tool set. Bound per-agent via the
// tools table (default: no binding — opt-in for orchestration agents).
func (s *Server) orchestrationTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolListTasks(),
		s.toolListLLMProfiles(),
		s.toolSpawnTask(),
		s.toolPauseTask(),
		s.toolGetTaskGraph(),
		s.toolListTaskFindings(),
		s.toolAddHint(),
		s.toolGetWorkerTrace(),
		s.toolListWorkerTraces(),
		s.toolSearchWorkerTraces(),
		s.toolGetTaskNodeDetail(),
		s.toolUpdateFindingReport(),
		s.toolGetFindingTraffic(),
		s.toolBindFindingTraffic(),
	}
}

// --- schema helpers ---

func strParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// parseProfileID reads an LLM profile id from a tool arg that may arrive as a JSON
// number (5) or a numeric string ("5"); returns 0 when absent/unparseable.
func parseProfileID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		return v
	}
	return 0
}

func objSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

func roTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func wrTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// delegateToTask resolves the `task_id` in the input, builds a ToolSet bound to
// that task's store, strips task_id, and calls the chosen per-task tool — so the
// cross-task read reuses the exact in-task logic against another task.
func (s *Server) delegateToTask(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	var head struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(in, &head)
	if strings.TrimSpace(head.TaskID) == "" {
		return actool.Errorf("task_id가 필요합니다"), nil
	}
	t, ok := s.m.Task(head.TaskID)
	if !ok {
		return actool.Errorf("할 일이 존재하지 않습니다: " + head.TaskID), nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(in, &m)
	delete(m, "task_id")
	inner, _ := json.Marshal(m)
	tsx := agent.NewToolSet(t.Store, "orchestrator")
	if s.m.Assets() != nil {
		tsx.SetAssetStore(s.m.Assets(), s.m.Assets().Companies())
	}
	tsx.SetNotify(t.Notify)         // 범용 깨우기(전용 콜백 없이 쓰기 작업이 진행됨, 읽기 도구는 작동하지 않음)
	tsx.SetNotifyHint(t.NotifyHint) // add_hint → "사람들이 N개의 전략적 힌트를 추가했습니다:..."라는 메시지를 기억하고 플래너를 깨우세요
	return pick(tsx).Call(ctx, inner, nil)
}

// --- tools ---

func (s *Server) toolListTasks() actool.CoreTool {
	return roTool("list_tasks",
		"모든 작업(id/description/goal/status/running time/parent task/LLM 구성)을 나열하고, 에이전트를 조정하고 이를 사용하여 전체 상황을 파악하고, 어떤 작업이 너무 오래 정체되어 있는지, 각각에 어떤 LLM을 사용해야 하는지 확인하세요. 실행 시간: 실행 중 = 생성됨 → 현재, 최종 상태 = 생성됨 → 마지막 활동(초). llm_profile: 작업 플래너/작업자가 사용하는 구성 이름, (활성화 구성) = 전역 활성화를 따릅니다.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			lastAct, _ := s.m.PG().LastActivityAll()
			// id -> name to resolve each task's pinned LLM profile.
			profName := map[int64]string{}
			if profs, err := s.m.pg.ListProfiles(); err == nil {
				for _, p := range profs {
					profName[p.ID] = p.Name
				}
			}
			out := make([]map[string]any, 0)
			for _, t := range s.m.List() {
				status := s.deriveTaskStatus(t)
				end := lastAct[t.ExpID]
				if live := s.engine.LastActivity(t.ID); live > end {
					end = live
				}
				dur := int64(0)
				if status == "running" {
					dur = time.Now().Unix() - t.CreatedAt
				} else if end > t.CreatedAt {
					dur = end - t.CreatedAt
				}
				row := map[string]any{"id": t.ID, "description": t.Description, "goal": t.Goal, "status": status, "run_seconds": dur}
				if t.ParentRef != "" {
					row["parent_ref"] = t.ParentRef
				}
				llmState := t.llmStateSnapshot()
				if llmState.ProfileID == nil {
					row["llm_profile"] = "(활성화 구성)"
				} else if n, ok := profName[*llmState.ProfileID]; ok {
					row["llm_profile"] = n
				} else {
					row["llm_profile"] = fmt.Sprintf("#%d(삭제됨)", *llmState.ProfileID)
				}
				out = append(out, row)
			}
			return jsonResult(out)
		})
}

// toolListLLMProfiles lists the available LLM profiles (name/model/active) so an
// orchestration agent can pick one for spawn_task's llm_profile. Never leaks keys.
func (s *Server) toolListLLMProfiles() actool.CoreTool {
	return roTool("list_llm_profiles",
		"사용 가능한 LLM 프로필(프로필)을 나열합니다: ID, 이름, 모델, 형식, 현재 활성 프로필인지 여부. ID를 사용하여 spawn_task의 llm_profile_id 매개변수에 대한 하위 작업별 LLM(예: 정찰을 위한 저렴한 모델 및 활용을 위한 강력한 모델)을 지정합니다. API 키는 포함되지 않습니다.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			profs, err := s.m.pg.ListProfiles()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := make([]map[string]any, 0, len(profs))
			for _, p := range profs {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "model": p.Model, "format": p.Format, "is_active": p.IsDefault,
				})
			}
			return jsonResult(map[string]any{"profiles": out})
		})
}

func (s *Server) toolSpawnTask() actool.CoreTool {
	return wrTool("spawn_task",
		"새 하위 작업을 만들고 탐색 엔진을 시작하여 task_id를 반환합니다. 하나의 항목(예: 질문/목표)을 독립적인 작업에 할당하는 데 사용됩니다. parent_ref 선택사항: 현재 오케스트레이션 연결의 상위 작업 ID를 입력하여 상위-하위 연결을 만듭니다.",
		objSchema(map[string]any{
			"description":            strParam("작업 설명(짧은 제목)"),
			"goal":                   strParam("과제 목표(달성해야 할 것)"),
			"parent_ref":             strParam("선택사항: 상위 작업 ID(상위-하위 연결)"),
			"source_task_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": fmt.Sprintf("선택사항: 상속된 소스 작업 ID의 읽기 전용 목록(최대 %d). 하위 작업에는 이러한 작업이 시작점으로 입증된 자산/결론에 대한 읽기 전용 참조가 있을 수 있습니다. 이것은 parent_ref의 순수 부모-자식 포인터와는 달리 내용 상속입니다.", db.MaxTaskSourceCount)},
			"llm_profile_id":         map[string]any{"type": "integer", "description": "선택 사항: 이 하위 작업 계획자/작업자가 사용하는 LLM 구성 ID를 지정합니다(list_llm_profiles 참조). 상위 작업을 상속한 다음 전역 활성화 구성으로 롤백하려면 비워 두세요."},
			"timeout_seconds":        map[string]any{"type": "integer", "description": "선택사항: 작업 수준 시간 제한(초). 해당 지점에 도달한 후 우아한 엔딩을 트리거하고 타임아웃 최종 상태로 들어갑니다. 비워두거나 0 = 시간 제한 없음"},
			"plan_heartbeat_seconds": map[string]any{"type": "integer", "description": "선택 사항: 플래너 하트비트 트리거 간격(초). 이 값은 마지막 계획 라운드 종료/작업 시작부터 도달하며 해당 기간 동안 트리거가 없습니다. → 계획 라운드 트리거(교착 상태 + 비행 작업자를 감독하기 위해 깨어남). 비워두거나 0 = 기본값 600(10분);"},
			"seed_first_intent":      map[string]any{"type": "boolean", "description": "선택사항: 간단한 작업에 대해 활성화할 수 있습니다. 생성 시 시드 의도(내용 = 설명 + 목표)가 작업자에게 직접 전송되어 플래너의 첫 번째 라운드를 기다리지 않고 테스트 실행을 시작합니다. 기본값은 false입니다(먼저 표준 계획을 따른 다음 실행)."},
		}, "description", "goal"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Description          string          `json:"description"`
				Goal                 string          `json:"goal"`
				ParentRef            string          `json:"parent_ref"`
				SourceTaskIDs        []string        `json:"source_task_ids"`
				LLMProfileID         json.RawMessage `json:"llm_profile_id"`
				TimeoutSeconds       int             `json:"timeout_seconds"`
				PlanHeartbeatSeconds int             `json:"plan_heartbeat_seconds"`
				SeedFirstIntent      bool            `json:"seed_first_intent"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Description) == "" {
				a.Description = "이름이 지정되지 않은 작업"
			}
			if strings.TrimSpace(a.Goal) == "" {
				return actool.Errorf("목표는 필수입니다"), nil
			}
			if a.TimeoutSeconds < 0 {
				a.TimeoutSeconds = 0
			}
			// 읽기 전용 상속 소스 작업: 개수 상한 + 각 ID의 유효성/중복 제거/존재, 확인 규칙은 HTTP 생성 작업과 일치합니다.
			if len(a.SourceTaskIDs) > db.MaxTaskSourceCount {
				return actool.Errorf(fmt.Sprintf("최대 %d개의 관련 작업을 선택하세요", db.MaxTaskSourceCount)), nil
			}
			sourceIDs := make([]int64, 0, len(a.SourceTaskIDs))
			seenSources := map[int64]bool{}
			for _, raw := range a.SourceTaskIDs {
				id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if err != nil || id <= 0 || seenSources[id] {
					return actool.Errorf("연결된 작업 ID가 잘못되었거나 중복되었습니다."), nil
				}
				if _, ok := s.m.Task(strconv.FormatInt(id, 10)); !ok {
					return actool.Errorf(fmt.Sprintf("연결된 작업 #%d이(가) 존재하지 않습니다", id)), nil
				}
				seenSources[id] = true
				sourceIDs = append(sourceIDs, id)
			}
			// LLM profile resolution: explicit id > inherit parent's pin > active(nil).
			var pin *int64
			if id := parseProfileID(a.LLMProfileID); id > 0 {
				if _, ok := s.loadProfileConfig(id); !ok {
					return actool.Errorf(fmt.Sprintf("LLM 구성 #%d이(가) 존재하지 않거나 API 키가 설정되지 않았습니다", id)), nil
				}
				pin = &id
			} else if a.ParentRef != "" {
				if pt, ok := s.m.Task(a.ParentRef); ok {
					pin = pt.LLMProfileID
				}
			}
			var llmIDs []int64
			if pin != nil {
				llmIDs = []int64{*pin}
			}
			t, err := s.m.CreateTaskWithOptions(a.Description, a.Goal, db.TaskCreateOptions{
				SourceTaskIDs:        sourceIDs,
				LLMProfileIDs:        llmIDs,
				TimeoutSeconds:       a.TimeoutSeconds,
				PlanHeartbeatSeconds: a.PlanHeartbeatSeconds,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.ParentRef != "" {
				t.ParentRef = a.ParentRef
				if id, e := strconv.ParseInt(t.ID, 10, 64); e == nil {
					_ = s.m.PG().SetParentRef(id, a.ParentRef)
				}
			}
			// 공유된 생성 후 프로세스는 HTTP 생성 작업(server.go createTask)과 동일한 launchTask 섹션을 재사용합니다.
			// 시드 + 백그라운드에서 눈에 띄게 목표 분해 수행(0/LLM 단계/목표를 하나씩 수행) + 엔진.실행.
			// Seed_first_intent의 기본값은 false입니다(표준이 먼저 계획된 후 실행됨). 간단한 작업을 활성화하여 작업 테스트를 직접 제공할 수 있습니다.
			s.launchTask(t, a.Description+" "+a.Goal, a.SeedFirstIntent)
			return actool.Text(fmt.Sprintf("task created: %s", t.ID)), nil
		})
}

func (s *Server) toolPauseTask() actool.CoreTool {
	return wrTool("pause_task", "지정된 작업을 일시 중지합니다(플래너/작업자 루프 중지).",
		objSchema(map[string]any{"task_id": strParam("일시중지할 작업 ID")}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(in, &a)
			t, ok := s.m.Task(a.TaskID)
			if !ok {
				return actool.Errorf("할 일이 존재하지 않습니다: " + a.TaskID), nil
			}
			if _, err := s.applyTaskControlWithCause(t, "pause", agent.AbortPausedByOrchestrator); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("task paused: " + a.TaskID), nil
		})
}

func (s *Server) toolGetTaskGraph() actool.CoreTool {
	return roTool("get_task_graph", "지정된 작업의 탐색 그래프 개요(graph_overview와 동일: 자산 수/프론티어/디스커버리/커버리지 등)를 읽고 task_id로 작업을 지정합니다.",
		objSchema(map[string]any{"task_id": strParam("작업 ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GraphOverviewTool)
		})
}

func (s *Server) toolListTaskFindings() actool.CoreTool {
	return roTool("list_task_findings", "지정된 작업(플래그/PoC 포함, 각각 id/task_id/intent_id/vulnclass/severity/summary/status 포함)의 확인된 취약점을 읽고 task_id로 작업을 지정합니다.",
		objSchema(map[string]any{"task_id": strParam("작업 ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListFindingsTool)
		})
}

func (s *Server) toolAddHint() actool.CoreTool {
	return wrTool("add_task_hint", "지정된 작업에 전략적 힌트를 삽입합니다(작업 기획자가 다음 라운드에서 의도를 생성할 때 이를 읽습니다).\n"+
		"★일괄 우선순위: 힌트 배열에 여러 개의 힌트를 넣고 한 번에 제출합니다(힌트와 동일한 길이와 순서로 ids 배열을 반환하고 실패한 항목 ID=0). 단일 팁의 경우 힌트가 생략되고 최상위 텍스트로 직접 전송됩니다.",
		objSchema(map[string]any{
			"task_id":      strParam("작업 ID"),
			"hints":        map[string]any{"type": "array", "description": "[이것을 먼저 사용하세요] 프롬프트 배열, 각 요소 필드는 최상위 수준(text/asset_ids/traffic_refs)과 동일합니다.", "items": objSchema(map[string]any{"text": strParam("안내 내용"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()})},
			"text":         strParam("[싱글] 프롬프트 내용"),
			"traffic_refs": agent.HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "고정된 asset_ids(선택 사항, 0/1/다중, 이 작업 내의 asset_ids)"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).AddHintTool)
		})
}

func (s *Server) toolGetWorkerTrace() actool.CoreTool {
	return roTool("get_task_worker_trace",
		"지정된 작업에서 특정 작업(인텐트)의 실행 프로세스를 살펴보세요. get_task_worker_trace(task_id,intent_id) 단계 요약을 확인하세요. 그런 다음 step_ids=[...] 를 추가하여 해당 단계의 전체 내용을 가져옵니다(한 번에 최대 5개, 다중 전송의 경우 처음 5개만 반환됨).",
		objSchema(map[string]any{
			"task_id":   strParam("작업 ID"),
			"intent_id": map[string]any{"type": "integer", "description": "인텐트 ID(이 작업의 작업)"},
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "선택사항: 전체 콘텐츠를 검색하기 위한 단계 ID(한 번에 최대 5개, 다중 패스의 경우 처음 5개만 반환되고 나머지는 omitted_step_ids에 나열됨)"},
		}, "task_id", "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GetWorkerTraceTool)
		})
}

func (s *Server) toolListWorkerTraces() actool.CoreTool {
	return roTool("list_task_worker_traces", "지정된 작업에서 실행된 작업(의도) + 살펴볼 가치가 있는 작업을 찾는 데 사용되는 각각의 단계 수를 나열합니다(get_task_worker_trace 재사용).",
		objSchema(map[string]any{"task_id": strParam("작업 ID")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListWorkerTracesTool)
		})
}

func (s *Server) toolSearchWorkerTraces() actool.CoreTool {
	return roTool("search_task_worker_traces", "지정된 작업의 모든 작업 실행 프로세스를 키워드로 검색합니다(히트 단계 요약 반환 +intent_id).",
		objSchema(map[string]any{"task_id": strParam("작업 ID"), "q": strParam("검색 키워드")}, "task_id", "q"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).SearchWorkerTracesTool)
		})
}

func (s *Server) toolGetTaskNodeDetail() actool.CoreTool {
	return roTool("get_task_node_detail",
		"지정된 작업(결과/사실/의도/목표: 요약 + 세부정보/증거/PoC)에서 탐색 그래프 노드의 전체 내용을 읽습니다. id는 탐색 노드 ID입니다(예: report_finding에서 반환된 ID 또는 list_task_findings의 ID). 취약점 보고서를 작성하기 전에 이를 사용하여 취약점에 대한 완전한 증거를 얻으십시오.",
		objSchema(map[string]any{
			"task_id": strParam("작업 ID"),
			"id":      map[string]any{"type": "integer", "description": "탐색 그래프 노드 ID(비asset_ids)"},
		}, "task_id", "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).NodeDetailTool)
		})
}

// toolUpdateFindingReport writes/overwrites a finding's detailed Markdown report.
// finding_id is the id report_finding returned ("finding recorded: <id>", the
// finding node id). The write (SetFindingReportByNodeID) is keyed by node_id and
// task-agnostic, so this host tool needs no task_id / exploration store.
func (s *Server) toolUpdateFindingReport() actool.CoreTool {
	return wrTool("update_finding_report",
		"등록된 취약점에 대한 [상세 보고서]를 작성/업데이트합니다(마크다운의 전체 텍스트, 이전 내용으로 전체 단락 포함). finding_id에는 report_finding이 반환한 \"finding recorded: <id>\"의 탐색 노드 ID를 입력합니다. 보고서 권장 사항에는 취약점 개요, 영향 및 피해, 재현 단계, 증거/PoC 및 수리 제안이 포함됩니다.",
		objSchema(map[string]any{
			"finding_id":       map[string]any{"type": "integer", "description": "대상 취약점 ID(report_finding에서 반환된 ID)"},
			"report":           strParam("상세 보고서 전문, 마크다운 형식"),
			"evidence_version": map[string]any{"type": "integer", "description": "get_finding_traffic에서 반환된 증거 버전입니다. 보고서가 새로운 증거 변경 사항을 다루지 못하도록 방지하는 데 사용됩니다."},
		}, "finding_id", "report"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				EvidenceVersion *int64          `json:"evidence_version"`
				FindingID       json.RawMessage `json:"finding_id"`
				Report          string          `json:"report"`
			}
			_ = json.Unmarshal(in, &a)
			nodeID := parseProfileID(a.FindingID) //"숫자 또는 숫자 문자열" 구문 분석 재사용
			if nodeID <= 0 {
				return actool.Errorf("finding_id가 잘못되었습니다"), nil
			}
			n, err := s.m.pg.SetFindingReportVersionByNodeID(ctx, nodeID, a.Report, a.EvidenceVersion)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == 0 {
				return actool.Errorf(fmt.Sprintf("finding_id=%d에 해당하는 취약점 기록을 찾을 수 없습니다(report_finding을 사용하여 먼저 등록하세요)", nodeID)), nil
			}
			return actool.Text(fmt.Sprintf("finding %d report updated (%d chars)", nodeID, len(a.Report))), nil
		})
}

// deriveTaskStatus mirrors listTasks' status derivation for the list_tasks tool.
func (s *Server) deriveTaskStatus(t *Task) string {
	lifecycle := t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	}
	return "created"
}

// orchestrationToolSeeds seeds the cross-task tools into the tools table so they
// are bindable per-agent (default: bound to nobody — opt-in for orchestration
// agents). First-insert only, like the traffic seeds.
func (s *Server) seedOrchestrationTools() {
	// task-op + 플랫폼 도구는 기본적으로 내장된 자동 에이전트에 바인딩됩니다.
	// 운영 플랫폼). SeedTool을 처음 삽입하면 적용됩니다. 이전 라이브러리에 시드된 행은 SeedAutoDefaultBindings로 보완됩니다.
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range s.orchestrationTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bindings := autoAgents
		if t.Name() == "bind_finding_traffic" {
			bindings = json.RawMessage(`["reporter"]`)
		}
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, bindings)
	}
	for _, t := range s.platformTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, autoAgents)
	}
	s.refreshBuiltinToolSchemas()
	s.seedAutoDefaultBindings()
	s.seedPlannerDefaultBindings()
	s.seedPlannerListAssetsBinding()
	s.seedCompanyScopeRebind()
	s.seedWorkerReadToolsUnbind() // list_facts/list_companies/list_worker_traces 기본적으로 작업자로부터 바인딩 해제(일회성)
	s.seedWorkerReadbackRebind()  // 이전 마이그레이션의 실수로 삭제된 문제 수정: search_all_worker_traces/get_worker_trace/node_detail을 작업자에 다시 바인딩(일회성)
	s.seedAutoReportFindingBinding()
	s.unbindGoalMetDefault()
	s.reseedGoalsPrompt()             // "펌핑 작업 제약" 단계에 목표 프롬프트 추가 → 이전 라이브러리에 새로운 기본 버전 추가(1회)
	s.reseedMainAgentPrompt()         // Mainagent 프롬프트 추가 "add_intent가 목표 달성 후 목표 생성 여부를 묻습니다."(1회)
	s.reseedPlannerPrompt()           // 플래너 프롬프트: "의도 0" 근거 다시 작성 + 정량적 승인 확인 추가(1회)
	s.reseedWorkerPrompt()            // 작업자 프롬프트: 부정적인 결론 증거 임계값 추가(1회)
	s.seedReporterAgent()             // 사전 설정된 "보고서 작성" 에이전트 + 도구 바인딩 + 검색 트리거(1회)
	s.upgradeReporterTriggerMessage() // 기존 데이터베이스 마이그레이션: 보고자가 증거_버전을 반환하도록 허용(1회)
	s.seedFindingTrafficTools()       // 선택적 증거 매개변수 및 읽기 전용 증거 도구를 추가하고 사용자 구성을 유지합니다.
	s.seedFindingWorkflowTools()
	//참고: pentest의 기본 도구 바인딩은 마이그레이션할 필요가 없습니다. - BuildToolSeeds는
	// list_assets/insert_assets/report_finding/list_findings/list_companies와 함께
	// 함께 시드 침투 테스트를 하십시오(프로젝트에는 아직 이전 라이브러리가 없으며 마이그레이션되지 않습니다).
}

// refreshBuiltinToolSchemas propagates code schema/description changes on the
// orchestration + platform tools into already-seeded rows ONCE per version flag —
// SeedTool은 최초 삽입 전용이므로 새 매개변수(예: llm_profile의 generate_task)는 절대
// reaches an old DB otherwise. Preserves each tool's agent binding + enabled flag.
// Bump the flag whenever these tools' schemas/descriptions change in code.
func (s *Server) refreshBuiltinToolSchemas() {
	const flag = "tool_schema_refresh_v7_list_facts_paging"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tools := append(s.orchestrationTools(), s.platformTools()...)
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err != nil {
			log.Printf("[tools] refresh %s schema failed: %v", t.Name(), err)
		}
	}
	// 동시에 일부 기본 제공 에이전트 도구가 코드 기본값으로 새로 고쳐집니다.
	//   - goal_met: 이전 라이브러리 시드에 대한 설명은 "이 계획 라운드 종료"로 오해를 불러일으킬 수 있으며, 이는 계획자가 이를 다음과 같이 간주하게 만듭니다.
	//     '빈 라운드 종료' 방식과 실행이 시작되자마자 전체 미션이 완료됐다고 오판하는 방식.
	//   - insert_assets: 관련 입력 매개변수 추가(자산이 현재 작업과 관련되어 있는지 표시하고 적용 범위 입력 여부를 결정),
	//     SeedTool은 이전 라이브러리의 스키마가 시드된 경우에만 먼저 삽입됩니다. 그렇지 않으면 이 새 매개변수가 수신되지 않습니다.
	//   - list_facts: 페이징으로 변경, 제한/이전/q 입력 매개변수 추가; 이전 라이브러리는 빈 스키마로 시드되었습니다. 그렇지 않은 경우
	//     공구 관리 페이지에 "매개변수 없음"이 표시되고 모델은 이러한 매개변수 설명을 얻을 수 없습니다.
	refreshBuiltin := map[string]bool{"goal_met": true, "insert_assets": true, "list_facts": true}
	for _, sd := range agent.BuiltinToolSeeds() {
		if !refreshBuiltin[sd.Key] {
			continue
		}
		schema, _ := json.Marshal(sd.Schema)
		if err := s.m.pg.RefreshToolDefaults(sd.Key, sd.Desc, schema); err != nil {
			log.Printf("[tools] refresh %s desc failed: %v", sd.Key, err)
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
	log.Printf("[tools] 코드 기본값에 대한 오케스트레이션/플랫폼 도구 스키마 새로 고침(일회성)")
}

// unbindGoalMetDefault removes goal_met's default "planner" binding ONCE (guarded by
// a settings flag), so existing DBs match the new default of NO agent. goal_met bypasses
// per-goal prove_goal to declare the whole task done — powerful/risky and redundant with
// the prove_goal→auto-complete path — so it ships unbound; users can re-bind it per agent
// in the UI. A user's own binding to another agent is untouched (we only strip planner).
func (s *Server) unbindGoalMetDefault() {
	const flag = "goal_met_unbind_default_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("planner", "goal_met"); err != nil {
		log.Printf("[tools] goal_met가 플래너 바인딩을 해제하지 못했습니다: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// reseedGoalsPrompt는 목표 목표 디스어셈블러의 프롬프트를 [현재 코드 기본값]으로 새로 고칩니다. 기본 텍스트가 추가되기 때문입니다.
// "먼저 작업 제약 조건(set_constraints)을 추출한 다음 대상을 분할합니다" 단계, SeedPromptIfEmpty는 먼저 이전 라이브러리에만 삽입됩니다.
// 기존 버전 1에서는 이 단계를 받을 수 없습니다. 여기 [새 버전 추가]에서 버전 관리를 사용하고 해당 버전으로 전환합니다(ResetPromptToDefault).
// 이전 버전은 여전히 기록에 유지됩니다. 사용자가 이를 사용자 정의한 경우 버전 기록에서 검색할 수 있습니다. 플래그 가드 설정 → 한 번만 수행하십시오.
// 앞으로는 이 플래그를 범프하는 것이 기본값이 될 것입니다. 새로운 라이브러리는 처리할 필요가 없습니다(기본적으로 SeedPromptIfEmpty가 시드되었습니다).
func (s *Server) reseedGoalsPrompt() {
	const flag = "goals_prompt_constraint_step_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.
	a, err := s.m.pg.GetAgentByKey("goals")
	if err != nil || a == nil {
		return // 새 라이브러리가 아직 에이전트 라인을 생성하지 않은 경우, seedPrompts는 이 마이그레이션 없이 최신 기본값을 직접 시드합니다.
	}
	tmpl := agent.BuiltinPromptSeeds()["goals"]
	if tmpl == "" {
		return
	}
	// 새 라이브러리 SeedPrompts가 시드되었으며 최신 기본값 → 현재 버전이 코드 기본값과 동일하므로 중복 버전을 추가할 필요가 없습니다.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] 목표 프롬프트 새로 고침이 기본적으로 실패함: %v", err)
		return
	}
	log.Printf("[prompts] 목표 프롬프트가 새로운 기본 버전으로 추가되었습니다(펌핑 작동 제약 단계 추가, 일회성)")
}

// reseedMainAgentPrompt 메인 에이전트 프롬프트를 [현재 코드 기본값]으로 새로 고칩니다. 기본 텍스트에 "Target All"이 추가되었습니다.
// add_intent 직접투자의향 달성 후 공식대상으로 등록할지 여부를 해당자에게 이 안내에 문의하고 SeedPromptIfEmpty가 먼저 삽입됩니다.
// 단, 이전 라이브러리의 기존 버전은 받을 수 없습니다. 버전 관리 [새 버전 추가]를 사용하고 전환(ResetPromptToDefault)하면 이전 버전이 여전히 유지됩니다.
// 역사 속에 남아있습니다. 사용자가 이를 사용자 정의한 경우 버전 기록에서 검색할 수 있습니다. 설정 플래그 가드 → 이 작업은 한 번만 수행하십시오. 새로운 라이브러리를 처리할 필요가 없습니다.
// (SeedPromptIfEmpty는 최신 기본값을 시드했습니다). GoalsPrompt를 다시 시드하는 것과 완전히 동형입니다.
func (s *Server) reseedMainAgentPrompt() {
	const flag = "mainagent_prompt_goalless_intent_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.
	a, err := s.m.pg.GetAgentByKey("mainagent")
	if err != nil || a == nil {
		return // 새 라이브러리가 아직 에이전트 라인을 생성하지 않은 경우, seedPrompts는 이 마이그레이션 없이 최신 기본값을 직접 시드합니다.
	}
	tmpl := agent.BuiltinPromptSeeds()["mainagent"]
	if tmpl == "" {
		return
	}
	// 새 라이브러리 SeedPrompts가 시드되었으며 최신 기본값 → 현재 버전이 코드 기본값과 동일하므로 중복 버전을 추가할 필요가 없습니다.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[프롬프트] Mainagent 프롬프트 새로 고침이 기본적으로 실패했습니다: %v", err)
		return
	}
	log.Printf("[프롬프트] 메인에이전트 프롬프트가 새로운 기본 버전으로 추가되었습니다(목표 달성 후 목표 생성을 요청하는 일회성 추가)")
}

// reseedPlannerPrompt 플래너 프롬프트를 [현재 코드 기본값]으로 새로 고칩니다. 기본 텍스트가 간소화되고 재구성되었으며 "제한"이 다음으로 다운그레이드되었습니다.
// 중복된 내용만 제거하고, "보도보다 깊이가 우선한다", "하드 바운더리: 목표가 달성되지 않고 실행할 의도가 없으면 목표를 생성해야 한다"는 부정적인 결론에 대한 검토에 한계가 추가됩니다.
// 기본값이 크게 변경될 때마다 아래 플래그(현재 v2)를 올려 기존 이전 라이브러리를 다시 새로 고칠 수 있습니다. SeedPromptIfEmpty는 처음에만 삽입됩니다. 기존 라이브러리의 기존 버전을 받을 수 없으므로 버전 관리를 사용합니다.
// [새 버전을 추가]하고 해당 버전으로 전환합니다(ResetPromptToDefault). 이전 버전은 여전히 ​​기록에 유지됩니다. 사용자가 이를 사용자 정의한 경우 버전에서 녹음할 수 있습니다.
// 검색합니다. 설정 플래그 가드 → 이 작업은 한 번만 수행하십시오. 새로운 라이브러리는 처리할 필요가 없습니다(기본적으로 SeedPromptIfEmpty가 시드되었습니다). 와
// reseedGoalsPrompt는 완전히 동형입니다.
func (s *Server) reseedPlannerPrompt() {
	const flag = "planner_prompt_compact_realistic_v2"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.
	a, err := s.m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		return // 새 라이브러리가 아직 에이전트 라인을 생성하지 않은 경우, seedPrompts는 이 마이그레이션 없이 최신 기본값을 직접 시드합니다.
	}
	tmpl := agent.BuiltinPromptSeeds()["planner"]
	if tmpl == "" {
		return
	}
	// 새 라이브러리 SeedPrompts가 시드되었으며 최신 기본값 → 현재 버전이 코드 기본값과 동일하므로 중복 버전을 추가할 필요가 없습니다.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[프롬프트] 플래너 프롬프트를 새 단어로 새로 고치는 것이 기본적으로 실패합니다: %v", err)
		return
	}
	log.Printf("[프롬프트] 플래너 프롬프트에 새로운 기본 버전이 추가되었습니다(단순화된 재구성 + 제한된 다운그레이드 및 중복 제거 + 깊이 우선 + 부정적인 검토 상한, 일회성)")
}

// reseedWorkerPrompt는 작업자 프롬프트를 [현재 코드 기본값]으로 새로 고칩니다. 기본 텍스트 Record_fact 섹션은 "부정적 결론"을 삭제합니다.
// "관찰 + 잠정적 읽기"라는 전체 문장을 작성하고 "원래 의도와 수단이 소진되었는지 여부"에서 자신감(관찰/추론)을 분리합니다(이는 기획자를 오도하기 쉽습니다).
// 동시에, 사실 배열은 "서로 완전히 독립적이고 병합될 수 없는" 몇 가지 예외로 강화됩니다. 기존의 이전 라이브러리를 새로 고치려면 플래그를 v3으로 범프하세요.
// SeedPromptIfEmpty는 처음에만 삽입됩니다. 기존 라이브러리의 기존 버전을 받을 수 없으므로 버전 관리 [새 버전 추가]를 이용하여 잘라냅니다. 이전 버전은 기록에 계속 유지되며 검색할 수 있습니다.
// 설정 플래그 가드 → 이 작업은 한 번만 수행하십시오. 새로운 라이브러리에는 처리가 필요하지 않습니다. GoalsPrompt를 다시 시드하는 것과 완전히 동형입니다.
func (s *Server) reseedWorkerPrompt() {
	const flag = "worker_prompt_compact_v4"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.
	a, err := s.m.pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		return // 새 라이브러리가 아직 에이전트 라인을 생성하지 않은 경우, seedPrompts는 이 마이그레이션 없이 최신 기본값을 직접 시드합니다.
	}
	tmpl := agent.BuiltinPromptSeeds()["worker"]
	if tmpl == "" {
		return
	}
	// 새 라이브러리 SeedPrompts가 시드되었으며 최신 기본값 → 현재 버전이 코드 기본값과 동일하므로 중복 버전을 추가할 필요가 없습니다.
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[프롬프트] 작업자 프롬프트를 새 프롬프트로 새로 고치는 것이 기본적으로 실패했습니다: %v", err)
		return
	}
	log.Printf("[프롬프트] 작업자 프롬프트에 새로운 기본 버전이 추가되었습니다(컨텍스트 세그먼트가 list_assets/list_findings로 수렴되고 list_facts/node_detail/asset_neighbors가 일회성 제거됨)")
}

// reporterToolCallMessage는 보고서를 작성하기 전에 무조건 get_finding_traffic을 한 번 읽어야 합니다.
// 이 도구는 읽기 전용이며 "캡처 스위치에 의존하지 않습니다". 수동 바인딩의 증거는 자동 바인딩이 꺼져 있는지 여부에 관계없이 읽을 수 있습니다. 여기라면
// "자동 제본이 활성화된 경우에만 읽기"라고 기록됩니다. 기본 구성이 꺼져 있으면 보고자는 증거_버전을 전달하지 않습니다.
// SetFindingReportVersionByNodeID는 레거시 의미 체계, 취약점 세부 정보 및 Markdown 내보내기에 따라 -1을 씁니다.
// 이제부터 "증거가 변경되었습니다. 보고서를 업데이트해야 합니다."라는 메시지가 항상 표시되며 이를 지울 수 있는 항목이 UI에 없습니다.
const reporterToolCallMessage = "report_finding에 의해 방금 취약점이 등록되었습니다. JSON으로 반환된 finding_id(독립 취약점 레코드 ID) 및 finding_node_id(탐색 노드 ID)를 읽어보시기 바랍니다." +
	"먼저 get_finding_traffic(finding_id)를 사용하여 현재 증거 목록과 해당 버전을 읽습니다(빈 목록이 일반적이며 평소대로 보고서를 작성합니다)." +
	"실행 가이드에서 자동 바인딩을 활성화한 경우 읽기 전에 이 취약점의 트래픽을 확인하고 상관관계를 확인하세요. 노드 세부정보는 finding_node_id를 사용합니다." +
	"마지막으로 update_finding_report(finding_id=finding_node_id, 보고서, evidence_version=실제 읽기 버전)를 호출하여 저장하고," +
	"evidence_version을 전달해야 합니다. 그렇지 않으면 보고서가 업데이트 보류 중으로 영구적으로 표시됩니다. 두 숫자를 혼합하지 마십시오."

// 레거시 트리거 메시지(0.3.8 이하). 여전히 단어 대 단어가 동일한 레코드만 마이그레이션으로 덮어쓰게 되며 사용자가 변경한 내용은 변경되지 않은 상태로 유지됩니다.
const reporterToolCallMessageV1 = "report_finding에 의해 방금 취약점이 등록되었습니다. 트리거 컨텍스트에서 finding_id" +
	"(도구 반환 \"finding recorded: <id>\" ) 및 작업 ID, 귀하의 책임에 따라 취약점에 대한 자세한 보고서를 작성하세요." +
	"마지막으로 update_finding_report(finding_id, Report)를 호출하여 저장합니다."

// 업그레이드ReporterTriggerMessage는 이전 라이브러리에 여전히 기본 복사본이 있는 보고자 트리거 메시지를 새 버전으로 새로 고칩니다.
// SeedReporterAgent는 reporter_agent_seed_v1에 의해 보호되며 새 에이전트를 생성할 때만 트리거를 작성하므로
// 업그레이드된 라이브러리는 새 사본을 얻을 수 없습니다. 도구 스키마는 seedFindingTrafficTools에 의해 완료됩니다.
// 증거_버전이지만 기자에게 이를 사용하라고 지시하는 내용은 없습니다. 이는 일회성 작업이며 수정되지 않은 복사본에만 적용됩니다.
func (s *Server) upgradeReporterTriggerMessage() {
	const flag = "reporter_trigger_evidence_version_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 한번만 해보세요
	triggers, err := s.m.pg.ListTriggersFor("reporter")
	if err != nil {
		log.Printf("[reporter] 트리거 읽기 실패: %v", err)
		return
	}
	for _, t := range triggers {
		if !t.OnToolCall || t.ToolCallMessage != reporterToolCallMessageV1 {
			continue // 사용자가 이를 변경하거나 결과 트리거가 아닌 경우 이동하지 않습니다.
		}
		t.ToolCallMessage = reporterToolCallMessage
		if err := s.m.pg.UpdateTrigger(t); err != nil {
			log.Printf("[reporter] 업그레이드 트리거 메시지 실패: %v", err)
			return
		}
		log.Printf("[reporter] 트리거 메시지가 evidence_version을 읽고 반환하도록 업그레이드되었습니다")
	}
}

// SeedReporterAgent는 "보고서 작성" 사용자 정의 에이전트를 사전 설정합니다(내장=false, UI에서 편집/삭제 가능).
// update_finding_report + 작업 쿼리 도구를 바인딩하고 "report_finding이 호출되면 트리거됩니다"를 중단합니다.
// 트리거 - 취약점이 등록될 때마다 자세한 보고서를 작성하도록 트리거됩니다. 일회성(플래그 가드 설정): 사용자가 삭제한 후에는 다시 생성되지 않습니다.
// 종속성: 오케스트레이션 도구는 이 기능 위의 SeedTool에 저장되어 있으므로 바인딩할 수 있습니다.
func (s *Server) seedReporterAgent() {
	const flag = "reporter_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 성공하든 실패하든 한 번만 시도하세요.

	if exist, _ := s.m.pg.GetAgentByKey("reporter"); exist != nil {
		return // 키가 사용됨(사용자가 수동으로 생성) - 덮어쓰지 않음
	}
	a, err := s.m.pg.CreateAgent("reporter", "보고서 작성",
		"상세 취약점 보고서 작성: 취약점 발견 시 자동으로 실행되며, 증거 및 실행 프로세스를 확인한 후 마크다운 보고서를 작성하고 다시 작성합니다.")
	if err != nil {
		log.Printf("[reporter]가 에이전트를 생성하지 못했습니다: %v", err)
		return
	}
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt); err != nil {
		log.Printf("[reporter] 시드 프롬프트 실패: %v", err)
	}
	// 트리거 실행 전략: 병렬 + 없음 - 하나의 취약점 보고서, 여러 결과가 병렬로 작성됩니다.
	// merge는 없음이어야 합니다. 그렇지 않으면(기본값 모두) 검색 결과가 하나의 실행으로 병합되고 병렬화가 의미가 없게 됩니다.
	// maxParallel=5: 한 번에 너무 많은 LLM 호출을 피하기 위해 동시에 최대 5개의 보고 세션.
	if err := s.m.pg.SetAgentTriggerBehavior("reporter", "parallel", "none", 5); err != nil {
		log.Printf("[reporter]가 트리거 실행 정책을 설정하지 못했습니다: %v", err)
	}
	// 이를 묶는 데 필요한 도구: 보고서 작성 + 증거/실행 프로세스/상황 읽기.
	if err := s.m.pg.AddAgentToToolBinding("reporter", []string{
		"update_finding_report", "get_task_node_detail", "list_task_findings",
		"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
		"get_task_graph",
	}); err != nil {
		log.Printf("[reporter] 바인딩 도구 실패: %v", err)
	}
	// 트리거: Report_finding이 호출되면 트리거됩니다(도구는 find_id와 함께 "기록된 찾기: <id>"를 반환합니다.
	// 작업 ID는 트리거 메시지에도 있습니다.
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:        "reporter",
		Enabled:         true,
		OnToolCall:      true,
		ToolNames:       []string{"report_finding"},
		ToolCallMessage: reporterToolCallMessage,
	}); err != nil {
		log.Printf("[reporter]가 트리거를 생성하지 못했습니다: %v", err)
	}
	log.Printf("[reporter]는 \"보고서 작성\" 에이전트 + 발견 트리거를 미리 설정했습니다")
}

// seedAutoReportFindingBinding adds "auto" to report_finding's binding ONCE so
// conversation-context agents can call it without requiring an intent_id.
func (s *Server) seedAutoReportFindingBinding() {
	const flag = "auto_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("auto", []string{"report_finding"}); err != nil {
		log.Printf("[auto] report_finding 기본 바인딩 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerDefaultBindings adds "planner" to report_finding's binding ONCE
// (guarded by a settings flag), so existing DBs — whose report_finding row was
// seeded as worker-only — also let the planner record findings. Fresh DBs already
// get it via PlannerTools(); this only backfills without overriding a user unbind.
func (s *Server) seedPlannerDefaultBindings() {
	const flag = "planner_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"report_finding"}); err != nil {
		log.Printf("[planner] report_finding 기본 바인딩 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedPlannerListAssetsBinding adds "planner" to list_assets's binding ONCE
// (guarded by a settings flag), so existing DBs — whose list_assets row was seeded
// as auto/pentest-only — also let the planner query the asset store by DSL. Fresh
// DBs already get it via PlannerTools(); this only backfills without overriding a
// user unbind.
func (s *Server) seedPlannerListAssetsBinding() {
	const flag = "planner_list_assets_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"list_assets"}); err != nil {
		log.Printf("[planner] list_assets 기본 바인딩 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedCompanyScopeRebind changes add_company_scope's default binding ONCE on
// existing DBs (guarded by a settings flag): the tool moves off worker and onto
// planner — defining a company's asset scope is a planning/main/auto concern, not
// something a worker does mid-exploration. Fresh DBs already get planner via
// PlannerTools() and lack worker via WorkerTools(); this only backfills old rows.
// One-shot + flag-guarded so a user who later re-binds worker isn't overridden.
func (s *Server) seedCompanyScopeRebind() {
	const flag = "company_scope_rebind_v1" // 작업자→계획자 기본 바인딩 스위치
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"add_company_scope"}); err != nil {
		log.Printf("[planner] add_company_scope 기본 바인딩 실패: %v", err)
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("worker", "add_company_scope"); err != nil {
		log.Printf("[worker] add_company_scope 바인딩 해제 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadToolsUnbind strips the read-context tools off worker's default
// binding ONCE on existing DBs (guarded by a settings flag): a worker executes one
// intent and writes back — reading facts/companies and listing all workers' traces is
// a planning/main concern, not the executor's. Fresh DBs already lack these via
// WorkerTools(); this only backfills old rows without overriding a user who
// deliberately re-binds worker. Each RemoveAgentFromTool is per-tool +
// membership-guarded, so planner/mainagent bindings of the same tool are untouched.
//
// NOTE: search_all_worker_traces / get_worker_trace / node_detail are intentionally NOT
// unbound — worker owns them for cross-work look-back + node drill-down (see WorkerTools).
// They used to be in this list back when worker lacked them; seedWorkerReadbackRebind
// repairs DBs whose old run stripped them.
func (s *Server) seedWorkerReadToolsUnbind() {
	const flag = "worker_readtools_unbind_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	for _, k := range []string{
		"list_facts", "list_companies", "list_worker_traces",
	} {
		if err := s.m.pg.RemoveAgentFromTool("worker", k); err != nil {
			log.Printf("[worker] %s가 작업자에서 바인딩을 해제하지 못했습니다: %v", k, err)
			return // 오류가 발생하면 플래그가 삭제되지 않고 다음에 다시 시도됩니다.
		}
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedWorkerReadbackRebind re-binds the cross-work look-back / drill-down tools onto
// worker ONCE (guarded by a settings flag): an earlier seedWorkerReadToolsUnbind wrongly
// stripped search_all_worker_traces / get_worker_trace / node_detail from worker after
// they had been added to WorkerTools(), so any DB that ran that migration lost them.
// Fresh DBs already have them via WorkerTools() and this is a harmless no-op there.
// One-shot + flag-guarded so a user who later deliberately unbinds them isn't overridden.
func (s *Server) seedWorkerReadbackRebind() {
	const flag = "worker_readback_rebind_v2" //v2: node_detail 추가
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("worker", []string{
		"search_all_worker_traces", "get_worker_trace", "node_detail",
	}); err != nil {
		log.Printf("[worker] 검토/세부정보 도구 재바인딩 실패: %v", err)
		return // 오류가 발생하면 플래그가 삭제되지 않고 다음에 다시 시도됩니다.
	}
	_ = s.m.pg.SetSetting(flag, "true")
}

// seedAutoDefaultBindings adds "auto" to the task-op + platform tools' bindings
// ONCE (guarded by a settings flag), so existing DBs whose tool rows were seeded
// before Auto existed still give Auto its default toolset — without re-adding it
// after a user deliberately unbinds.
func (s *Server) seedAutoDefaultBindings() {
	const flag = "auto_default_bindings_v3" // v3: 이전 자산 도구 이름을 바꾸고 insert_assets/add_company_scope를 추가합니다.
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	keys := make([]string, 0, len(platformToolKeys)+12)
	for _, t := range s.orchestrationTools() {
		keys = append(keys, t.Name())
	}
	keys = append(keys, platformToolKeys...)
	// 자산 도구: 자동 운영 플랫폼은 종종 자산을 확인/등록하고 회사 범위를 관리해야 합니다.
	keys = append(keys, "insert_assets", "add_company_scope", "list_assets")
	if err := s.m.pg.AddAgentToToolBinding("auto", keys); err != nil {
		log.Printf("[auto] 기본 바인딩 실패: %v", err)
		return
	}
	_ = s.m.pg.SetSetting(flag, "true")
}
