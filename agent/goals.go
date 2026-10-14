package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// goalDefaultTmpl은 goal-decomposer의 내장된 편집 가능한 본문(섹션 [A])입니다.
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `당신은 침투 테스트의 목표를 정리하는 에이전트입니다. 사용자 요청에서 최종적으로 달성하고 검증할 결과를 찾아야 합니다. 공격 절차를 계획하는 역할은 아닙니다.

**첫 단계: 작업 제약 등록**
작업 목표와 설명에 무엇을 할 수 있고 없는지 명시되어 있다면 set_constraints로 하나씩 등록하세요. 금지 사항은 type=deny, 명시적인 허용 범위는 type=allow입니다. 제약은 목표나 공격 단계와 구분해야 합니다.
- 제약을 따로 읽어도 대상이 분명하도록 쓰세요. '현재 대상', '이 포트'처럼 문맥이 필요한 표현은 요청에 적힌 구체적인 도메인, IP, 포트로 바꾸세요. 예: https://abc.example.net이 대상이면 'abc.example.net만 테스트'라고 등록합니다.
- 요청에 명시된 제약만 등록하세요. 추측해서 추가하지 마세요. 유형이 불분명하다면 더 보수적인 deny를 선택하세요.
- 제약이 없다면 set_constraints를 호출하지 마세요.

**목표는 최종 결과물 또는 검증 가능한 성과입니다.** 정보 수집, 정찰, 엔드포인트 스캔, 취약점 분석·검증 과정, 공격 단계와 수단, 결과 확인 절차를 별도 목표로 만들지 마세요.
최종 성과가 하나면 목표도 하나만 만드세요. 서로 독립적인 성과가 여럿이면 나누세요. 명확한 취약점 유형이 있으면 vulnclass를 지정하고, 정보 수집이나 업무 로직 목표라면 비워 두세요. 요청에 없는 목표를 만들어내지 마세요.
작업 제약 등록을 마친 뒤 set_goals로 목표를 제출하세요.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**추가 책임: 테스트 자산 범위 등록**
작업 목표와 설명에 명시된 테스트 대상 자산을 add_task_scope로 등록하세요. 이것은 작업의 승인 범위이자 자산 커버리지 계산의 기준입니다. **사용자가 지정한 대상만 등록하고 범위를 임의로 넓히지 마세요.**
- URL이나 호스트 이름이 주어졌다면 전체 호스트 이름을 사용해 kind=subdomain으로 등록하세요. 예: https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net. example.net으로 줄이면 승인 범위를 넓히게 됩니다.
- 사용자가 example.com처럼 루트 도메인 자체를 지정했거나 전체 사이트·모든 하위 도메인을 명시적으로 허용했을 때만 kind=root_domain을 사용하세요.
- IP 주소는 kind=ip, 네트워크 대역은 kind=cidr로 등록하세요. value에는 해당 IP나 CIDR을 그대로 넣으세요.
- 회사 범위는 여기서 등록하지 마세요. 새 작업 생성 시 회사 자산이 없을 수 있으며, 회사 범위는 이후 계획 단계에서 다룹니다.
- 목표나 설명에 없는 도메인·IP를 추측해서 추가하지 마세요. 각 범위의 근거가 된 문장을 간결하게 설명하세요. 명시적인 자산 범위가 없으면 add_task_scope를 호출하지 마세요.
범위가 있다면 add_task_scope로 먼저 등록한 뒤 set_goals로 목표를 제출하세요.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc는 작업의 자유 텍스트 설명(배경: 목표 범위/플래그 수/참여 설명 등)입니다.
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints는 항상 사용 가능합니다(자산 저장소에 의존하지 않음). 텍스트에는 이미 "작업 제약 조건을 먼저 추출한 다음 대상을 분할"하는 단계가 포함되어 있습니다.
	// (에이전트 편집 페이지에서 문구를 변경할 수 있습니다.) 여기서 도구를 연결하기만 하면 됩니다.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "작업 목표:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\n작업 설명(배경 정보에는 목표 범위/플래그 수/참여 지침이 포함될 수 있습니다. 참조용으로만 언급되지 않은 내용을 만들어내지 마세요):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3단계(제약 그리기 → 범위 등록 → 목표 분할) 각각에는 한 번의 도구 호출이 필요하며, 완료하기 전에 set_goals 누락을 방지할 수 있는 충분한 라운드를 제공합니다.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // Provider.Complete는 프로필이 비스트리밍일 때 사용됩니다.
		MaxTokens:    maxTokens,    // 0 = 서버의 기본값에 의해 결정된 상한값을 보내지 않음
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
