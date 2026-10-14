package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// 이 문서는 "내장 도구"를 순수 코드에서 DB가 재정의할 수 있는 열거 가능한 디렉터리로 변경합니다.
//   - BuilderinToolSeeds(): 실행 에이전트의 세 가지 내장 도구 세트를 시드 레코드(키 +
//     설명 + 매개변수 스키마 + 기본 바인딩된 에이전트), 서버가 시작될 때 도구 테이블에 멱등성을 부여하기 위한 것입니다.
//- ToolResolve 후크 : 실행 시 DB의 도구 행을 누르면 설치된 도구를 에이전트별로 필터링 +
//     설명/스키마 + 매개변수 기본값 삽입'을 재정의합니다. 키/핸들러는 여전히 코드 수준에 있으며 DB는 '산문 및 기본값'만 변경합니다.
// 핸들러(호출 동작)는 항상 코드에서 나옵니다. DB는 이를 변경할 수 없으며 모델에 표시된 설명 및 기본 입력 매개변수만 변경할 수 있습니다.

// ToolSeed는 내장 도구의 시드 가능한 스냅샷입니다. 키는 CoreTool.Name()입니다(핸들러에 연결됨,
// UI는 읽기 전용) 설명/스키마는 코드의 도구 정의에서 가져오고 에이전트는 코드가 기본적으로 제공하는 에이전트입니다.
type ToolSeed struct {
	Key    string         // = CoreTool.Name(), 기본 키는 변경할 수 없습니다.
	Desc   string         // 최상위 설명(UI에서 재정의 가능)
	Schema map[string]any // 매개변수 JSON-스키마(구조는 읽기 전용이며 설명/기본값은 UI에서 변경 가능)
	Agents []string       // 기본 바인딩된 에이전트 키(작업자/계획자/메인에이전트)
}

// 내장 도구ByAgent는 "읽기 전용 빈 셸" 도구 세트(저장소 없음)를 사용하여 각 실행 에이전트를 구성합니다.
// 도메인 도구 세트. 도구 생성자는 클로저를 Spec에 채우기만 하고 생성 중에 저장소를 역참조하지 않으므로 nil은 안전합니다.
// 이러한 도구는 여기서 Name()/Description()/InputSchema()를 읽는 데만 사용되며 호출은 사용되지 않습니다.
//
// 의도적으로 [제외] SDK 일반 도구 actool.DefaultTools()(읽기/쓰기/편집/MultiEdit/LS/Glob/
// Grep/Bash): 각각 고정된 에이전트가 있으며 "누구에게 바인딩할지"를 선택할 수 없으며 대부분의 지침은 Prompt()에 있습니다.
// (이 표에서는 설명()만 다루므로 절반만 다루면 오해의 소지가 있습니다.) 시드 없음 → DB 행 없음 → ToolResolve
// 덮어쓰지 않고 있는 그대로 해제하고 이전처럼 작동합니다. Artex의 자체 도메인 도구만 테이블에서 관리할 수 있습니다.
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// 목표(목표 디스어셈블러)는 기본적으로 set_goals + set_constraints에 바인딩됩니다. 목표를 분리하기 위해 목표를 사용합니다.
		// 추출된 작업 제약 조건이 라이브러리에 기록됩니다. mainagent와 동일한 관리 도구를 공유합니다. 설명/스키마는 웹 측에서 변경이 가능하며, 에이전트가 확인할 수 있습니다.
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto는 기본적으로 취약점 보고 + 자산 관리 도구와 함께 번들로 제공됩니다. 필요에 따라 다른 도메인 도구를 UI에서 확인할 수 있습니다.
		// 이 시드에 의해 새로운 라이브러리가 작성됩니다. 이전 라이브러리는 seedAutoDefaultBindings에 의해 마이그레이션됩니다.
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// 펜테스트(독립 침투 에이전트)는 기본적으로 자산 확인/자산 삽입/취약점 보고/취약점 확인/기업 확인으로 바인딩됩니다.
		// 이 시드에 의해 새로운 라이브러리가 작성됩니다. 이전 라이브러리는 seedPentestDefaultBindings에 의해 마이그레이션됩니다.
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound: 이러한 시스템 도구는 평소와 같이 디렉터리에 입력되지만(웹 측에서 볼 수 있으며 에이전트가 수동으로 확인할 수 있음)
// 기본값 [에이전트 바인딩 안 함] - ToolResolve는 빈 개체에 바인딩되어 있고 명시적으로 선택해야 하는 모든 도구를 삭제합니다.
// 특정 에이전트의 기본 도구 세트(예: PlannerTools의 goal_met)에 남아 있는 이유: 첫째, 시드를 허용합니다.
// 설명/스키마를 가져오도록 구성합니다. 둘째, 사용자는 이를 수동으로 다시 바인딩한 다음 기본에 있고 ToolResolve를 유지할 수 있는 경우에만 실행합니다.
//
// goal_met: Prove_goal을 하나씩 우회하여 글로벌 관점에서 [전체 작업 완료]를 직접 알리는 방식으로 무게가 무겁고 오판의 위험이 있으며, 와 일치하지 않습니다.
// "prove_goal은 마지막 골을 표시 → 자동 종료"가 반복되므로 기본적으로 에이전트가 제공되지 않으며 필요할 때 수동으로 연결할 수 있습니다.
var defaultUnbound = map[string]bool{"goal_met": true}

// 내장 툴시드(BuiltinToolSeeds)는 각 에이전트의 내장 도구 세트를 시드 목록으로 결합합니다. 즉, 이름이 같은 도구(예: list_assets)
// 여러 에이전트)가 하나로 결합되고 에이전트가 연합을 차지합니다. defaultUnbound의 도구는 바인딩을 비워 둡니다.
func BuiltinToolSeeds() []ToolSeed {
	byAgent := builtinToolsByAgent()
	order := []string{"mainagent", "goals", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool   actool.CoreTool
		agents []string
	}
	m := map[string]*acc{}
	var keys []string
	for _, ak := range order {
		for _, t := range byAgent[ak] {
			a, ok := m[t.Name()]
			if !ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out := make([]ToolSeed, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		agents := a.agents
		if defaultUnbound[k] {
			agents = []string{} // 디렉터리를 입력하고 수동으로 바인딩할 수 있지만 기본적으로 에이전트가 제공되지 않습니다(다른 도구와 일치하도록 null 대신 [] 저장).
		}
		out = append(out, ToolSeed{
			Key:    k,
			Desc:   a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// 기본 입력 매개변수는 주입됩니다. 일치하는 DB 행이 없는 도구(예: MCP/기술/호스트 도구)
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (o *overriddenTool) Description() string         { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// 널. 구조(이름/유형/필수)는 그대로 유지되며 기본값만 병합됩니다.
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs := scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m := map[string]json.RawMessage{}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &m); err != nil {
			return in // non-object input: don't touch it
		}
	}
	changed := false
	for k, dv := range defs {
		if cur, ok := m[k]; !ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if !changed {
		return in
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dv, ok := p["default"]
		if !ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err := json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := string(raw)
	return s == "null" || s == `""`
}
