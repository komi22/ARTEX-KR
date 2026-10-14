package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[당신의 할 일 계획(당신이 마지막 라운드에서 작성한, 기상 후에도 유지됨)]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("따라서 진행하세요. [이전 단계가 완료되었거나 의존하는 사실이 이미 존재하는 경우] 다음 단계로만 인텐션을 보냅니다. TodoWrite를 사용하여 목록을 업데이트합니다(사실에 의해 충족된 단계를 완료됨으로 표시). 매니페스트에서 이미 보류 중/진행 중인 단계를 반복하지 마세요.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — 작업자가 의도 IntentID(세부 사항 = 요약)에 대한 결과를 보고했습니다.
//	"goal" — 인간(주 에이전트의 set_goals를 통해)이 하나 이상의 목표를 추가했습니다.
//	            단일 호출(Goals = 이번에는 새 대상 텍스트, 1개 이상의 항목, set_goals는 배치를 지원함)
//	"goal_deleted" — 인간이 개요 목표 관리에서 목표를 삭제했습니다(세부 사항 = 삭제된 목표 텍스트).
//	"goal_edited" — 인간이 목표 관리 개요(OldGoal→NewGoal 텍스트)에서 목표를 편집했습니다.
//
// "취소됨" - 사람이 삭제한 인텐트 IntentID(세부정보 = 삭제 이유) 의도는
//
//	stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" Exclusive: 삭제 전 캡처된 인텐트 요약(삭제 후 노드가 더 이상 존재하지 않으며 다시 확인할 수 없음)
	Goals    []string // Kind=="goal" Exclusive: 이번에 set_goals에 의해 추가된 대상 텍스트(1개 이상)
	OldGoal  string   // Kind=="goal_edited" 특수: 수정 전 대상 텍스트
	NewGoal  string   // Kind=="goal_edited" 독점: 수정된 목표 텍스트
	Hints    []string // Kind=="hint" 독점: 이 add_hint에 의해 추가된 새 힌트 텍스트(1개 이상)
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[이 라운드를 촉발한 실제 변경 사항(먼저 여기를 읽고 방향을 정할지 결정)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- 개인(주요인)이 새로운 목표를 추가했습니다: %s - 달성할 새로운 목표, 이에 따라 탐색 방향을 보완하십시오(아직 해당 의도가 없는 경우).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 개인(주요 에이전트)이 %d개의 새로운 목표를 추가했습니다: %s - 모두 달성해야 할 새로운 목표입니다. 해당 의도가 없는 목표에 대해서는 탐색 방향을 하나씩 추가해 주세요.", len(ev.Goals), strings.Join(ev.Goals, "；")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- 사람(주요원)이 새로운 전략 팁을 추가했습니다: %s - 탐험 지도에 연결되었습니다. 이에 따라 탐험 방향을 조정/보충하십시오(아직 해당 의도가 없는 경우).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 사람(주요원)이 %d개의 전략 팁을 추가했습니다: %s - 모두 탐색 지도에 연결되어 있으므로 이에 따라 탐색 방향을 조정/보완하세요.", len(ev.Hints), strings.Join(ev.Hints, "；")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- 누군가가 대상을 삭제했습니다: %s - 대상이 제거되었습니다. 그에 따라 나머지 대상/방향을 다시 판단하십시오(더 이상 인텐트를 할당할 필요가 없습니다).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- 대상이 %q에서 %q로 변경되었습니다. 새로운 대상에 따라 탐색 방향을 조정하십시오(원래 방향이 더 이상 적용되지 않는 경우 전송을 중지하십시오).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- #%d(%s) 의도를 가진 작업자가 결과를 보고했습니다: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 삭제 중에 캡처된 요약을 사용하여 인텐트 콘텐츠에 우선순위가 부여됩니다(실제 삭제 후에는 노드가 더 이상 존재하지 않으며,intentSummary를 찾을 수 없습니다).
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- 사용자가 인텐트 #%d을(를) 삭제했습니다. 인텐트 콘텐츠는 %s이고 삭제 이유는 %s입니다. 인텐트가 삭제되었습니다(더 이상 실행되지 않음). 그에 따라 계획을 세우십시오.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- #%d(%s) 의도를 가진 작업자가 종료되고 결론 출력: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf(";이 의도에 의해 생성된 새로운 사실 ID: %s ", fids))
			}
		}
	}
	b.WriteString("\n(자세한 내용은 node_detail / get_worker_output / list_findings에서 확인하실 수 있습니다.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, "、")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(출력을 가져오지 못했습니다)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(아직 출력기록이 없는 작품입니다)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(잘림, 전체 내용은 get_worker_output 참조)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[이번 라운드의 현황: 아래 내용은 graph_overview의 반환값과 같습니다. 추가 증거가 필요할 때만 node_detail이나 list_facts를 호출하세요.]:\n" + string(b)
}

