package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// 종료 프롬프트(wrap-up/결제 프롬프트): 에이전트의 단계가 부족할 때(MaxTurns) 또는
// [Timeout (run_seconds/MaxDuration)]이 종료되면 SDK의 정산 단계에서 이 프롬프트를 삽입합니다.
// 에이전트가 식별되었지만 다시 기록되지 않은 콘텐츠를 먼저 저장한 다음, 완료되지 않은 작업을 피하기 위해 요약을 출력하도록 합니다.
//
//각 에이전트의 종료 프롬프트는 요청 시 백그라운드에서 덮어쓸 수 있습니다(agents.wrapup_prompt 저장). 비워두면 여기를 사용하세요.
// 내장된 기본값. [프롬프트 텍스트]만 편집할 수 있습니다. 비활성화할 도구와 종료할 예산 라운드 수는 코드 고정 전략입니다.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// 에이전트 키로 색인화되는 기본 종료 프롬프트가 내장되어 있습니다. 작업자는 역사적으로 하드 코딩된 SetWrapUpPrompt를 재사용합니다.
// (worker.go에 정의됨) 플래너/메인에이전트 각각에는 자체 버전이 있습니다. 누락된 것(커스텀 에이전트)은 보편적인 것입니다.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// WrapupTurnDefaults: 각 에이전트의 종료 단계 [self]에 내장된 기본 라운드 예산입니다(배경 >0으로 재정의 가능).
// 각 게임에는 최종 단계에서 충분한 움직임이 있도록 10라운드가 제공됩니다. genericWrapupTurns를 놓쳤습니다.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "이번 계획 라운드의 단계 수가 곧 소진됩니다. 끝나는 것은 이번 라운드뿐이며, 시스템은 상황이 바뀌면 다시 계획을 요청합니다. 전체 작업을 여기서 마무리할 필요는 없습니다. 이번 라운드에서 이미 판단한 내용을 반영하되 마무리하려고 억지로 실행 계획을 만들지는 마세요. 이번 라운드에 새 실행 계획이 0개여도 정상일 수 있습니다. (1) 지금 바로 맡겨야 할 탐색 방향을 정했다면 add_intent 한 번으로 일괄 등록하세요. (2) 발견 사항이나 사실로 달성이 입증된 목표는 prove_goal로 met 상태를 표시하세요. (3) 여러 단계가 필요한 순차적 활용 경로를 찾았다면 다음 라운드에 이어갈 수 있도록 TodoWrite에 기록하세요. 마친 뒤 이번 라운드를 종료하고 요약문은 출력하지 마세요."

const mainAgentWrapUpDefault = "이번 상호작용의 단계 수가 곧 소진됩니다. 새 탐색이나 작업을 시작하지 마세요. 현재 진행 상황, 핵심 결론, 권장하는 다음 단계를 사용자에게 일반 텍스트 한 문장으로 요약하세요."

const genericWrapUpDefault = "실행 예산이 곧 소진됩니다. 완료했지만 아직 저장하지 않은 결과를 먼저 기록하세요. 그런 다음 수행한 작업과 핵심 결론을 일반 텍스트 한 문장으로 요약하세요. 이 문장이 이번 실행의 결과로 표시됩니다."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- 작업 수준 시간 초과 종료 단어(문서/작업 수준 시간 초과 및 디자인 종료.md 참조) ----------
//
// 실행당으로 끝나는 단어는 [두 세트]입니다. 실행당은 "이 실행에 대한 예산이 모두 사용되었습니다"입니다. 작업 시간 초과는
// "모든 임무가 끝나가고 있습니다." 의미론은 종종 반대입니다(특히 플래너: 실행당 "계획을 멈추지 마세요"라고 말합니다.
// 작업 시간 초과는 "계획을 중지하고 해당 지점에 도달하면 최종 판단을 내리십시오"를 의미합니다. 작업자/계획자용으로만 구성됩니다.

// WrapupTaskTimeoutOverride / ...TurnsOverride: 작업 시간 초과 종료 단어 및 라운드 수의 DB 재정의
// (agents.task_timeout_wrapup_prompt/_max_turns에 연결, 작업자/계획자에게만 해당)
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**전체 작업의 제한 시간에 도달하여 곧 종료됩니다.** 이번 실행의 예산이 아니라 전체 탐색의 제한 시간입니다. 마지막으로 (1) 파악했지만 아직 저장하지 않은 내용을 모두 기록하세요. 새 자산은 insert_assets, 탐색 결과와 사실은 record_fact, 확인된 취약점은 report_finding을 사용하세요. (2) 새 명령이나 탐지를 시작하지 마세요. (3) 마지막에 이 실행 계획의 핵심 결론을 일반 텍스트 한 문장으로 요약하세요."

const plannerTaskTimeoutDefault = "**전체 작업의 제한 시간에 도달하여 곧 종료됩니다.** 이번 라운드만이 아니라 전체 작업이 끝납니다. 현재까지의 모든 사실과 발견 사항을 바탕으로 목표를 최종 판정하세요. 증거로 달성이 입증된 목표는 prove_goal로 met 상태를 표시하세요. **새 실행 계획은 만들지 마세요.** 지금 등록해도 실행되지 않습니다. 판정을 마친 뒤 종료하고 요약문은 출력하지 마세요."

// TaskTimeoutWrapupDefault는 에이전트 작업 시간 초과의 기본 제공 기본 종료 단어를 반환합니다(백그라운드 자리 표시자/복원 기본값의 경우).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // 구성되지 않음(mainagent/chat)은 빈 문자열을 반환합니다.
}

// solveTaskTimeoutWrapup: DB 재정의(null이 아님) > 기본 제공 기본값. 빈 문자열은 에이전트에 작업 시간 초과 단어가 없음을 나타냅니다.
// (비작업자/계획자) 호출자는 이때 실행별 단어를 대체해야 합니다.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // 기본적으로 실행당 라운드 수가 사용됩니다.
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - 고정=true → 이 실행은 작업 기한에 의해 고정됩니다. 시간 초과 종료로 인해 = 작업 기한 → 작업 시간 초과 단어;
//     MaxTurns가 종료되기 때문에 = 핀치 창의 단계 수가 먼저 소진되고 작업에 아직 몇 분이 남아 있으므로 → 실행별 단어로 돌아갑니다.
//   - 고정=false → 아직 작업 초기입니다. 두 가지 이유 모두 실행별 단어를 사용합니다(예: WrapupSettlement로 퇴화).
//
// 하네스에 부여된 PromptByReason은 마지막에 [실제] 이유에 따라 현장에서 선택되며, 빌드가 없을 경우 불일치됩니다.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // 맨 아래로 돌아 가기 (또한 고정되지 않은 두 가지 이유의 값)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // 임무 완수
				harness.ReasonMaxTurns: perRun, // 먼저 단계 수가 소진되고 작업에 아직 시간이 남아 있습니다.
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
