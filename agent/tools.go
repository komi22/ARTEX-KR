package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// compactIntents distills intents to {id, summary, state, asset_ids, parents,
// yields} so the planner sees both the direction and its LINEAGE — parents (the
// upstream nodes it derived from: facts/intents/findings) and yields (the facts/
// findings it produced) — without pulling full payloads. parentsOf/yieldsOf are
// built from the exploration edges in graph_overview.
func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64) []map[string]any {
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m := map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State}
		if n.Inherited {
			m["source_task_id"] = n.SourceTaskID
			m["inherited"] = true
		}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok := p["asset_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_id"]; ok && tg != nil && tg != "" {
			m["asset_ids"] = []any{tg}
		}
		if ps := parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // 업스트림: 이 의도가 파생되는 노드(여러 사실이 공동으로 의도를 생성할 수 있음)
		}
		if ys := yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // 다운스트림: 의도로 인해 어떤 사실/발견이 이루어졌는가?
		}
		out = append(out, m)
	}
	return out
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	findingRecorder FindingRecorder
	as              *db.AssetStore   // asset store (optional; nil = asset tools not available)
	cs              *db.CompanyStore // company store (optional)
	ts              *db.ExplorationStore
	worker          string
	taskID          int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// coverageDisabled mirrors tasks.coverage_enabled=false. Stored inverted so the
	// zero value (all existing ToolSet constructions) means ENABLED — matching the
	// DB default (true). When true: graphOverviewData drops the coverage block, the
	// auto-scope hook (insertAssets) is skipped, and add_task_scope/list_untested_assets
	// are filtered out of the agent's tool list. The scope field stays regardless.
	coverageDisabled bool
	// ownerNode is the exploration node that writes attach to: assets this run
	// touches get anchored to it as lineage/provenance (NOT visibility — the asset
	// graph is global and shared). Worker = its claimed intent; planner = begin root.
	ownerNode int64
	GoalMet   bool
	Reason    string
	writes    WriteCounts
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
	// notifyFinding, if set, wakes the task's planner when this run reports a finding,
	// carrying (intentID, summary) so the round can spell out which intent found what.
	// Wired for workers; nil elsewhere → falls back to notify (bare wake).
	notifyFinding func(intentID int64, summary string)
	// resumeTask, if set, revives the task after a graph change that should make a
	// stopped task run again (currently: set_goals adds a goal). It flips a terminal/
	// paused task back to running and (re)starts the engine loops — a plain notify()
	// can't, because the planner's terminal gate swallows wakes. Wired ONLY for the
	// main agent (human steering); nil for the goals decomposer and workers.
	resumeTask func()
	// notifyGoal이 설정된 경우 플래너를 깨우고 하나의 "사람들이 N개의 새 목표를 추가했습니다:..." 트리거를 기록합니다.
	// for a whole set_goals call (batch-aware — one call, one trigger, not one per goal)
	// so the next round spells out the added goals (instead of the planner having to
	// spot new open goals in the overview). Wired ONLY for the main agent; nil for the
	// goals decomposer (round-0 has no running planner to inform) and workers → those
	// fall back to the bare notify.
	notifyGoal func(texts []string)
	//informHint가 설정된 경우 플래너를 깨우고 "사람들이 N개의 전략적 팁을 추가했습니다:…"를 1개 기록합니다.
	// trigger for a whole add_hint call (batch-aware — one call, one trigger) so the next
	// round is told the round was fired by a new hint and spells the hint out, instead of
	// the planner having to spot it folded into the graph overview. Wired for the main
	// agent + cross-task orchestration; nil elsewhere → falls back to the bare notify.
	notifyHint func(texts []string)
}

// SetNotifyGoal wires the goal-add trigger callback (see ToolSet.notifyGoal). Set only
// by the main-agent chat, so runtime-added goals are announced to the planner by name.
func (t *ToolSet) SetNotifyGoal(fn func([]string)) { t.notifyGoal = fn }

// SetNotifyHint wires the hint-add trigger callback (see ToolSet.notifyHint). Set by
// the main-agent chat and cross-task orchestration, so a runtime-added hint fires a
// planner round announced by name instead of a bare wake.
func (t *ToolSet) SetNotifyHint(fn func([]string)) { t.notifyHint = fn }

// SetResumeTask wires the task-revive callback (see ToolSet.resumeTask). Set only by
// the main-agent chat, so runtime-added goals can pull a finished task back to running.
func (t *ToolSet) SetResumeTask(fn func()) { t.resumeTask = fn }

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// SetNotifyFinding wires the finding-wake callback (see ToolSet.notifyFinding).
func (t *ToolSet) SetNotifyFinding(fn func(int64, string)) { t.notifyFinding = fn }

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings; each element of a batch counts once).
type WriteCounts struct {
	Facts    int
	Assets   int
	Findings int
}

// Total is every node persisted this run, regardless of kind — the
// "explored but persisted nothing" signal (Total == 0).
func (w WriteCounts) Total() int { return w.Facts + w.Assets + w.Findings }