// plannerDefaultTmpl은 플래너 프롬프트의 내장된 편집 가능한 본문(섹션 [A])입니다.
// Agent_prompts에 시드됩니다. 목표는 {{.Goal}} 템플릿 변수입니다. 중간제품 출력 사양
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `당신은 승인된 침투 테스트 작업의 계획자입니다. 탐색 그래프가 변할 때마다 실행됩니다. 전체 상황을 읽고 목표 달성을 판단한 뒤, 아직 다른 의도가 다루지 않는 실질적인 새 방향이 있을 때만 탐색 의도를 추가하세요. 직접 테스트하는 실행자가 아닙니다. 이번 계획 라운드의 결과는 의도 생성 또는 목표 판정이어야 합니다.

작업 목표: {{.Goal}}

# 이번 라운드의 의도 개수
- **최우선 규칙**: 목표가 아직 달성되지 않았고 열린 의도도 실행 중인 의도도 없다면(frontier_open=0, running_intents 비어 있음), 목표를 향한 의도를 적어도 하나 내야 합니다. 기다릴 작업과 대기 중인 방향이 모두 없는데 의도가 0개라면 작업이 멈춥니다. recent_done만 남았다면 아래의 완료·예산 소진·차단 상태를 검토하고 다른 경로를 열거나 이어서 진행하세요.
- 위 경우가 아니라면 0개도 타당할 수 있습니다. 생각한 방향이 이미 open/running 의도에 포함되거나, 다음 단계가 실행 중인 작업의 실제 결과를 기다려야 할 때입니다. 표현만 바꾼 중복 의도나 아직 없는 결과를 가정한 의도를 만들지 마세요.
- 아직 다루지 않았고 실행 중인 작업에 의존하지 않는 방향이 있거나, 목표가 미달성이고 승인 범위 안에 미검증 영역이 있다면 의도를 내세요. 무조건 적게 내는 것을 기본값으로 삼지 마세요.

# 매번 실행할 판단 과정
1. **현재 상황 확인**: 아래에 graph_overview 결과가 미리 붙어 있으므로 같은 도구를 다시 호출할 필요가 없습니다. task, 자산 수, goals와 상태, open/running/recent_done 의도, sites_without_endpoints, facts와 findings의 개수, recent_facts(id, summary, confidence)를 읽으세요. 탐색 노드는 현재 작업에 속하지만 자산 그래프는 여러 작업이 공유합니다. 관련 없는 자산은 무시하세요. 의도의 parents와 yields, 사실의 from_intent로 어떤 방향에서 어떤 결과가 나왔는지 확인하세요.
   - '포트 닫힘', '주입 불가' 같은 부정적 관찰은 Worker의 관찰이지 자동으로 최종 결론이 아닙니다. node_detail(id)로 evidence를 확인하세요. 충분한 증거, confidence=observed, 합리적인 수단의 소진이 함께 확인되면 해당 경로를 잠시 닫으세요. 증거가 없거나 한 번 시도했거나 confidence=inferred라면 범위 안에서 다른 의도가 다루지 않는 경우 검토 의도를 한 번 내세요. 같은 부정적 방향은 최대 한 번만 재검토하고, 그 뒤에도 합당한 부정적 근거가 있으면 존중하세요.
   - 세부 정보가 필요할 때만 list_facts(최신순 페이지, q/before 필터), list_findings, node_detail(id), list_assets(q/type/company_id/task_id 또는 id/ids), asset_neighbors를 사용하세요. 전역 자산 목록 전체를 기본으로 가져오지 마세요.
2. **목표 판정**: 확인된 발견이나 사실이 미달성 목표를 입증한다면 prove_goal(goal_id, evidence_id, reason)을 호출하세요. 마지막 미달성 목표를 표시하면 시스템이 작업 완료를 결정합니다. 별도 일괄 완료 방법은 없습니다. 목표에 커버리지, flag 개수, 권한 등 정량 조건이 있다면 graph_overview의 실제 수치(coverage.pct, findings_total 등)를 먼저 확인하세요. 미달성이면 prove_goal을 호출하지 말고 부족한 부분을 확인할 의도를 내세요. '대체로 달성'했다는 이유로 미리 완료 처리하지 마세요.
3. **초기 방향을 정하기 위한 선택적 확인**: 작업이 막 시작돼 facts와 recent_facts가 거의 없고 상황만으로 첫 의도를 구체화할 수 없을 때만, Bash 등으로 홈페이지나 기술 지문을 확인하는 읽기 전용 요청을 최대 세 번 수행할 수 있습니다. 목적은 의도 설명을 정확히 하는 것뿐입니다. 취약점 발견·검증·악용이나 엔드포인트·디렉터리·매개변수 열거는 Worker에게 의도로 맡기세요. Worker 사실이 하나라도 생긴 뒤에는 계획자가 직접 탐지하지 마세요. 기존 사실로 판단할 수 있으면 초기 탐지도 생략하세요.
4. **새 방향 선택**: 목표, 사실, 자산을 결합해 열린 탐색 방향을 생각하고 open, running, recent_done 의도와 비교하세요. 절제는 중복을 피하라는 뜻이지 무조건 적게 내라는 뜻이 아닙니다.
   - open/running이 이미 다루는 방향은 다시 만들지 마세요.
   - recent_done의 state=done이면 정상 종료된 경로입니다. 실제 yields의 사실을 확인하고, 새로운 사실·자산·매개변수 또는 분명히 다른 메커니즘이 있을 때만 재개하세요. summary에 지난번과 무엇이 다른지 명시하세요.
   - state=exhausted 또는 blocked이면 완료되지 않은 경로일 수 있습니다. get_worker_trace나 get_worker_output으로 실제 진행과 중단 지점을 확인하세요. 예산 때문에 중간에 멈췄다면 이어서 수행하는 의도를, 외부 오류로 거의 실행하지 못했다면 같은 방향의 재시도를 고려하세요. 계속 같은 곳에서 막히면 접근법을 바꾸세요. 상태 이름만으로 판단하지 마세요.
   - 모든 알려진 방향을 open/running이 다루고 있다면 다음 결과를 기다리세요. recent_done만 남고 목표가 미달성이라면 최우선 규칙에 따라 다른 방향을 열거나 계속 진행해야 합니다.
   - 가치 있는 진입점이 RCE, 권한 상승, 데이터 유출로 이어질 근거가 있다면 얕은 커버리지 확대보다 그 경로를 깊게 검증할 의도를 우선하세요. 다만 서로 다른 메커니즘의 경로 2~3개를 유지해 너무 일찍 한 방향에만 집중하지 마세요. 모든 새 방향은 작업 제약을 지켜야 하며 금지된 호스트·포트·행위를 의도로 만들지 마세요.
   - 앞 단계의 실제 결과가 필요한 연속 공격 체인은 병렬로 내지 마세요. TodoWrite에 각 단계를 기록하고 이번에는 전제조건이 충족된 단계만 발행하세요. 결과가 나오면 다음 단계로 넘어가고 완료된 단계를 표시하세요. 독립적인 조사만 병렬화하세요.
5. **의도 제출**: 골라낸 새 방향을 add_intent 한 번의 호출로 intents 배열에 최대 네 개까지 제출하세요. summary는 대상의 전체 주소, 할 일, 이유를 한 문장으로 적고 기존 의도와 비교해 중복을 피하세요. 구체적인 자산을 다루면 list_assets에서 확인한 asset_ids를 넣으세요. 여러 자산을 함께 다루면 모두 전달하고 순수 전역 정찰일 때만 비워 두세요. 상위 사실·의도·발견에서 파생됐다면 해당 노드의 parent_ids를 전달하세요. 완전히 새로운 최상위 방향이면 비워 두세요.

필요 없는 의도를 억지로 만들지 마세요. 목표가 미달성이고 새로운 실질적 방향이 있으면 분명하게 발행하세요.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 도메인 도구 + 기본 기본 도구 세트(읽기/쓰기/편집/MultiEdit/LS/Glob/Grep/Bash)
	// 자산 적용 범위 기능이 꺼진 경우 add_task_scope/list_untested_assets(프롬프트 입력 없이)을 제거하세요.
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// 핵심 상황(방금 완료된 의도 + 미리 가져온 완전한 그림)이 [이번 사용자 입력](아래 입력 참조)으로 변경되고, 시스템
	// 정적 계획 텍스트만 남습니다. 이동은 각 라운드에서 시스템을 안정적으로 만들고 캐싱에 더 도움이 됩니다. 가격은 단일 라운드가 길어지면 상황이 발생할 수 있다는 것입니다.
	// 압축으로 압축됩니다(플래너 라운드는 일반적으로 짧고 위험이 낮습니다). 상황에 따른 철자를 아래 입력에 입력합니다.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	//작업 수준 기한/최종 모드(ctx를 통해 주입, taskclock.go 참조). 마지막 라운드에서 작업 시간이 초과됩니다.
	// 플래너의 종료어는 [이 라운드의 동작 명령]으로 사용되며, 이번 라운드(상황)에 대한 사용자 입력에 삽입되어 마지막에만 수행할 수 있도록 합니다.
	// 목표 결정, 새로운 의도 없음.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[미션 마지막(위의 일반 계획 프로세스를 다루는 이번 라운드에 대한 특별 지침)]:" + resolveTaskTimeoutWrapup("planner")
	}
	// 이 작업의 작업 디렉터리인 <workDir>/tasks/<taskID>가 먼저 생성됩니다.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 운영 제약 조건(있는 경우)이 시스템에 주입되어 탐색 경계를 구성하라는 메시지가 표시됩니다.
	}
	system, boundary := deferredSystem(sysBody, def)
	// 플래너에게는 자체 벽시계 예산이 없습니다. 기한이 있는 경우 실행 중인 계획 휠이 다음을 수행할 수 있도록 MaxDuration이 나머지 부분으로 고정됩니다.
	// 작업이 끝에 도달하면 종료됩니다(시간 초과로 인해 → 작업 시간 초과 단어로 인해, 단계 수로 인해 → 실행당 단어로 인해).
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, //프록시 추적을 기록합니다. MITM이 다시 서명한 HTTPS 인증서를 확인하기 위해 프록시 CA를 로드합니다.
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// 인터넷 검색(선택사항). ddgs에는 키가 필요하지 않습니다. Brave-Free에는 BraveKey가 필요합니다. TavilyKey가 필요합니다.
		// WebSearchProxy는 독립적인 내보내기 프록시(http/https/socks5)이며 트래픽을 기록하는 MITM 프록시와 아무 관련이 없습니다. 비어 있으면 직접 연결됩니다.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 하위 명령의 기본값은 프록시 + 신뢰할 수 있는 CA입니다.
		WorkingDir:            taskDir,                              // 이 작업의 작업 디렉터리 <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=제한 없음; 마감일이 있을 때 = 마감일까지 남은 시간
		Compaction:            compactionConfig(p.compactionWindow()),
		// 웨이크 전반에 걸쳐 백로그 공유 계획: 라운드 전반에 걸쳐 직렬 체인이 지속되도록 합니다(세션은 새 것이고 저장소는 아님).
		Todos: p.todoFor(ts.ID()),
		// [이번 라운드] 단계 예산 달성 → 끝까지 SDK 실행: 이번 라운드에서 명확하게 생각한 결론을 구현합니다(팩션의 add_intent,
		// 증명 가능한 Provive_goal, 직렬 체인 레코드 TodoWrite), 계획을 중단하는 대신 계획자는 이후에도 반복적으로 깨어납니다.
		// 고정된 경우(작업 기한에 따라 고정된 경우) 대신 PromptByReason을 사용하세요(wrapupSettlementForTask 참조).
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // Provider.Complete는 프로필이 비스트리밍일 때 사용됩니다.
		MaxTokens:    p.maxTokens(),    // 0 = 서버의 기본값에 의해 결정된 상한값을 보내지 않음
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// 실험 기능: 켜진 후 noa가 컨텍스트 압축을 대신합니다(아카이브는 <workDir>/noa/<SessionID>에 집중되어 있으며 지속됩니다).
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// 이제 상황(방금 완료된 의도 + 완전한 그림)이 이 사용자 입력 라운드에 결합됩니다(아래 입력 참조). 또한 있다
	//Command + 교차 깨우기 할 일(todo는 재현 가능하고 사용자에게 배치할 수 있는 모델 자체의 계획 노트입니다.)
	// 오프닝 멘트는 "이번 라운드에 구체적인 변경 사항이 있는지 여부"에 따라 두 가지 유형으로 나뉩니다. 변경 사항이 있습니다 → 아래를 가리키는 [실제 변경 사항] 블록; 변경사항 없음
	// (심박예정점검/힌트/회복 등) → "그림이 바뀌었다"라고 거짓말하지 말고 대신 실행의도를 재검토하라는 메시지를 표시합니다.
	lead := "특정 변경 사항이 있었습니다(아래 [이번 라운드를 촉발한 실제 변경 사항] 참조). 이에 따라 다음 단계를 계획하세요."
	if len(triggers) == 0 {
		lead = "이번 라운드는 **정기 검사(하트비트에서 지점까지)/특정 변경 신호 없음**의 모닝콜입니다. 그래프에 반드시 새로운 변경 사항이 있을 수는 없습니다. 그런데 실행 의도를 검토하십시오. 오랫동안 진행이나 편차가 없으면 steer_work를 사용하여 편차를 수정하십시오. 방향이 완전히 잘못된 경우 kill_work를 사용하여 손실을 중지하세요. 그런 다음 대상을 판단하고 방향을 수정할지 여부를 결정합니다."
		// 심박동 변화 없이 깨어났을 때, 맵 전체에 오픈 또는 실행 의도가 없는 경우 → 탐색이 중지되었습니다(워커가 실행 중이 아니며,
		// 대기열 방향도 없습니다). 기획자에게 명확하게 알리고 이번 라운드에 새로운 방향을 제시하도록 강제하세요. 단지 실행 의도를 검토한 다음 한 라운드 동안 유휴 상태로 두지 마십시오.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "이번 라운드는 예정된 검사(하트비트 투 포인트)의 각성이며 현재는 열거나 실행할 의도가 없습니다. 실행 중인 작업자도 없고 대기열에 방향도 없으며 탐색이 중지되었습니다. 이 라운드에서 목표를 향해 나아가고 그림의 기존 의도와 **중복되지 않는** 하나 이상의 새로운 의도를 **생성해야 합니다**(0개의 의도는 허용되지 않음). 먼저 다음 상황을 토대로 목표 달성 여부를 판단하고, 그렇지 않은 경우 즉시 방향을 추가하세요."
		}
	}
	input := lead + situational + "\n\n위의 상황을 토대로 대상을 결정하세요. 목표가 [실제로 달성]되면(목표 결과 획득/대상 취약점 확인) prove_goal을 사용하여 하나씩 표시합니다. **확실한 결론: 목표에 도달하지 않았고 현재 열려 있거나 실행 중인 의도가 없는 한(frontier_open=0 및 running_intents가 비어 있음), 이 라운드는 목표를 향해 나아가기 위한 하나 이상의 의도를 생성해야 합니다. 이때 대기할 실행 작업이 없고 대기열에 방향이 없습니다. 0의 의도 생성 = 작업 중단. 이미 open/running 의도가 진행 중이거나 목표가 달성된 경우에만 이번 라운드에서는 새로운 의도가 생성될 수 없습니다. **" +
		renderPlannerTodos(opts.Todos.List())
	// 이제 MaxDuration은 벽시계가 해당 지점에 도달하면 실행 중인 도구를 중단하고 그 자리(라이브 ctx에서)에서 완료하며 더 이상 단일 라운드에 갇히지 않습니다.
	// 마감 처리를 생략하고 외부 하드 CTX 포켓이 필요하지 않습니다. ctx는 일시 중지/종료/종료만 수행합니다.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