// 문자열은 로그에 대한 종류별 분석을 렌더링합니다. "사실 1 자산 25 취약점 0".
func (w WriteCounts) String() string {
	return fmt.Sprintf("사실 %d 자산 %d 취약점 %d", w.Facts, w.Assets, w.Findings)
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// SetCoverageEnabled records whether this task has the asset-coverage feature on
// (default enabled). Passing false makes graphOverviewData omit the coverage block
// and DropCoverageTools filter the two coverage-only tools out of the agent's tool
// list. It does NOT stop scope accumulation: insertAssets' auto-scope hook runs
// either way, because task_scope is the task's range boundary (the filter basis for
// asset queries), not merely a coverage denominator.
func (t *ToolSet) SetCoverageEnabled(enabled bool) { t.coverageDisabled = !enabled }

// CoverageDisabled reports whether the coverage feature is off for this task.
func (t *ToolSet) CoverageDisabled() bool { return t.coverageDisabled }

// coverageOnlyTools are the LLM tools that only make sense when asset coverage is
// on. When the feature is off they are filtered out of the agent's tool list so
// they neither pollute the prompt nor let the model build a disabled denominator.
// add_task_scope is deliberately NOT here: task_scope is the task's range boundary
// (the filter basis for asset queries), not merely a coverage denominator, so the
// 범위 정의를 소유한 에이전트는 이를 어느 쪽이든 유지합니다 — insertAssets'와 고정 단계로 유지합니다.
// auto-scope hook, which also runs regardless of the switch.
var coverageOnlyTools = map[string]bool{"list_untested_assets": true}

// DropCoverageTools returns tools with the coverage-only ones removed when this
// task has the feature disabled; otherwise it returns tools unchanged.
func (t *ToolSet) DropCoverageTools(tools []actool.CoreTool) []actool.CoreTool {
	if !t.coverageDisabled {
		return tools
	}
	out := tools[:0:0]
	for _, tool := range tools {
		if coverageOnlyTools[tool.Name()] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(…,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool      { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool       { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool     { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool   { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) NodeDetailTool() actool.CoreTool         { return t.nodeDetail() }
func (t *ToolSet) AddHintTool() actool.CoreTool            { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// anchorOwner records a lineage edge from this run's owner node to an asset
// (no-op if unset). Provenance only — the asset graph is global and shared, so
// this no longer affects which assets a task can read.
func (t *ToolSet) anchorOwner(assetID int64) {
	if t.ts != nil && t.ownerNode > 0 && assetID > 0 {
		_ = t.ts.Anchor(t.ownerNode, assetID)
	}
}

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r := range raw {
		if v := pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// readExpTool / writeExpTool build a domain tool whose handler dereferences the
// task-bound ExplorationStore. Two ToolSets carry a nil store: the catalog's
// seed-only shell (never called) and the server-level one behind buildDomainReg,
// which the tools table can bind to ANY agent — including ones that never run
// 작업 내부(자동/침투 테스트/보고자/사용자 정의 에이전트/우회 질문). 거기에 거절
// keeps a mis-bound tool a bad tool call; without the guard it was a nil deref,
// and tool handlers run on the harness's own goroutine, so the panic is out of
// reach of every recover() in the server and kills the whole process.
func (t *ToolSet) readExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return readTool(name, desc, schema, t.needExploration(name, run))
}

func (t *ToolSet) writeExpTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return writeTool(name, desc, schema, t.needExploration(name, run))
}

// needExploration wraps a handler so it only runs with an exploration store.
// Tools that degrade more usefully than "unavailable" (report_finding points at
// add_task_hint, set_goals/set_constraints at the task itself) keep their own
// bespoke guard instead.
func (t *ToolSet) needExploration(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if t.ts == nil {
			return actool.Errorf(name + " 작업 컨텍스트(탐색 그래프)가 필요합니다. 현재 에이전트가 작업에서 실행되고 있지 않고 작업의 탐색 그래프를 얻을 수 없으며 도구를 사용할 수 없습니다. 작업 내에서 이를 사용하거나 task_id가 있는 교차 작업 읽기 도구(get_task_node_detail / list_task_findings / get_task_graph 등)를 대신 사용하세요."), nil
		}
		return run(ctx, in)
	}
}

func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return t.readExpTool("graph_overview",
		"(탐색 링크 다이어그램) 탐색 상황 증류 요약: 자산 수, 인터페이스 없는 사이트, 프론티어, 발견, 힌트(인간/마스터 에이전트에 대한 전략적 힌트, 의도 생성 시 포함되어야 함). 계획할 때 먼저 조정하세요.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return jsonResult(t.graphOverviewData())
		})
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func (t *ToolSet) graphOverviewData() map[string]any {
	out := map[string]any{}
	// goals summary folded in so the planner needn't call list_goals each round.
	goals, _ := t.ts.ListByKind(db.KindGoal, 100)
	gsum := make([]map[string]any, 0, len(goals))
	for _, g := range goals {
		var p map[string]any
		_ = json.Unmarshal(g.Payload, &p)
		gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
	}
	out["goals"] = gsum
	// 힌트: 인간/주요 에이전트는 add_hint를 통해 지도에 전략적 힌트를 표시합니다. 이렇게 접어서
	// 플래너는 인텐트를 생성할 때 매 라운드마다 이를 읽습니다(그렇지 않으면 쓰기만 하고 읽지는 않습니다).
	hints, _ := t.ts.ListByKind(db.KindHint, 50)
	hsum := make([]map[string]any, 0, len(hints))
	for _, h := range hints {
		var p map[string]any
		_ = json.Unmarshal(h.Payload, &p)
		hint := map[string]any{"id": h.ID, "state": h.State, "text": p["text"]}
		if findingTrafficBindingEnabled() && p["traffic_refs"] != nil {
			hint["traffic_refs"] = p["traffic_refs"]
		}
		hsum = append(hsum, hint)
	}
	out["hints"] = hsum
	// lineage from the exploration edges: an intent's parents (what it
	// derived_from — possibly several facts combined) and its yields (the
	// facts/findings it produced). factFrom maps a fact → the intent that
	// produced it. This is the relationship layer the flat lists lacked.
	edges, _ := t.ts.Edges(5000)
	parentsOf := map[int64][]int64{}
	yieldsOf := map[int64][]int64{}
	factFrom := map[int64]int64{}
	for _, e := range edges {
		switch e.Rel {
		case db.RelDerivedFrom, db.RelSpawns: // upstream: derived_from (fact/finding/intent→intent) or spawns (origin fact→goal, legacy begin→intent)
			parentsOf[e.To] = append(parentsOf[e.To], e.From)
		case db.RelYields: // intent --yields--> fact/finding
			yieldsOf[e.From] = append(yieldsOf[e.From], e.To)
			factFrom[e.To] = e.From
		}
	}
	// cold-digest §6: members folded into an active digest are shown via cold_digests
	// (below), not the flat recent_* lists. `covered` maps member id → its digest id.
	// §6 render-time revival check: a covered member that has become hot again (a new
	// intent derived from it) must reappear this round — so `hidden` folds a member out
	// only when it is covered AND still cold.
	covered, _ := t.ts.CoveredMembers()
	// Render-time hot set (ancestor of a live intent / fact under a live intent).
	// §6 revival check: a covered member that revived (now hot) must NOT stay folded
	// — hidden() only folds a member out when it is covered AND still cold. Computed
	// every round (cheap for real graph sizes); nil map degrades safely.
	var hotAtRender map[int64]bool
	if cg, _, err := loadColdGraph(t.ts); err == nil {
		hotAtRender = cg.hotSet()
	}
	hidden := func(id int64) bool { _, c := covered[id]; return c && !hotAtRender[id] }
	const openIntentsCap = 30
	fr, _ := t.ts.Frontier(openIntentsCap) // 우선 순위 DESC, id ASC - 우선 순위가 가장 높은 상위 N개 항목입니다. 실제 총 개수는 frontier_open을 참조하세요.
	out["open_intents"] = compactIntents(fr, parentsOf, yieldsOf)
	all, _ := t.ts.ListByKind(db.KindIntent, 300)
	var running, recentDone []*db.Node
	for _, n := range all {
		switch n.State {
		case "running":
			running = append(running, n)
		case "done", "blocked", "exhausted":
			if hidden(n.ID) {
				continue // in a cold_digest and still cold — shown via cold_digests (§6.2)
			}
			recentDone = append(recentDone, n) // 최신순(모두 ID 기준 내림차순) 접힌 것은 없애고, 출력시 가장 최근의 N은 잘립니다.
		}
	}
	out["running_intents"] = compactIntents(running, parentsOf, yieldsOf)
	// done_intents_total: 종료된 의도의 총 수(완료/차단/소진), Recent_done_intents와 동일
	// 병렬 이름 지정 - 후자는 최신 창을 잘린 보기일 뿐입니다. 나란히 있는 두 개의 키는 다음과 같이 자체적으로 설명됩니다. "보이는 것은 N/총 숫자입니다."
	// 플래너에게 중복 제거를 요청할 때 "표시되지 않음"을 "전송되지 않음"으로 처리하지 마십시오. 프롬프트에서는 설명할 필요가 없습니다.
	if dt, err := t.ts.CountFinishedIntents(); err == nil {
		out["done_intents_total"] = dt
	}
	// frontier_open: 열린 인텐트의 실제 총 개수입니다(open_intents는 우선순위가 가장 높은 상위 N개의 잘린 뷰입니다).
	if fo, err := t.ts.CountOpenIntents(); err == nil {
		out["frontier_open"] = fo
	} else {
		out["frontier_open"] = len(fr)
	}
	// findings (confirmed vulns) and facts (worker exploration results) are
	// now distinct node kinds. recent_facts surfaces fact summaries (esp.
	// negative results) so the planner sees them in one call; full content
	// via node_detail(id).
	vulnNodes, _ := t.ts.ListByKind(db.KindFinding, 1000)
	factNodes, _ := t.ts.ListByKind(db.KindFact, 1000) // newest first
	out["findings_total"] = len(vulnNodes)             //총 취약점 수를 확인합니다(대상 결정은 참조). 자세한 내용은 Finding_list(최신 창)를 참조하세요.
	out["facts"] = len(factNodes)                      // 사실/결론의 수를 탐색합니다(부정적 결론 포함).
	// 결과는 작업에서 가장 높은 가치의 제품입니다. → 개요에는 최신 창이 표시됩니다(10개 항목 이하, vulnNodes는 ID별로 내림차순으로 정렬되었습니다. 즉, 최신 항목부터).
	// 기획자가 각 라운드의 대상을 판단할 때 최근 확인된 취약점을 한눈에 볼 수 있도록 하세요. 전체 금액을 이전에 얻으려면 list_findings를 사용하십시오.
	// 각 항목에는 {id, summary, from_intent?}만 남습니다. from_intent는 이 취약점을 생성하려는 의도입니다.
	// 증거/자산/vulnclass/심각도/상태 등은 여전히 list_findings / node_detail(id)를 사용하여 얻을 수 있습니다.
	const findingListCap = 10
	findingList := make([]map[string]any, 0, findingListCap)
	for _, n := range vulnNodes {
		if len(findingList) >= findingListCap {
			break
		}
		var fp map[string]any
		_ = json.Unmarshal(n.Payload, &fp)
		m := map[string]any{"id": n.ID, "summary": fp["summary"]}
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 이 취약점의 목적은 무엇입니까?
		}
		findingList = append(findingList, m)
	}
	out["finding_list"] = findingList
	// Recent_facts: 폴딩되지 않은 팩트 중 최신 창(≤N,factNode는 id 기준 내림차순, 즉 최신 항목부터 정렬됨). 접힌
	// 다이제스트 및 여전히 콜드(숨겨짐)는 cold_digests로 이동하며 여기서는 반복되지 않습니다. 각 {id, summary, from_intent?,
	// 자신감?}; 증거 및 기타 세부정보를 확인하려면 node_detail(id)를 사용하세요. 이전에 번역하려면 list_facts를 사용하세요.
	const recentFactsCap = 20
	recentFacts := make([]map[string]any, 0, recentFactsCap)
	for _, n := range factNodes {
		if len(recentFacts) >= recentFactsCap {
			break
		}
		if hidden(n.ID) {
			continue // 다이제스트에 접혀 있고 여전히 차갑습니다. cold_digests를 참조하세요.
		}
		m := compactNode(n)
		if from := factFrom[n.ID]; from > 0 {
			m["from_intent"] = from // 이 사실은 어떤 의도에서 비롯된 것인가?
		}
		//자신감은 개요를 제공합니다. 계획자는 어떤 결론이 추론만 되었는지 한 눈에 확인할 수 있습니다(특히 부정적인 결론).
		// 견고한 사례로 받아들이지 마십시오); 증거가 더 길면 node_detail(id)에 맡기세요.
		var fp map[string]any
		if json.Unmarshal(n.Payload, &fp) == nil {
			if c, ok := fp["confidence"].(string); ok && c != "" {
				m["confidence"] = c
			}
		}
		recentFacts = append(recentFacts, m)
	}
	out["recent_facts"] = recentFacts
	// 최근_done_intents: 접히지 않은 종료된 인텐트의 최신 창입니다(≤N, 최근 완료는 ID별로 내림차순으로 정렬되었습니다).
	// 앞서 done_intents_total count + node_detail(id)를 참조하세요.
	const recentDoneCap = 12
	if len(recentDone) > recentDoneCap {
		recentDone = recentDone[:recentDoneCap]
	}
	out["recent_done_intents"] = compactIntents(recentDone, parentsOf, yieldsOf)
	// cold-digest §6.1: cold zone의 다이제스트 본문을 접고 최신 멤버 시간의 내림차순으로 상위 N을 가져옵니다. 잘린 오래된 다이제스트
	// Cold Zone의 유일한 출구가 무한히 늘어나는 것을 방지하려면 베어 ID(expand_digest로 확장 가능)만 제공하세요.
	const coldDigestsCap = 15
	if cds, more := coldDigestsRecent(t.ts, coldDigestsCap); len(cds) > 0 {
		out["cold_digests"] = cds // [{id, body, member_count}] - 본문 직접 읽기(§6.1)
		if len(more) > 0 {
			out["cold_digests_more"] = more // 이전 다이제스트의 잘린 ID; Expand_digest(id)로 확장
		}
	}
	// the original task (root) so the planner always has it, not just the
	// decomposed goals.
	if description, goal, err := t.ts.Root(); err == nil {
		out["task"] = map[string]any{"description": description, "goal": goal}
	}
	// Direct source tasks are a live, read-only blackboard view. Keep their
	// summaries in a separate field so their intents never enter this task's
	// frontier or get mistaken for locally claimable work.
	out["related_tasks"] = t.relatedTaskOverviews()
	// Coverage: 대략적인 자산 테스트 Coverage 참조 - 사실에 접한 범위(task_scope) 내의 자산
	// 비율 + by_type(유형별 총 개수/측정값). 테스트되지 않은 특정 자산은 요청 시 list_untested_assets를 호출하여 에이전트에 의해 결정됩니다. 작업 컨텍스트에만 있습니다.
	// 자산 커버리지 기능이 꺼진 경우(coverageDisabled): host_count(대상 호스트 수에 대해 인지된 정보)만 유지됩니다.
	// 컨텍스트를 오염시키고 유도하지 않도록 denominator/tested/pct/by_type/note와 같은 적용 범위 지표를 폐기하십시오.
	//숨겨진 add_task_scope/list_untested_assets.
	if t.as != nil && t.ts != nil && t.taskID > 0 {
		{
			m := map[string]any{}
			if !t.coverageDisabled {
				if cov, err := t.as.TaskCoverageWithSources(t.taskID); err == nil {
					m["denominator"] = cov.Denominator
					m["tested"] = cov.Tested
					m["by_type"] = cov.ByType
					m["note"] = "커버리지 자산 테스트 커버리지(인터페이스 등 다양한 관련 자산 포함), 대략적인 추정치, 참고용: 현재 작업 및 직접적으로 관련된 작업의 범위 및 사실 기준이 포함됩니다. 관련 범위는 읽기 전용입니다. 컨테이너형 자산/많은 수의 열거로 인해 낮아질 수 있으므로 테스트가 완료되었다고 가정하지 마십시오. add_task_scope를 사용하여 이 작업의 범위를 보완하고 list_untested_assets를 사용하여 테스트되지 않은 자산을 볼 수 있습니다[일반적으로 list_untested_assets를 호출하지 않고 작업에 따라 진행합니다];"
					if cov.Denominator == 0 {
						m["pct"] = nil
						m["status"] = "범위가 고정되지 않았습니다"
					} else {
						m["pct"] = cov.Pct
					}
				}
			}
			if hosts, err := t.as.HostsByTaskWithSources(t.taskID); err == nil {
				// 총 호스트 수만 제공하고 더 이상 호스트 목록을 graph_overview(대규모 작업의 경우 각 라운드)로 평면화하지 않습니다.
				// 많은 수의 문자열이 반복적으로 전달되므로 계획 결정에 대한 가치가 제한됩니다. 특정 호스트는 list_assets를 사용하여 요청 시 확인할 수 있습니다.
				m["host_count"] = len(hosts)
			}
			if len(m) > 0 {
				out["coverage"] = m
			}
		}
	}
	return out
}

func inheritedMap(m map[string]any, sourceTaskID int64) map[string]any {
	m["source_task_id"] = sourceTaskID
	m["inherited"] = true
	return m
}

const (
	relatedOverviewTotalTextRunes      = 48_000
	relatedOverviewMaxTextPerSource    = 8_000
	relatedOverviewMaxGoalsPerSource   = 8
	relatedOverviewMaxHintsPerSource   = 6
	relatedOverviewMaxFactsPerSource   = 12
	relatedOverviewMaxFindingsPerTask  = 6
	relatedOverviewMaxIntentsPerTask   = 8
	relatedOverviewMaxScopePerSource   = 12
	relatedOverviewMaxDigestsPerSource = 6
)

// overviewTextBudget bounds inherited prompt text while preserving a fair slice
// for every direct source. Full evidence remains available through the on-demand
// read tools, so truncation here does not discard persisted blackboard data.
type overviewTextBudget struct {
	remaining int
	truncated bool
}

func relatedOverviewBudgetForSources(sourceCount int) int {
	if sourceCount <= 0 {
		return 0
	}
	if sourceCount > db.MaxTaskSourceCount {
		sourceCount = db.MaxTaskSourceCount
	}
	perSource := relatedOverviewTotalTextRunes / sourceCount
	if perSource > relatedOverviewMaxTextPerSource {
		perSource = relatedOverviewMaxTextPerSource
	}
	return perSource
}

func (b *overviewTextBudget) take(value any, fieldLimit int) string {
	var text string
	switch value := value.(type) {
	case string:
		text = strings.TrimSpace(value)
	case nil:
		return ""
	default:
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "" {
		return ""
	}
	if b.remaining <= 0 || fieldLimit <= 0 {
		b.truncated = true
		return ""
	}
	runes := []rune(text)
	limit := fieldLimit
	if limit > b.remaining {
		limit = b.remaining
	}
	if len(runes) > limit {
		b.truncated = true
		if limit == 1 {
			text = "…"
		} else {
			text = string(runes[:limit-1]) + "…"
		}
		runes = []rune(text)
	}
	b.remaining -= len(runes)
	return text
}

func recentTerminalIntents(store *db.ExplorationStore, limit int) []*db.Node {
	if limit <= 0 {
		return []*db.Node{}
	}
	const batch = 300
	cursor := int64(0)
	out := make([]*db.Node, 0, limit)
	for len(out) < limit {
		page, more, err := store.ListByKindPage(db.KindIntent, cursor, batch)
		if err != nil || len(page) == 0 {
			break
		}
		for _, intent := range page {
			switch intent.State {
			case "done", "blocked", "exhausted", "stopped":
				out = append(out, intent)
			}
			if len(out) >= limit {
				break
			}
		}
		if !more {
			break
		}
		cursor = page[len(page)-1].ID
	}
	return out
}

// relatedTaskOverviews distills persistent blackboard state from direct source
// tasks. It intentionally reads each source's local store methods, never its own
// related sources, so inheritance is one level only.
func (t *ToolSet) relatedTaskOverviews() []map[string]any {
	sources, err := t.ts.DirectSourceStores()
	if err != nil {
		return []map[string]any{}
	}
	if len(sources) > db.MaxTaskSourceCount {
		sources = sources[:db.MaxTaskSourceCount]
	}
	perSourceTextBudget := relatedOverviewBudgetForSources(len(sources))
	out := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		ts := source.Store
		// §2 cross-task: render the source task's OWN folded view — fold out the
		// members it has already folded, and surface its cold_digests read-only.
		hidden := hiddenMembersFor(ts)
		budget := overviewTextBudget{remaining: perSourceTextBudget}
		item := map[string]any{
			"source_task_id": source.Task.TaskID,
			"inherited":      true,
			"task": map[string]any{
				"description": budget.take(source.Task.Description, 800),
				"goal":        budget.take(source.Task.Goal, 800),
				"status":      source.Task.Status,
			},
		}
		stats, statsErr := ts.Stats()

		edges, _ := ts.Edges(5000)
		parentsOf := map[int64][]int64{}
		yieldsOf := map[int64][]int64{}
		factFrom := map[int64]int64{}
		for _, edge := range edges {
			switch edge.Rel {
			case db.RelDerivedFrom, db.RelSpawns:
				parentsOf[edge.To] = append(parentsOf[edge.To], edge.From)
			case db.RelYields:
				yieldsOf[edge.From] = append(yieldsOf[edge.From], edge.To)
				factFrom[edge.To] = edge.From
			}
		}

		goals, _ := ts.ListByKind(db.KindGoal, relatedOverviewMaxGoalsPerSource)
		goalSummary := make([]map[string]any, 0, len(goals))
		for _, goal := range goals {
			var payload map[string]any
			_ = json.Unmarshal(goal.Payload, &payload)
			goalSummary = append(goalSummary, inheritedMap(map[string]any{
				"id": goal.ID, "state": goal.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["goals"] = goalSummary

		hints, _ := ts.ListByKind(db.KindHint, relatedOverviewMaxHintsPerSource)
		hintSummary := make([]map[string]any, 0, len(hints))
		for _, hint := range hints {
			var payload map[string]any
			_ = json.Unmarshal(hint.Payload, &payload)
			hintSummary = append(hintSummary, inheritedMap(map[string]any{
				"id": hint.ID, "state": hint.State, "text": budget.take(payload["text"], 400),
			}, source.Task.TaskID))
		}
		item["hints"] = hintSummary

		facts, _ := ts.ListByKind(db.KindFact, relatedOverviewMaxFactsPerSource)
		findings, _ := ts.ListByKind(db.KindFinding, relatedOverviewMaxFindingsPerTask)
		intentNodes, _ := ts.ListByKind(db.KindIntent, 300)
		terminalIntent := make(map[int64]bool, len(intentNodes))
		for _, intent := range intentNodes {
			terminalIntent[intent.ID] = inheritedIntentSummaryState(intent.State)
		}
		item["facts"] = len(facts)
		item["findings"] = len(findings)
		if statsErr == nil {
			item["facts"] = stats[db.KindFact]
			item["findings"] = stats[db.KindFinding]
			if stats[db.KindGoal] > len(goals) || stats[db.KindHint] > len(hints) ||
				stats[db.KindFact] > len(facts) || stats[db.KindFinding] > len(findings) {
				budget.truncated = true
			}
		}
		recentFindings := make([]map[string]any, 0, len(findings))
		for _, finding := range findings {
			entry := inheritedMap(compactFinding(finding), source.Task.TaskID)
			entry["summary"] = budget.take(entry["summary"], 400)
			recentFindings = append(recentFindings, entry)
		}
		item["recent_findings"] = recentFindings
		recentFacts := make([]map[string]any, 0, len(facts))
		for _, fact := range facts {
			if hidden(fact.ID) {
				continue // folded into this source's cold_digests — shown there (§2/§6.2)
			}
			m := inheritedMap(compactNode(fact), source.Task.TaskID)
			m["summary"] = budget.take(m["summary"], 400)
			if from := factFrom[fact.ID]; from > 0 && terminalIntent[from] {
				m["from_intent"] = from
			}
			var payload map[string]any
			if json.Unmarshal(fact.Payload, &payload) == nil {
				if confidence, ok := payload["confidence"].(string); ok && confidence != "" {
					m["confidence"] = confidence
				}
			}
			recentFacts = append(recentFacts, m)
		}
		item["recent_facts"] = recentFacts

		recentDoneRaw := recentTerminalIntents(ts, relatedOverviewMaxIntentsPerTask)
		recentDone := recentDoneRaw[:0] // in-place filter: drop this source's folded intents (§2)
		for _, intent := range recentDoneRaw {
			if hidden(intent.ID) {
				continue
			}
			recentDone = append(recentDone, intent)
		}
		for _, intent := range recentDone {
			intent.Inherited = true
			intent.SourceTaskID = source.Task.TaskID
		}
		intentResults := compactIntents(recentDone, parentsOf, yieldsOf)
		for i, intent := range recentDone {
			intentResults[i]["summary"] = budget.take(intentResults[i]["summary"], 400)
			acts, _, err := ts.ActivityPageForTerminalIntent(intent.ID, 0, 20)
			if err != nil {
				continue
			}
			var resultSummary, textFallback string
			for _, activity := range acts {
				switch activity.Kind {
				case "result":
					resultSummary = activity.Summary
				case "text":
					textFallback = activity.Summary
				}
			}
			if resultSummary == "" {
				resultSummary = textFallback
			}
			if resultSummary != "" {
				intentResults[i]["result_summary"] = budget.take(resultSummary, 800)
			}
		}
		item["recent_intent_results"] = intentResults
		// §2 cross-task: the source task's folded cold region, read-only, newest-member
		// first & capped like the current task's. Members (and overflow digests) are
		// resolvable via expand_digest(id)/node_detail(id), which search source tasks.
		if cds, more := coldDigestsRecent(ts, relatedOverviewMaxDigestsPerSource); len(cds) > 0 {
			for _, cd := range cds {
				cd["inherited"] = true
				cd["source_task_id"] = source.Task.TaskID
			}
			item["cold_digests"] = cds
			if len(more) > 0 {
				item["cold_digests_more"] = more // 이전 다이제스트의 잘린 ID; Expand_digest(id) 확장
			}
		}
		if statsErr == nil {
			item["node_stats"] = stats
		}

		if t.as != nil {
			if scopeRows, err := t.as.ListTaskScope(source.Task.TaskID); err == nil && len(scopeRows) > 0 {
				scopeCount := len(scopeRows)
				if len(scopeRows) > relatedOverviewMaxScopePerSource {
					scopeRows = scopeRows[:relatedOverviewMaxScopePerSource]
					budget.truncated = true
				}
				scope := make([]map[string]any, 0, len(scopeRows))
				for _, row := range scopeRows {
					entry := map[string]any{"kind": row.Kind, "source": budget.take(row.Source, 300)}
					switch {
					case row.Domain != "":
						entry["value"] = budget.take(row.Domain, 400)
					case row.Net != "":
						entry["value"] = budget.take(row.Net, 400)
					case row.Value != "":
						entry["value"] = budget.take(row.Value, 400)
					case row.CompanyID != nil:
						entry["company_id"] = *row.CompanyID
					}
					scope = append(scope, entry)
				}
				item["asset_scope"] = scope
				item["asset_scope_count"] = scopeCount
			}
			if coverage, err := t.as.TaskCoverage(source.Task.TaskID, source.Task.ExplorationID); err == nil {
				item["asset_coverage"] = map[string]any{
					"denominator": coverage.Denominator,
					"tested":      coverage.Tested,
					"pct":         coverage.Pct,
					"by_type":     coverage.ByType,
				}
			}
		}
		if budget.truncated {
			item["summary_truncated"] = true
		}
		out = append(out, item)
	}
	return out
}

func inheritedIntentSummaryState(state string) bool {
	switch state {
	case "done", "blocked", "exhausted", "stopped":
		return true
	default:
		return false
	}
}

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	return m
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if n.Inherited {
		inheritedMap(m, n.SourceTaskID)
	}
	if vc, ok := p["vulnclass"]; ok && vc != nil && vc != "" {
		m["vulnclass"] = vc
	}
	if sv, ok := p["severity"]; ok && sv != nil && sv != "" {
		m["severity"] = sv
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool {
	return t.readExpTool("list_findings", "이 작업 및 직접 관련된 작업의 [확인된 취약점]을 나열합니다(컴팩트: id+task_id+intent_id+vulnclass+severity+summary+status). 연관된 작업 항목은 source_task_id/inherited=true를 가지며 읽기 전용입니다. 여기에는 취약점만 포함되어 있습니다. 일반적인 사실은 list_facts를 사용하고 자세한 내용은 node_detail(id)을 사용하세요.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _ := t.ts.ListByKindWithSources(db.KindFinding, 500)
			if err := t.ts.PopulateFindingTrafficIDs(f); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			intentOf, _ := t.ts.FindingIntentsWithSources() // ID 찾기 -> 이를 생성한 인텐트 ID
			taskID := t.taskID
			if taskID <= 0 {
				taskID, _ = t.ts.TaskID()
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				m := compactFinding(n)
				if n.FindingID > 0 {
					m["finding_id"], m["finding_node_id"], m["traffic_count"] = n.FindingID, n.ID, n.TrafficCount
				}
				if n.Inherited {
					m["task_id"] = n.SourceTaskID
				} else {
					m["task_id"] = taskID
				}
				if iid, ok := intentOf[n.ID]; ok {
					m["intent_id"] = iid
				}
				out = append(out, m)
			}
			return jsonResult(out)
		})
}

// factsPageSize is the default page size for list_facts. Facts pile up on long
// tasks; returning all of them at once (the old behaviour) could blow up the
// context, so default to the newest page and let the agent page/filter for more.
const factsPageSize = 20

func (t *ToolSet) listFacts() actool.CoreTool {
	return t.readExpTool("list_facts", "이 작업 및 직접적으로 관련된 작업의 [탐색 사실/결론]을 최신순으로 페이지에 표시하세요(컴팩트: id+summary+status, 요약이 너무 길면 잘립니다. 전체 텍스트를 보려면 node_detail(id)를 사용하세요). 모든 매개 변수는 선택 사항입니다. 제한(기본값 20, 상한 100), 이전(커서, 이전 페이지를 전달하여 반환된 next_before는 이전 페이지를 사용합니다. /0 = 최신 페이지 생략), q(요약 키워드로 필터링). {facts, total, has_more, next_before} 반환: total은 필터링된 합계이며, has_more=true인 경우 next_before를 사용하여 페이지 넘기기를 계속합니다. 연관된 작업 항목은 source_task_id/inherited=true이며 읽기 전용입니다. 취약점은 list_findings를 참조하세요.",
		obj(map[string]any{
			"limit":  intp("반품 횟수, 기본값 20, 상한 100"),
			"before": intp("페이지 커서: 이 값보다 작은 ID를 가진 오래된 사실만 반환합니다. 생략 또는 0 = 최신 페이지"),
			"q":      str("사실 요약 키워드로 필터링합니다(대소문자를 구분하지 않음). 생략 = 필터링 없음"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Limit  int    `json:"limit"`
				Before int64  `json:"before"`
				Q      string `json:"q"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = factsPageSize
			}
			if limit > 100 {
				limit = 100
			}
			f, hasMore, total, err := t.ts.ListByKindPageWithSources(db.KindFact, a.Before, limit, strings.TrimSpace(a.Q))
			if err != nil {
				return actool.Result{}, err
			}
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				out = append(out, compactFact(n))
			}
			res := map[string]any{"facts": out, "total": total, "has_more": hasMore}
			if hasMore && len(f) > 0 {
				res["next_before"] = f[len(f)-1].ID // 다음 페이지(이전)를 얻으려면 다시 전달하세요.
			}
			return jsonResult(res)
		})
}

// factSummaryMax caps a fact summary in list_facts output. Facts carry one-line
// conclusions, but nothing enforces brevity; a runaway summary must not bloat a
// whole page. Full text stays available via node_detail(id).
const factSummaryMax = 160

// compactFact is compactNode with the summary rune-capped for list_facts, so a
// page of facts stays bounded regardless of how long any single summary grew.
func compactFact(n *db.Node) map[string]any {
	m := compactNode(n)
	if s, ok := m["summary"].(string); ok && len([]rune(s)) > factSummaryMax {
		m["summary"] = string([]rune(s)[:factSummaryMax]) + "…"
		m["summary_truncated"] = true
	}
	return m
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return t.readExpTool("node_detail", "id를 누르면 이 작업 또는 직접 관련된 작업의 [탐색 그래프 노드]의 전체 내용을 가져옵니다. 상속된 노드는 source_task_id/inherited=true를 가지며 읽기 전용입니다. list_facts/list_findings/graph_overview에서 반환된 탐색 노드 ID만; 자산의 경우 list_assets/asset_neighbors를 사용하세요.",
		obj(map[string]any{"id": idp("탐색 그래프 노드 ID(비asset_ids)")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("ID가 필요합니다"), nil
			}
			n, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("탐색 노드 %d을(를) 찾을 수 없습니다. 자산을 확인하고 싶다면 list_assets/asset_neighbors를 이용해주세요. (자산과 탐색 노드는 서로 다른 ID 공간에 있고, asset_ids는 node_detail에 전달할 수 없습니다.)", id)), nil
			}
			if err := t.ts.PopulateFindingTrafficIDs([]*db.Node{n}); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(n) // full payload incl. detail / evidence, plus explicit finding IDs
		})
}

// --- planner write tools ---

// intentItem은 add_intent 배치/단일 항목의 탐색 방향입니다.
type intentItem struct {
	Summary   string            `json:"summary"`
	AssetIDs  []json.RawMessage `json:"asset_ids"`
	ParentIDs []json.RawMessage `json:"parent_ids"`
	Priority  int               `json:"priority"`
}

// addOneIntent는 인텐트 노드를 생성하고 이를 업스트림 혈통에 연결하여 ID를 반환합니다.
// 제약 조건: 의도는 확인된 지식에만 고정될 수 있습니다. 각 parent_id는 기존 사실/결과여야 합니다.
// 노드(다른 의도/목표/프롬프트에 연결할 수 없음) 최상위 수준에서 새 방향에 대해 parent_ids를 비워두고 하단에서 원본 사실을 연결합니다.
// 이러한 방식으로 "각 의도는 팩트 노드에 연결되고 허공에서 계획되기보다는 검색 중심으로 이루어집니다"는 생성 경로에서 시행됩니다.
func (t *ToolSet) addOneIntent(it intentItem) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("요약은 비워둘 수 없습니다")
	}
	// 앵커 포인트를 먼저 확인하십시오(고아 의도를 남기는 잘못된 앵커 포인트를 피하기 위해 노드를 구축하기 전에).
	parents := pidList(it.ParentIDs)
	for _, pidv := range parents {
		n, err := t.ts.GetNodeWithSources(pidv)
		if err != nil || n == nil {
			return 0, fmt.Errorf("parent_id %d이(가) 이 작업에 존재하지 않거나 작업과 직접 관련되어 있습니다. parent_ids는 기존 [사실/조사 결과] 노드 ID여야 합니다. 최상위 수준의 새 방향에 대해서는 parent_ids를 비워 두십시오.", pidv)
		}
		if n.Kind != db.KindFact && n.Kind != db.KindFinding {
			return 0, fmt.Errorf("parent_id %d는 %q 노드이며 인텐트 앵커로 사용할 수 없습니다. 인텐트는 확인된 [사실/발견]에만 고정될 수 있으며 의도/목표/프롬프트에 매달릴 수 없습니다. parent_ids를 비워두세요", pidv, n.Kind)
		}
	}
	priority := it.Priority
	if priority == 0 {
		priority = 5
	}
	anchors := pidList(it.AssetIDs)
	//자산 차단: 인텐트에 바인딩된 자산이 시스템 자산 차단 규칙을 위반하는 경우 인텐트 발행이 금지됩니다.
	if t.as != nil && len(anchors) > 0 {
		hits, err := t.as.CheckAssetsIntercept(t.taskID, anchors)
		if err != nil {
			return 0, fmt.Errorf("자산 가로채기 확인 실패: %w", err)
		}
		if len(hits) > 0 {
			var b strings.Builder
			fmt.Fprintf(&b, "인텐트 \"%s\"에 바인딩된 자산이 테스트 범위 확인을 통과하지 못했습니다. 관련 자산 테스트를 중지하십시오:", it.Summary)
			for _, h := range hits {
				fmt.Fprintf(&b, "\n - %s", h.Describe())
			}
			return 0, fmt.Errorf("%s", b.String())
		}
	}
	payload := map[string]any{"summary": it.Summary}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	id, err := t.ts.AddIntent(payload, priority, anchors, "planner")
	if err != nil {
		return 0, err
	}
	// upstream lineage: link each (validated) fact/finding parent → this intent, so
	// "multiple facts combine into one new intent" is expressible.
	for _, parent := range parents {
		_ = t.ts.Link(parent, db.RelDerivedFrom, id)
	}
	// a top-level intent (no explicit parent) connects to the origin fact, so every
	// intent still traces back to a fact node — at task start the only fact is the
	// origin, and the first intents derive from it.
	if len(parents) == 0 {
		if origin, _ := t.ts.OriginFactID(); origin > 0 {
			_ = t.ts.Link(origin, db.RelDerivedFrom, id)
		}
	}
	return id, nil
}

func (t *ToolSet) addIntent() actool.CoreTool {
	return t.writeExpTool("add_intent", "[탐험 방향]을 생성하여 프론티어에 쓰고, 탐색 링크에 연결합니다. 의도는 고정된 형태가 아닌 열린 탐색 방향입니다. 탐색/검증/이용할 내용을 요약 문장을 사용하여 자유롭게 설명합니다.\n"+
		"★우선순위 배치: 한 라운드에서 필터링된 여러 개의 새로운 길찾기가 인텐트 배열에 입력되어 한 번에 제출됩니다(하나씩 호출하는 것보다 왕복 횟수가 절약됩니다). 인텐트와 동일한 길이 및 순서로 ID 배열을 반환합니다(실패 항목 ID=0, 자세한 내용은 오류 참조). 단일 기사의 경우 의도가 생략되고 최상위 요약에 직접 제공됩니다.",
		obj(map[string]any{
			"intents":    map[string]any{"type": "array", "description": "[먼저 사용하세요] 추가할 탐색 방향 배열이 순서대로 처리됩니다. 각 요소 필드는 아래의 최상위 필드(summary/asset_ids/parent_ids/priority)와 동일합니다. 반환된 ID는 이 배열과 길이 및 순서가 동일합니다.", "items": map[string]any{"type": "object"}},
			"summary":    str("[단일 기사] 이 탐구 방향을 무엇을 해야 할지 + 왜 해야 하는지 한 문장으로 설명하세요. 방향을 명확하게 작성하면 asset_ids에 의존하지 않습니다."),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "이 방향으로 테스트/공격할 [대상 asset_ids](**통과 시도**, 0/1/다중; 탐색 노드 ID가 아니라 list_assets에서 반환한 asset_ids입니다): 이 탐색 방향이 대상으로 삼는 자산(사이트/인터페이스/매개변수/호스트 등). 방향이 특정 특정 자산을 중심으로 돌아가는 한 업로드되어야 합니다. 이는 \"이 탐색의 목표는 무엇입니까?\"라는 구조화된 마크업입니다. 중복을 커버하고 의도를 자산 링크에 연결하는 데 사용됩니다. 순수 글로벌 정찰이고 실제로 특정 대상 자산이 없는 경우에만 이 항목을 공백으로 두십시오."},
			"parent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "업스트림 앵커 ID(선택 사항, 0/1/다중): 이 방향은 [확인된 사실/조사 결과]에서 파생됩니다. **기존 사실/발견 노드 ID만 입력할 수 있으며 의도/목표/프롬프트는 입력할 수 없습니다** - 의도는 무작정 계획하기보다는 발견을 통해 확인된 지식에 고정되어야 합니다. 여러 사실이 함께 새로운 의도를 생성하는 경우 여러 사실을 전달하세요. 최상위 신규 정찰 방향은 비워두세요. (과제의 원래 사실과 자동으로 연결됩니다.)"},
			"priority":   intp("우선순위 0-10, 기본값 5"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Intents    []intentItem `json:"intents"`
				intentItem              // 단일 모드: 상위 요약/asset_ids/parent_ids/priority
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Intents) > 0
			items := a.Intents
			if !batch {
				items = []intentItem{a.intentItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			createdAny := false
			for i, it := range items {
				id, err := t.addOneIntent(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				createdAny = true
			}

			// 주체가 직접 의도를 발동 → 작업이 완료되면(오픈 골이 없는 골 없는 지점) 넣어줍니다.
			// 작업자가 이 의도를 실행할 수 있도록 다시 실행을 시작하세요. 이력서태스크는 대표 상담원의 채팅으로만 접속됩니다.
			// (SetResumeTask); 플래너의 ToolSet은 nil이므로 플래너는 이 기간 동안 add_intent를 조정합니다.
			// no-op는 정상적인 생산 의도에 영향을 미치지 않습니다. 인텐션 노드가 그 위에 구축(열려)되어 있으며 부활 시 실수로 배수되지 않습니다.
			if createdAny && t.resumeTask != nil {
				t.resumeTask()
			}

			if !batch { // 단일: 원래 반환을 유지합니다.
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("intent created: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listGoals() actool.CoreTool {
	return t.readExpTool("list_goals", "이 작업의 대상 노드와 해당 상태(열림/충족)를 나열하여 달성 여부를 확인하세요.",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _ := t.ts.ListByKind(db.KindGoal, 100)
			return jsonResult(g)
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return t.writeExpTool("prove_goal", "발견/사실이 특정 목표가 달성되었음을 증명한다고 판단할 때 호출됩니다. 증거 노드를 대상 노드에 연결하고 목표 달성을 표시합니다.",
		obj(map[string]any{
			"goal_id":     idp("대상 노드 ID"),
			"evidence_id": idp("이를 증명하는 검색/사실 노드 ID"),
			"reason":      str("이 증거가 목표를 달성하는 이유"),
		}, "goal_id", "evidence_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID     json.RawMessage `json:"goal_id"`
				EvidenceID json.RawMessage `json:"evidence_id"`
				Reason     string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			goal, ev := pid(a.GoalID), pid(a.EvidenceID)
			if goal == 0 || ev == 0 {
				return actool.Errorf("goal_id 및 evidence_id가 필요합니다"), nil
			}
			goalNode, err := t.ts.GetNode(goal)
			if err != nil || goalNode == nil || goalNode.Kind != db.KindGoal {
				return actool.Errorf("goal_id는 이 작업의 대상 노드여야 합니다(관련된 작업 대상은 읽기 전용입니다)"), nil
			}
			evidenceNode, err := t.ts.GetNodeWithSources(ev)
			if err != nil || evidenceNode == nil || (evidenceNode.Kind != db.KindFact && evidenceNode.Kind != db.KindFinding) {
				return actool.Errorf("evidence_id는 이 작업의 사실/취약점 노드이거나 직접 관련된 작업이어야 합니다"), nil
			}
			_ = t.ts.Link(ev, db.RelProves, goal)
			_ = t.ts.SetNodeState(goal, "met")
			// 목표 달성이 표시될 때마다 이 작업의 [모든 목표]가 달성되었는지 확인하세요. 그렇다면 자동으로 결정됩니다.
			// 작업이 완료되고(GoalMet이 설정됨) goal_met을 명시적으로 조정하기 위해 모델에 의존할 필요가 없습니다.
			if goals, err := t.ts.ListByKind(db.KindGoal, 1000); err == nil && len(goals) > 0 {
				allMet := true
				for _, g := range goals {
					if g.State != "met" {
						allMet = false
						break
					}
				}
				if allMet {
					t.GoalMet = true
					t.Reason = fmt.Sprintf("모든 %d개 목표 달성(마지막으로 목표 %d에 의해 실행됨)", len(goals), goal)
					return actool.Text(fmt.Sprintf("목표 %d이(가) 달성된 것으로 표시되었습니다. 이 작업의 모든 목표가 달성되었으며 작업 완료가 자동으로 결정됩니다.", goal)), nil
				}
			}
			return actool.Text(fmt.Sprintf("goal %d marked met", goal)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return writeTool("goal_met", "[즉시 전체 미션 종료] - [미션의 모든 목표가 실제로 달성되었고 전체적인 결론이] 확인된 경우에만 조정됩니다. (미션이 [전체적으로] 완료되었습니다. 목표/플래그/취약점 중 하나만 달성하면 [계산되지 않음] - 이 경우에는 prove_goal로 목표를 표시하면 됩니다. ⚠️\"이번 계획 라운드 종료\"에는 사용되지 않습니다. 이 라운드에서 보낼 새로운 의도가 없거나 작업자 출력을 기다리고 있는 경우에는 [라운드를 직접 종료하고 이 도구를 조정하지 마세요](의도가 0인 것은 완전히 정상입니다). 일반적인 판단에서는 prove_goal을 사용하여 목표를 하나씩 증명하는 것이 우선순위입니다. goal_met은 하나씩 증명을 우회하고 전체적인 상황을 바로 종료하기 위한 수단일 뿐입니다.",
		obj(map[string]any{"reason": str("성취 이유(목표가 진정으로 달성되었다는 증거여야 하며, '이번 라운드에 새로운 방향이 없습니다' 등 이번 라운드를 종료하는 이유가 될 수 없음)")}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Reason string }
			_ = json.Unmarshal(in, &a)
			t.GoalMet = true
			t.Reason = a.Reason
			return actool.Text("acknowledged: goal marked met"), nil
		})
}

// --- worker write tools ---

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "확인된 취약점을 기록하고 증거를 사용하여 명령 출력, 로그 및 기타 검증 가능한 증거를 제공합니다. 작업 컨텍스트는 현재intent_id를 전달합니다. 반환된 finding_id는 개별 취약점 레코드 ID이고 finding_node_id는 검색 노드 ID입니다(첫 번째 행에는 노드 번호가 있음).", obj(map[string]any{
		"vulnclass": str("취약점 카테고리"), "name": str("취약점 이름"), "severity": str("critical|high|medium|low"), "summary": str("검색 요약"),
		"intent_id": idp("현재 작업의 인텐트 ID"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "영향을 받은 asset_ids"},
		"evidence":         str("증거/PoC 텍스트"),
		"evidence_hint_id": idp("선택 사항: 이 작업의 취약점에 해당하는 프롬프트 노드 ID는 구조화된 traffic_refs를 자동으로 전달합니다. 상속 프롬프트나 다른 취약점의 프롬프트는 참조할 수 없습니다."),
		"traffic_refs": map[string]any{"type": "array", "description": "선택사항; HTTP/HTTPS 취약점을 먼저 검색하여 요청/응답이 실제로 취약점 결론을 지원하는지 하나씩 검증한 후 반복되는 순서대로 실제 ID를 입력합니다. TCP 및 기타 non-HTTP 취약점이 수집되지 않거나 정확한 기록을 찾을 수 없는 경우 []를 생략하거나 전달하여 신고를 차단하지 않습니다. 그 이유를 증거로 설명할 수 있고, 검증 가능한 다른 증거를 제시할 수 있습니다. ID를 추측하거나, 도메인/시간별로 연관을 추론하거나, 패치 패킷에 대해서만 조사를 반복하지 마십시오. 목적 기준선 정상 통제 / 취약점 입증 / 검증 보완 검증 / 보조 증거 지원.",
			"items": obj(map[string]any{"traffic_id": str("traffic_search에서 반환된 실제 트래픽 ID"), "role": map[string]any{"type": "string", "enum": []string{"baseline", "proof", "verification", "supporting"}}, "note": str("이 트래픽은 취약점 결론을 어떻게 뒷받침합니까")}, "traffic_id")},
	}, "vulnclass", "severity", "summary"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		var a struct {
			VulnClass, Name, Severity, Summary, Evidence string
			IntentID                                     json.RawMessage   `json:"intent_id"`
			AssetIDs                                     []json.RawMessage `json:"asset_ids"`
			TrafficRefs                                  []db.TrafficRef   `json:"traffic_refs"`
			EvidenceHintID                               json.RawMessage   `json:"evidence_hint_id"`
		}
		if err := json.Unmarshal(in, &a); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.ts == nil {
			return actool.Errorf("report_finding에는 작업 컨텍스트가 필요합니다. 플랫폼 대화의 경우 add_task_hint를 통해 해당 작업에 취약점을 넘겨주시고, 작업 에이전트가 등록할 기존 traffic_refs를 프롬프트에 전달하세요. 등록된 취약점은 bind_finding_traffic을 사용하여 패치할 수 있습니다."), nil
		}
		// Auto-binding off: ignore the evidence params instead of rejecting the call.
		// stripTrafficParameters already removes them from the advertised schema, but
		// models routinely emit fields anyway — failing here would discard a confirmed
		// finding over a stray parameter. The success path below reports evidence_status
		// "닫혀 있음, 페이지에서 수동으로 연결할 수 있음" 메모가 있는 "not_bound"는 호출자에게 필요한 것입니다.
		if !findingTrafficBindingEnabled() {
			a.TrafficRefs, a.EvidenceHintID = nil, nil
		}
		if len(a.EvidenceHintID) > 0 && pid(a.EvidenceHintID) <= 0 {
			return actool.Errorf("evidence_hint_id는 유효한 힌트 노드 ID여야 합니다. 핸드오버 힌트가 없으면 생략"), nil
		}
		refs, err := t.findingRefsFromHint(pid(a.EvidenceHintID), a.TrafficRefs)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		input := db.RecordFindingInput{TaskID: t.taskID, ExplorationID: t.ts.ID(), IntentID: pid(a.IntentID), VulnClass: a.VulnClass, Name: a.Name, Severity: a.Severity, Summary: a.Summary, Evidence: a.Evidence, Worker: t.worker, AssetIDs: pidList(a.AssetIDs)}
		var recorded *db.RecordedFinding
		if t.findingRecorder != nil {
			recorded, err = t.findingRecorder.Record(ctx, input, refs)
		} else if len(refs) > 0 {
			return actool.Errorf("트래픽 증거를 저장할 수 없습니다. 등록되지 않은 취약점"), nil
		} else {
			recorded, err = t.ts.RecordFinding(ctx, input)
		}
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if t.notifyFinding != nil {
			iid := input.IntentID
			if iid <= 0 {
				iid = t.ownerNode
			}
			t.notifyFinding(iid, a.Summary)
		} else if t.notify != nil {
			t.notify()
		}
		t.writes.Findings++
		// Keep the first line's node-ID contract for existing reporter triggers.
		for i := range recorded.Traffic.Bindings {
			recorded.Traffic.Bindings[i].Snapshot.ReqHead = ""
			recorded.Traffic.Bindings[i].Snapshot.RespHead = ""
		}
		result := struct {
			*db.RecordedFinding
			EvidenceStatus string `json:"evidence_status"`
			EvidenceNote   string `json:"evidence_note,omitempty"`
		}{RecordedFinding: recorded, EvidenceStatus: "bound"}
		if len(recorded.Traffic.Bindings) == 0 {
			result.EvidenceStatus = "not_bound"
			result.EvidenceNote = "취약점이 저장되었으며 트래픽이 바인딩되지 않았습니다. TCP/no-packet 상황은 정상적으로 계속될 수 있습니다. 이미 검증된 HTTP 트래픽이 있는 경우, 사용 가능한 bind_finding_traffic 또는 취약점 페이지를 사용하여 패치한 후 증거 전달을 완료하세요. 취약점을 재현하지 마십시오."
			if !findingTrafficBindingEnabled() {
				result.EvidenceNote = "취약점이 저장되었습니다. 에이전트의 트래픽 자동 바인딩이 해제되었으며 페이지에서 트래픽을 수동으로 연결할 수 있습니다."
			}
		}
		raw, _ := json.Marshal(result)
		return actool.Text(fmt.Sprintf("finding recorded: %d\n%s", recorded.NodeID, raw)), nil
	})
}

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// FactItem은 Record_fact 배치/단일 항목의 팩트입니다.
type factItem struct {
	Summary    string            `json:"summary"`
	Detail     string            `json:"detail"`
	Evidence   string            `json:"evidence"`   // 결론을 뒷받침하고 후속 검증을 용이하게 하는 주요 증거 한 줄(명령 + 주요 출력 줄)
	Confidence string            `json:"confidence"` // observed(직접 확인) | inferred(정황상 추론)
	IntentID   json.RawMessage   `json:"intent_id"`
	AssetIDs   []json.RawMessage `json:"asset_ids"`
}

// RecordOneFact는 팩트 노드를 작성하고 이를 인텐트(의도→수익률→팩트)에 연결합니다. defaultIntent는
// 일괄 처리의 기본 인텐트(이 문서에서intent_id가 제공되지 않은 경우 사용됨)
func (t *ToolSet) recordOneFact(it factItem, defaultIntent int64) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("요약은 비워둘 수 없습니다")
	}
	payload := map[string]any{"summary": it.Summary}
	if it.Detail != "" {
		payload["detail"] = it.Detail
	}
	if e := strings.TrimSpace(it.Evidence); e != "" {
		payload["evidence"] = e
	}
	if c := strings.TrimSpace(it.Confidence); c != "" {
		payload["confidence"] = c
	}
	intent := pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	if intent > 0 {
		node, err := t.ts.GetNode(intent)
		if err != nil || node == nil || node.Kind != db.KindIntent {
			return 0, fmt.Errorf("intent_id는 이 작업의 인텐트여야 합니다(관련된 작업 인텐트는 읽기 전용입니다)")
		}
	}
	// a fact is its OWN node kind (distinct from a vuln finding).
	id, err := t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, pidList(it.AssetIDs))
	if err != nil {
		return 0, err
	}
	if intent > 0 {
		_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> fact
	}
	t.writes.Facts++
	return id, nil
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return t.writeExpTool("record_fact", "탐색 [사실/결론]을 탐색 그래프에 작성하고 이를 생성한 의도(intent_id)와 연결합니다. 지문/열거 등을 포함한 탐색 결과를 기록하는 데 사용됩니다. [긍정적 결론] 및 '포트 폐쇄'/'매개변수를 삽입할 수 없음'/'로그인 입구를 찾을 수 없음' 등 [부정적 결론].\n"+
		"⚠️한 번의 탐색에서 여러 관찰 내용을 [하나의 사실로 요약]해야 합니다. 여러 조각으로 나누지 마십시오. 하나의 사실로 병합될 수 있다면 하나의 사실로 표현해 보십시오: 요약 = 이 결론의 요약 문장, 세부 사항 = 관련 세부 사항(여러 특정 항목을 포함할 수 있음). 예: 지문 조사 의도 → 사실{summary:'기술 스택과 응답 특성 확인', detail:'확인한 버전과 응답 근거'}. 한 의도에서는 보통 사실 하나만 기록합니다. 너무 많이 쪼개면 그래프가 무한히 늘어나게 됩니다.\n"+
		"★사실 배열은 한 번에 여러 개의 [서로 다른] 결론을 작성하는 데 사용됩니다(각 항목마다 intent_id를 생략할 수 있으며 기본적으로 최상위 intent_id가 사용됩니다). 사실과 동일한 길이와 순서로 ids 배열을 반환합니다.\n"+
		"⚠️도구 출력에서 [실제로 확인된] 결론만 작성하고 결정하지 마세요. 부정확한 결론으로 인해 그래프가 오염되는 것을 방지하기 위해 증거와 신뢰도가 사용됩니다.\n"+
		"  · 증거=【한 줄】이 결론을 뒷받침하는 핵심 증거(명령 + 이를 가장 잘 증명하는 출력 한두 줄), **간결해야 합니다** - 세부 사항은 이미 자세히 설명되어 있으므로 여기서는 출력의 큰 부분에 집착하지 마세요.\n"+
		"  · confidence=observed(도구 출력에서 직접 확인) | inferred(정황상 추론). 값은 영어 그대로 입력하세요.\n"+
		"  · **부정적 결론**(주입 불가, 포트 닫힘, 진입점 없음 등)은 관찰한 사실과 잠정적 해석을 구분하세요. 실제로 확인한 내용과 evidence를 남기고, 방향을 중단할지는 플래너가 전체 상황을 보고 결정합니다. 검증이 부족하거나 증거가 약하면 inferred, 합리적인 검증 수단을 다 썼고 직접 확인했다면 observed로 표시하세요.",
		obj(map[string]any{
			"facts":      map[string]any{"type": "array", "description": "[다양한 결론이 있을 때 사용] 사실 배열, 요소 필드는 아래의 최상위 필드(요약/세부 정보/evidence/confidence/intent_id/asset_ids)와 동일합니다. intent_id가 생략되면 최상위 intent_id가 사용됩니다. 반환된 ID는 이 배열과 길이 및 순서가 동일합니다.", "items": map[string]any{"type": "object"}},
			"summary":    str("[결론] 이번 탐구의 결론(세부사항 요약)"),
			"intent_id":  idp("이 사실을 생성한 의도 ID(귀하가 받은 의도, 일괄적으로 각 항목의 기본값으로 사용됨)"),
			"detail":     str("이 사실과 관련된 세부정보: 이 탐색에서 얻은 여러 관찰 내용을 여기에 기록하세요"),
			"evidence":   str("[한 줄] 핵심 증거: command + 결론을 가장 잘 증명하는 출력 한 줄 또는 두 줄. 간결하게 작성하고 출력의 큰 부분을 고집하지 마십시오(자세한 내용을 남겨주세요)."),
			"confidence": str("observed(도구 출력에서 직접 확인) | inferred(정황상 추론). 영어 값을 입력하고, 부정적 결론도 증거에 맞게 표시하세요."),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "관련 자산 ID(선택 사항, 여러 개 가능): 이 사실과 관련된 자산"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Facts    []factItem `json:"facts"`
				factItem            // 단일 모드 + 일괄 기본 Intent_id
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Facts) > 0
			items := a.Facts
			if !batch {
				items = []factItem{a.factItem}
			}
			defaultIntent := pid(a.factItem.IntentID) // 최상위 Intent_id = 배치 기본값

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.recordOneFact(it, defaultIntent)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // 단일: 원래 반환을 유지합니다.
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("fact recorded: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type hintItem struct {
	Text        string            `json:"text"`
	AssetIDs    []json.RawMessage `json:"asset_ids"`
	TrafficRefs []db.TrafficRef   `json:"traffic_refs"`
}

// addOneHint는 자산을 고정하고 ID를 반환할 수 있는 힌트 노드(활성/인간)를 탐색 그래프에 걸어 놓습니다.
func (t *ToolSet) addOneHint(it hintItem) (int64, error) {
	if len(it.TrafficRefs) > 0 && !findingTrafficBindingEnabled() {
		return 0, fmt.Errorf("에이전트의 자동 트래픽 바인딩이 꺼졌고, traffic_refs를 전달하는 프롬프트가 저장되지 않았습니다. 시스템 설정에서 켜거나 텍스트를 전달할 수 있습니다")
	}
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("텍스트는 비워둘 수 없습니다")
	}
	var anchors []int64
	for _, raw := range it.AssetIDs {
		if tid := pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	refs, err := db.NormalizeTrafficRefs(it.TrafficRefs)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{"text": it.Text}
	if len(refs) > 0 {
		payload["traffic_refs"] = refs
	}
	// 여기서는 planner를 하나씩 깨우는 작업이 수행되지 않습니다. 전체 배치가 작성된 후 addHint가 한 번(프롬프트 텍스트와 함께) 트리거됩니다.
	// 플래너의 트리거 라인을 하나씩 새로 고치려면 한 번에 여러 힌트를 추가하는 것을 피하세요.
	return t.ts.AddNode(db.KindHint, payload, 0, "active", "human", anchors)
}

type goalItem struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass"`
}

// addOneGoal은 탐색 그래프에 목표 노드(열림)를 걸어 놓습니다. 즉, 작업 루트(원본 사실, 상대 생성)에 연결됩니다.
// Origin은 t.worker(기본 시스템): 목표, 디스어셈블러 "goals"에 의해 작성된 레코드 및 주 에이전트 런타임 레코드를 사용합니다.
// "인간". 플래너를 깨우는 것은 전체 배치가 작성된 후 setGoals에 의해 수행됩니다(아래 참조). 도서관에 들르는 것만 책임집니다.
func (t *ToolSet) addOneGoal(it goalItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("텍스트는 비워둘 수 없습니다")
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(it.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	origin := t.worker
	if origin == "" {
		origin = "system"
	}
	id, err := t.ts.AddNode(db.KindGoal, payload, 0, "open", origin, nil)
	if err != nil {
		return 0, err
	}
	if of, _ := t.ts.OriginFactID(); of > 0 && id > 0 {
		_ = t.ts.Link(of, db.RelSpawns, id) // goals descend from the task root (origin fact)
	}
	return id, nil
}

// setGoals는 [이 작업]에 새로운 탐색 목표(목표 노드)를 추가합니다. 이는 대상 디스어셈블러의 제출 도구이자 주요 도구입니다.
// 에이전트가 실행 중일 때 대상을 패치하기 위한 도구 - 동일한 관리 도구는 웹 측에서 설명/스키마를 변경하고 에이전트별로 바인딩할 수 있습니다.
func (t *ToolSet) setGoals() actool.CoreTool {
	return writeTool("set_goals",
		"[이 작업]에 새로운 탐색 목표(목표)를 추가하세요. 목표 = 공격 단계나 정찰 조치가 아닌 최종 제공 가능/검증 가능한 결과입니다.\n"+
			"★우선순위 배치: 여러 목표를 목표 배열에 넣고 한 번 제출하면 동일한 길이와 순서로 ID를 반환합니다(실패 항목 ID=0, 자세한 내용은 오류 참조). 단일 기사의 경우 목표가 생략되고 최상위 텍스트에 직접 제공됩니다.\n"+
			"vulnclass 선택 사항: 취약점 클래스(예: SQLi/IDOR)에 해당하며 비즈니스 논리 클래스 대상은 비어 있습니다. 목표 달성 여부는 시스템에 의해 결정되며 달성된 것으로 표시됩니다. 이 도구는 새로운 추가에만 책임이 있습니다.",
		obj(map[string]any{
			"goals":     map[string]any{"type": "array", "description": "[먼저 사용하세요] 추가할 타겟 어레이를 순서대로 처리합니다. 각 요소: 텍스트(필수, 독립적으로 검증 가능한 최종 목표) + vulnclass(선택 사항). 반환된 ID는 이 배열과 길이 및 순서가 동일합니다.", "items": map[string]any{"type": "object"}},
			"text":      str("[단일 항목] 독립적으로 검증 가능한 최종 목표"),
			"vulnclass": str("[단일 항목] SQLi/IDOR과 같은 해당 취약점 클래스(해제된 경우); 비즈니스 로직 타겟은 비워둘 수 있습니다"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_goals 활성화되지 않음: ExplorationStore가 초기화되지 않음"), nil
			}
			var a struct {
				Goals    []goalItem `json:"goals"`
				goalItem            // 단일 모드: 최상위 텍스트/vulnclass
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Goals) > 0
			items := a.Goals
			if !batch {
				items = []goalItem{a.goalItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneGoal(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// 플래너를 깨우세요(한 번에 한 배치씩). informGoal: set_goals의 우선순위를 한 번 지정하고 "사람이 추가되었습니다.
				// N 대상:..."이 트리거되고 화면이 하나씩 새로 고쳐지지 않습니다. 디스어셈블러/워커에는 이 콜백이 없습니다 → 순수 inform(disassembler)으로 돌아갑니다.
				// round-0은 알림도 받지 않습니다. 즉, 플래너가 아직 시작되지 않았기 때문에 작업이 없습니다.
				switch {
				case t.notifyGoal != nil:
					t.notifyGoal(addedTexts)
				case t.notify != nil:
					t.notify()
				}
				// 주 에이전트가 실행 중일 때 새 대상을 추가 → 완료/일시 중지된 작업을 다시 실행 상태로 끌어와 계속 실행합니다(최종 상태 게이트는
				// 일반 알림을 삼키려면 명시적으로 부활시켜야 합니다.) 메인 에이전트만이 이 콜백을 수신합니다. 디스어셈블러/작업자는 0입니다.
				if t.resumeTask != nil {
					t.resumeTask()
				}
			}

			if !batch { // 단일: 원래 반환을 유지합니다.
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("goal added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type constraintItem struct {
	Text string `json:"text"`
	Type string `json:"type"` // allow | deny
}

// addOneConstraint는 task_constraints에 작업 제약 조건을 추가합니다. Origin은 t.worker를 사용합니다(기본 시스템):
// 디스어셈블러는 "goals", 주체는 "human"이라고 적으세요.
func (t *ToolSet) addOneConstraint(it constraintItem) (int64, error) {
	text := strings.TrimSpace(it.Text)
	if text == "" {
		return 0, fmt.Errorf("텍스트는 비워둘 수 없습니다")
	}
	kind := strings.TrimSpace(strings.ToLower(it.Type))
	if kind == "" {
		kind = "deny" // 기본적으로 처리는 금지됩니다. 유형이 표시되지 않으면 더욱 보수적입니다.
	}
	if kind != "allow" && kind != "deny" {
		return 0, fmt.Errorf("유형은 허용 또는 거부여야 합니다")
	}
	return t.ts.AddConstraint(kind, text, t.worker)
}

// setConstraints는 [이 작업]에 운영 제약 조건을 추가합니다(허용=허용되는 것 / 거부=금지되는 것). 둘다 타겟
// Disassembler round-0은 제약 조건을 추출하기 위한 제출 도구이자, 주 에이전트 실행 시 제약 조건을 보완하기 위한 도구이기도 합니다. 웹에서도 동일한 관리 도구를 사용할 수 있습니다.
// 설명/스키마를 변경하고 에이전트별로 바인딩합니다. 탐색 경계를 제한하라는 제약 조건이 계획자/작업자의 시스템 프롬프트에 주입됩니다.
func (t *ToolSet) setConstraints() actool.CoreTool {
	return writeTool("set_constraints",
		"탐색 경계를 정의하려면 [이 작업]에 작업 제약 조건을 추가하세요. 유형=허용(작업 허용) 또는 거부(작업 금지).\n"+
			"제약 조건 = \"수행할 수 있는 작업/수행할 수 없는 작업\"(예: \"현재 포트만 테스트하고 다른 포트는 검색하지 않음\", \"프로덕션 라이브러리에 작업 쓰기 금지\", \"수동 정찰만 허용\")에 대한 규정은 목표나 공격 단계가 아닙니다.\n"+
			"★우선순위 배치: 여러 항목을 제약 조건 배열에 넣고 한 번에 제출하고 동일한 길이와 순서의 ID를 반환합니다(실패 항목 ID=0, 자세한 내용은 오류 참조). 단일 항목의 경우 제약 조건이 생략되고 최상위 텍스트/유형에 직접 제공됩니다.\n"+
			"작업 목표/설명에는 [명확하게 작성된] 제약사항만 등록하고, 구성하지 마세요. 유형이 확실하지 않은 경우 거부(더 보수적)를 사용하세요.",
		obj(map[string]any{
			"constraints": map[string]any{"type": "array", "description": "[먼저 사용하세요] 추가할 Constraint 배열을 순서대로 처리합니다. 각 요소: 텍스트(필수, 제약 조건 1개) + 유형(허용|거부). 반환된 ID는 이 배열과 길이 및 순서가 동일합니다.", "items": map[string]any{"type": "object"}},
			"text":        str("[단일항목] 연산제약 내용"),
			"type":        str("[단일 입력] 허용(허용) 또는 거부(금지); 기본 처리는 거부입니다"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("set_constraints가 활성화되지 않음: ExplorationStore가 초기화되지 않음"), nil
			}
			var a struct {
				Constraints    []constraintItem `json:"constraints"`
				constraintItem                  // 단일 모드: 최상위 텍스트/유형
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Constraints) > 0
			items := a.Constraints
			if !batch {
				items = []constraintItem{a.constraintItem}
			}
			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneConstraint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}
			if !batch { // 싱글: 단순하게 리턴하세요
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("constraint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) addHint() actool.CoreTool {
	return t.writeExpTool("add_hint", "인간/마스터 에이전트의 전략적 프롬프트를 탐색 그래프에 걸어 놓으면 플래너가 다음에 인텐트를 생성할 때 이를 읽습니다.\n"+
		"★우선순위 배치: 여러 개의 힌트를 힌트 배열에 넣고 한 번에 제출합니다(하나씩 호출하는 것보다 왕복 시간이 절약됩니다). 힌트와 동일한 길이 및 순서로 ID 배열을 반환합니다(실패 항목 ID=0, 자세한 내용은 오류 참조). 단일 항목의 경우 힌트가 생략되고 최상위 텍스트에 직접 제공됩니다.",
		obj(map[string]any{
			"hints":        map[string]any{"type": "array", "description": "[먼저 사용하세요] 추가할 프롬프트 배열을 순서대로 처리합니다. 각 요소 필드는 아래의 최상위 필드(text/asset_ids/traffic_refs)와 동일합니다. 반환된 ID는 이 배열과 길이 및 순서가 동일합니다.", "items": obj(map[string]any{"text": str("안내 내용"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": HintTrafficSchema()})},
			"text":         str("[단일항목] '사후인증 인터페이스 발굴에 집중' 등 프롬프트 내용"),
			"traffic_refs": HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "고정된 asset_ids(선택사항, 0/1/다수)"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Hints    []hintItem `json:"hints"`
				hintItem            // 단일 모드: 최상위 text/asset_ids
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Hints) > 0
			items := a.Hints
			if !batch {
				items = []hintItem{a.hintItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			var addedTexts []string
			for i, it := range items {
				id, err := t.addOneHint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
				addedTexts = append(addedTexts, strings.TrimSpace(it.Text))
			}
			if len(addedTexts) > 0 {
				// 플래너를 깨우세요(한 번에 한 배치씩). informHint 우선순위 지정: add_hint가 "사람이 추가되었습니다."라는 메시지를 기록하면
				// N 전략적 힌트:..."가 트리거되어 플래너가 "이 라운드는 새로운 힌트에 의해 트리거됩니다"를 명확히 하고 프롬프트 내용을 볼 수 있습니다.
				// 콜백이 수신되지 않으면 순수 알림으로 돌아갑니다(깨어나지 않은 힌트는 그림에서 자체적으로 읽을 수 있도록 여전히 접혀 있습니다).
				switch {
				case t.notifyHint != nil:
					t.notifyHint(addedTexts)
				case t.notify != nil:
					t.notify()
				}
			}

			if !batch { // 단일: 원래 반환을 유지합니다.
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("hint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return t.writeExpTool("kill_work", "실행 중인 인텐트(작업)를 종료합니다. 방황/의미 없는 탐색을 중지하는 데 사용됩니다. 종료된 의도는 중지된 것으로 표시되며 더 이상 자동으로 회복되지 않습니다. 결정하기 전에 먼저 get_worker_output을 사용하여 무엇을 하고 있는지 확인하세요.",
		obj(map[string]any{"intent_id": idp("종료하려는 인텐트 ID(= 작업 핸들)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("kill_work는 현재 사용할 수 없습니다"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 필요"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id는 이 작업의 인텐트여야 합니다(관련된 작업 인텐트는 읽기 전용입니다)"), nil
			}
			if err := t.killWork(id); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("인텐트 %d를 사용하기 위해 종료 신호를 보냈습니다", id)), nil
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// 넛지("X 작업을 중지하고 Y에 집중하세요"); 전체 방향이 잘못된 경우 kill_work + 새로운 의도를 사용하세요.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return t.writeExpTool("steer_work", "실행 의도(작업)를 중단하거나 진행 상황을 잃지 않고 실시간으로 실행 의도(작업)에 수정 지침을 주입합니다. 작업자는 다음 단계를 수행하기 전에 지침을 받고 그에 따라 조정합니다. 'X로 가지 말고 Y에 집중하세요'와 같은 [의도적인] 수정에 사용됩니다. 방향이 완전히 잘못된 경우 kill_work를 사용한 다음 새로운 의도를 설정해야 합니다. get_worker_output을 먼저 사용하여 수행 중인 작업을 확인하는 것이 좋습니다.",
		obj(map[string]any{
			"intent_id": idp("수정할 인텐트 ID(= 작업 핸들)"),
			"message":   str("직원에게 무엇을 멈추고 무엇을 전환해야 하는지 명확히 알 수 있도록 시정 지침을 제공합니다."),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("steer_work는 현재 사용할 수 없습니다"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message  string          `json:"message"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 필요"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id는 이 작업의 인텐트여야 합니다(관련된 작업 인텐트는 읽기 전용입니다)"), nil
			}
			if strings.TrimSpace(a.Message) == "" {
				return actool.Errorf("메시지가 필요합니다"), nil
			}
			if err := t.steerWork(id, a.Message); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("수정 지침이 의도 %d 작업에 주입되었습니다(다음 단계에서 적용됨)", id)), nil
		})
}

// getWorkerOutput은 작업의 최종(또는 중단 시점) 결론 텍스트를 의도 ID로 반환합니다.
func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return t.readExpTool("get_worker_output", "이 작업의 최종 출력 결론이나 작업과 직접적으로 관련된 특정 의도(작업)를 가져옵니다. 연결된 작업 결과에는 source_task_id/inherited=true가 있으며 읽기 전용입니다. 일반 끝은 요약을 반환합니다. 중지된/비정상적인 작업은 중단 시점까지의 마지막 출력을 반환합니다.",
		obj(map[string]any{"intent_id": idp("인텐트 ID(= 작업 핸들)")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 필요"), nil
			}
			intentNode, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id는 이 작업이나 직접 관련된 작업에 속하지 않습니다"), nil
			}
			acts, _, err := t.ts.ActivityListWithSources(id, 0, 1000)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var chosen, fallback *db.Activity
			for i := range acts {
				switch acts[i].Kind {
				case "result":
					chosen = &acts[i]
					fallback = &acts[i]
				case "text":
					fallback = &acts[i]
				}
			}
			pick := chosen
			if pick == nil {
				pick = fallback
			}
			if pick == nil {
				if intentNode.Inherited {
					return jsonResult(inheritedMap(map[string]any{
						"intent_id": id, "final_text": "(아직 결과물이 없는 작품입니다)",
					}, intentNode.SourceTaskID))
				}
				return actool.Text("(아직 결과물이 없는 작품입니다)"), nil
			}
			detail, _ := t.ts.ActivityDetailWithSources(pick.ID)
			if detail == "" {
				detail = pick.Summary
			}
			result := map[string]any{
				"intent_id": id, "final_text": detail,
				"summary": pick.Summary, "is_error": pick.IsError,
			}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps := make([]map[string]any, 0, len(acts))
	for i := range acts {
		step := map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		}
		if acts[i].Inherited {
			inheritedMap(step, acts[i].SourceTaskID)
		}
		steps = append(steps, step)
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return t.readExpTool("get_worker_trace",
		"특정 의도(작업)의 실행 과정을 조회합니다. 최종 결과만 보여 주는 get_worker_output과 달리 단계별 기록을 확인할 수 있습니다. 사용법은 세 가지입니다:\n"+
			"① intent_id만 전달하면 각 단계의 요약과 step_id를 반환합니다. 전체 출력은 포함하지 않습니다.\n"+
			"② intent_id와 q를 전달하면 키워드에 맞는 단계의 요약을 반환합니다. 요약과 전체 출력에서 검색하지만 결과에는 요약만 표시합니다.\n"+
			"③ intent_id와 step_ids를 전달하면 해당 단계의 전체 내용을 반환합니다. 한 번에 최대 5개까지 가져오며, 초과분은 notice와 omitted_step_ids에 표시됩니다.\n"+
			"먼저 ① 또는 ②로 step_id를 찾고, ③으로 필요한 단계의 전체 출력을 확인하세요. 사고(thinking) 단계는 포함하지 않습니다. 직접 연관된 작업의 기록은 source_task_id/inherited=true로 표시되며 읽기 전용입니다.",
		obj(map[string]any{
			"intent_id": idp("인텐트 ID(= 작업 핸들)"),
			"q":         str("키워드: 해당 단계의 요약/전체 출력만 반환(선택 사항, step_ids와 상호 배타적)"),
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "완전한 콘텐츠의 step_id를 가져오려면(1/2에서 반환, 한 번에 최대 5개까지 가져오며 멀티 패스의 경우 처음 5개만 반환하고 나머지는 omitted_step_ids에 나열됨)"},
			"limit":     intp("다이제스트 스트림/검색 반환 제한(선택 사항)"),
		}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage   `json:"intent_id"`
				Q        string            `json:"q"`
				StepIDs  []json.RawMessage `json:"step_ids"`
				Limit    int               `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 필요"), nil
			}
			intentNode, nodeErr := t.ts.GetNodeWithSources(id)
			if nodeErr != nil {
				return actool.Errorf(nodeErr.Error()), nil
			}
			if intentNode == nil || intentNode.Kind != db.KindIntent {
				return actool.Errorf("intent_id는 이 작업이나 직접 관련된 작업에 속하지 않습니다"), nil
			}
			// ③ detail drill-down by step ids, thinking excluded by the store.
			if len(a.StepIDs) > 0 {
				// Dedup + drop invalid ids first so garbage/duplicates don't eat into
				// the per-call cap. detail is returned in full (untruncated), so the
				// cap bounds one tool result; over the cap we serve the first N and
				// tell the model exactly which ids were deferred, instead of erroring
				// and forcing it to re-plan the call.
				const maxStepIDs = 5
				var ids []int64
				seen := make(map[int64]bool)
				for _, raw := range a.StepIDs {
					if v := pid(raw); v > 0 && !seen[v] {
						seen[v] = true
						ids = append(ids, v)
					}
				}
				var omitted []int64
				if len(ids) > maxStepIDs {
					omitted = append(omitted, ids[maxStepIDs:]...)
					ids = ids[:maxStepIDs]
				}
				acts, err := t.ts.ActivityByIDsWithSources(ids)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				steps := make([]map[string]any, 0, len(acts))
				for i := range acts {
					if acts[i].NodeID == nil || *acts[i].NodeID != id || acts[i].Inherited != intentNode.Inherited ||
						(acts[i].Inherited && acts[i].SourceTaskID != intentNode.SourceTaskID) {
						continue
					}
					step := map[string]any{
						"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
						"is_error": acts[i].IsError, "detail": acts[i].Detail,
					}
					if acts[i].Inherited {
						inheritedMap(step, acts[i].SourceTaskID)
					}
					steps = append(steps, step)
				}
				result := map[string]any{"intent_id": id, "steps": steps, "returned_step_ids": ids}
				if len(omitted) > 0 {
					// returned_step_ids/omitted_step_ids let the model decide programmatically
					// whether another call is worth it; the notice states the same in prose.
					result["omitted_step_ids"] = omitted
					result["notice"] = fmt.Sprintf(
						"매번 최대 %d걸음의 전체 콘텐츠를 가져올 수 있습니다. 이번에는 처음 %d 단계(%v)가 반환되었으며 나머지 %d 단계는 %v입니다."+
							"이러한 내용이 충분히 배치되면 나머지 단계를 수행할 필요가 없습니다. 계속해야 한다면 이 step_ids를 사용하여 다시 조정하세요.",
						maxStepIDs, len(ids), ids, len(omitted), omitted)
				}
				if intentNode.Inherited {
					inheritedMap(result, intentNode.SourceTaskID)
				}
				return jsonResult(result)
			}
			// ①/② summary stream, optionally keyword-filtered; 100-char summaries.
			var acts []db.Activity
			var err error
			if strings.TrimSpace(a.Q) != "" {
				acts, err = t.ts.ActivityTraceSearchWithSources(id, a.Q, a.Limit)
			} else {
				acts, err = t.ts.ActivityTraceWithSources(id, a.Limit)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			result := map[string]any{"intent_id": id, "steps": traceSteps(acts)}
			if intentNode.Inherited {
				inheritedMap(result, intentNode.SourceTaskID)
			}
			return jsonResult(result)
		})
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return t.readExpTool("search_all_worker_traces",
		"[대부분의 정보가 시스템에 제공되어 있으므로 일반적으로 권장되지 않음] [이 작업의 다른 작업 실행 프로세스]에서 키워드(q)로 검색 - 작업자가 보았지만 사실로 기록되지 않은 것(특정 경로/토큰/오류 보고서 등)을 검색하는 데 사용됩니다."+
			"이 의도에 대한 귀하의 단계(이미 귀하의 컨텍스트에 있는 단계)는 자동으로 제외되었습니다."+
			"각각 intent_id가 있는 히트 단계의 요약(요약 ≤100단어)만 반환합니다. 이를 기반으로 get_worker_trace(intent_id, step_ids=[...])를 사용하여 전체 콘텐츠를 가져옵니다.",
		obj(map[string]any{
			"q":     str("키워드(요약 검색 + 모든 작업 단계의 전체 출력)"),
			"limit": intp("상한값 반환, 기본값 100(선택사항)"),
		}, "q"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Q) == "" {
				return actool.Errorf("q 필수"), nil
			}
			// 호출자 자신의 의도를 제외하는 단계(작업자 자신의 추적은 이미 해당 컨텍스트에 있음)
			acts, err := t.ts.ActivityTraceSearchAllWithSources(t.ownerNode, a.Q, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			hits := make([]map[string]any, 0, len(acts))
			for i := range acts {
				var intent int64
				if acts[i].NodeID != nil {
					intent = *acts[i].NodeID
				}
				hit := map[string]any{
					"intent_id": intent, "step_id": acts[i].ID, "worker": acts[i].Worker,
					"kind": acts[i].Kind, "tool": acts[i].Tool, "is_error": acts[i].IsError,
					"summary": firstLine(acts[i].Summary, 100),
				}
				if acts[i].Inherited {
					inheritedMap(hit, acts[i].SourceTaskID)
				}
				hits = append(hits, hit)
			}
			return jsonResult(map[string]any{"query": a.Q, "hits": hits})
		})
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return t.readExpTool("list_worker_traces",
		"[대부분의 정보가 시스템에 제공되므로 일반적으로 권장되지 않습니다.] 이 작업에 [실행된 작업(의도)] 인덱스를 나열합니다:intent_id + 문장 방향(요약) + 상태."+
			"귀하(작업자)는 탐색 그래프를 볼 수 없습니다. 이를 사용하여 살펴볼 가치가 있는 작업을 찾은 다음 get_worker_trace(intent_id)를 사용하여 해당 단계를 확인하고 get_worker_trace(intent_id, step_ids=[...])를 사용하여 세부정보를 가져옵니다."+
			"아직 실행되지 않은 열린 항목을 제외하고 실행된 항목(실행 중/완료/소진/차단/중지)만 나열합니다. 참고: 작업의 경계는 여전히 귀하가 받은 의도입니다. 다른 작품을 보는 것은 재사용과 관찰/중복 방지를 위한 것입니다.",
		obj(map[string]any{
			"q":     str("요약 키워드로 필터링(선택사항)"),
			"limit": intp("반환 상한, 기본값 50(선택 사항)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}
			all, err := t.ts.ListByKindWithSources(db.KindIntent, 500)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Q))
			out := make([]map[string]any, 0, limit)
			for _, n := range all {
				if n.Inherited && n.State == "running" {
					continue
				}
				switch n.State {
				case "running", "done", "exhausted", "blocked", "stopped": // has run → has a process
				default:
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				summary, _ := p["summary"].(string)
				if q != "" && !strings.Contains(strings.ToLower(summary), q) {
					continue
				}
				item := map[string]any{"intent_id": n.ID, "summary": summary, "state": n.State}
				if n.Inherited {
					inheritedMap(item, n.SourceTaskID)
				}
				out = append(out, item)
				if len(out) >= limit {
					break
				}
			}
			return jsonResult(map[string]any{"works": out})
		})
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		// cold-digest §6.1: restore folded cold nodes (digest body → members → detail).
		t.expandDigest(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.listGoals(), t.addIntent(), t.proveGoal(), t.goalMet(),
		t.killWorkTool(), t.steerWorkTool(),
		// Report_finding : 기획 상황 분석 및 판단 과정에서 취약점을 확인했다면 직접 등록할 수 있습니다(작업자와 동일한 툴).
		t.addFinding(),
		//list_companies: 회사 목록 + 범위 + 자산 수를 봅니다(회사 ID 가져오기 / 소유권 범위 이해).
		t.listCompanies(),
		// list_assets: 계획 중 DSL에 따라 전체 자산 라이브러리를 검색합니다(list_untested_assets의 "범위 내에서 테스트되지 않음" 관점 사용).
		// "도메인 이름/지문/포트/상태 코드 및 기타 조건으로 전체 데이터베이스를 검색"하는 기능이 추가되었습니다.
		t.listAssets(),
		// add_company_scope: 도메인 이름/IP/CIDR/ICP/키워드는 계획 중에 회사의 자산 범위에 포함될 수 있습니다(히트 자산 자동 청구).
		t.addCompanyScope(),
		// add_task_scope: 이 작업의 테스트 범위(커버리지 분모)에 전체 루트 도메인/전체 회사/특정 하위 도메인/IP를 포함하도록 주도적으로 수행합니다.
		t.addTaskScope(),
		// list_untested_assets: 요청 시 이 작업 범위 내에서 테스트되지 않은 자산(유형 + 페이징)을 확인하고 재량에 따라 추가 테스트를 수행합니다.
		t.listUntestedAssets(),
	}
}
