package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "사용자가 작업을 일시중지했습니다",
		"사용자가 작업 제어 인터페이스(POST /api/tasks/{id}/control, action=pause)를 통해 작업을 일시 중지했습니다. 이 플래너/작업자 실행이 자동으로 취소되었습니다. 실행 의도는 프론티어(open)로 반환되며 작업이 복원된 후 다시 선택되어 처음부터 실행됩니다.")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "스케줄링 에이전트가 작업을 일시 중지했습니다",
		"오케스트레이션 에이전트가 이 작업을 일시중지하기 위해 pause_task 도구를 호출했습니다. 이 플래너/작업자 실행이 적극적으로 취소되었습니다. 실행 의도는 프론티어(open)로 반환되고 복구 후 다시 실행됩니다")
	AbortTaskDeleted = cause("task_deleted", "작업이 삭제되었습니다",
		"작업을 삭제하는 중(DELETE /api/tasks/{id})이며 삭제 장벽으로 인해 실행 중인 Planner, Worker 및 작업의 기본 에이전트가 취소되었습니다. 이 작업의 결과는 더 이상 사용되지 않습니다")
	AbortPausedOnReload = cause("paused_on_reload", "백엔드가 작업의 일시 중지 상태를 재개했습니다",
		"백엔드가 시작되면 데이터베이스의 지속 상태에 따라 작업 일시 중지가 재개됩니다. 이 실행이 취소되었습니다. 정상적인 상황에서는 복구 단계에서 실행 중인 에이전트가 없습니다")
	AbortGoalMet = cause("goal_met", "기획자는 작업 목표가 달성되었다고 판단합니다",
		"플래너는 작업 목표가 달성되었다고 판단하고 완료할 작업을 설정한 후 아직 실행 중인 작업자를 취소합니다. 이러한 의도는 실패 대신 중지된 것으로 표시됩니다.")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "작업 시간 초과 완료를 위한 대기 시간이 소진되었습니다",
		"작업이 시간 초과에 도달한 후 실행 중인 작업자가 정상적으로 종료될 때까지 기다리지만 90초 드레이닝 유예가 여전히 부족하여 강제 취소가 수행됩니다. 의도는 소진된 것으로 표시되며 마감 단계에서 작성된 사실과 자산은 유지됩니다.")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "기획자가 이 의도를 종료했습니다",
		"플래너는 kill_work를 호출하여 이 의도를 적극적으로 종료하는데, 이는 일반적으로 방향이 벗어났거나 더 이상 가치가 없음을 의미합니다. 의도는 중지된 것으로 표시되며 자동으로 다시 획득되지 않습니다.")
	AbortWorkPausedByUser = cause("work_paused_by_user", "사용자가 이 작업자 인텐트를 일시중지했습니다",
		"사용자가 실행 중인 작업자를 일시중지했습니다. 이 호출은 취소되고 의도는 일시 중지된 것으로 변환됩니다. 등록된 모든 의도, 사실, 취약점 및 활동 기록은 유지되며 복구 후 처음부터 다시 실행할 수 있습니다.")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "사용자가 이 작업자 인텐트를 삭제했습니다",
		"사용자가 실행 중인 작업자를 삭제했습니다. 이 호출는 취소되었습니다. 작업자가 쓰기 영역을 종료한 후 서버는 사용자가 선택한 삭제 모드에 따라 인텐트를 처리합니다. 기록 보존 삭제는 삭제 상태만 표시하고 모든 결과를 유지합니다. 완전 삭제는 이 의도와 이 의도에서만 파생된 하위 노드를 제거합니다.")
	AbortWorkFinished = cause("work_finished", "작업자가 정상적으로 종료되고 컨텍스트가 해제되었습니다",
		"Worker가 정상적으로 종료되었으며 엔진은 detachWork에서 컨텍스트 리소스를 해제합니다. 이것은 실행 중인 인터럽트가 아닙니다. 인터럽트 메시지에 나타나면 취소 이벤트와 종료 이벤트 사이에 경쟁 조건이 있음을 의미합니다.")
	AbortPausedRaceGuard = cause("paused_race_guard", "작업이 일시 중지된 동안 새 실행 시작 거부",
		"작업이 일시 중지된 상태에 있으면 엔진은 새로운 실행 컨텍스트 발행을 거부합니다. 이는 청구와 일시 중지 사이의 경쟁 조건으로 인해 작업자가 계속 시작되는 것을 방지하는 데 사용됩니다. 선점한 의도는 대기열(open)로 반환됩니다")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "사용자가 이 대화를 중단했습니다",
		"사용자가 중지를 클릭하여 기본 에이전트 또는 세션 에이전트의 현재 라운드를 적극적으로 중지했습니다. 생성된 활동 기록은 그대로 유지되며, 다음 메시지를 계속해서 보내실 수 있습니다")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "작업이 일시 중지되고 기본 에이전트 대화가 중단되었습니다.",
		"사용자가 작업을 일시 중지하면 실행 중인 기본 에이전트 대화도 동시에 취소됩니다. 생성된 활동 기록은 유지됩니다. 이 메시지는 작업을 재개한 후에 자동으로 재생되지 않습니다.")
	AbortChatTurnFinished = cause("chat_turn_finished", "이번 대화는 정상적으로 종료되었으며 내용이 공개되었습니다",
		"이번 대화 라운드는 정상적으로 종료되었으며 서버는 이번 라운드의 컨텍스트 리소스를 해제하고 있습니다. 이것은 실행 중인 인터럽트가 아닙니다. 인터럽트 메시지에 나타나면 취소 이벤트와 종료 이벤트 사이에 경쟁 조건이 있음을 의미합니다.")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "백엔드 프로세스 종료 중",
		"백엔드 프로세스가 SIGINT 또는 SIGTERM을 수신했으며 다시 시작, 업데이트 또는 종료 중입니다. 실행 중인 모든 에이전트가 취소됩니다. 다시 시작한 후 남은 실행 의도는 열기로 재설정되고 다시 실행됩니다.")
	AbortRunHardTimeout = cause("run_hard_timeout", "단일 실행에 대한 하드 타임아웃이 트리거되었습니다",
		"단일 실행이 소프트 벽시계 예산 및 추가 유예를 초과합니다. 이는 모델 요청 또는 도구가 오랫동안 반환되지 않아 정상적인 라운드 경계 닫기가 실행될 수 없음을 나타냅니다. 중단 이전에 반환되지 않은 마지막 도구 호출을 집중적으로 확인하시기 바랍니다")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "업스트림 컨텍스트가 마감일에 도달",
			"업스트림 컨텍스트가 마감일에 도달했지만 설정자가 WithTimeoutCause를 통해 명명된 이유를 첨부하지 않았습니다. " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "취소 당사자가 명시적인 이유를 첨부하지 않았습니다",
			"업스트림 컨텍스트가 취소되었지만 취소 당사자가 context.WithCancelCause를 통해 명명된 이유를 첨부하지 않았습니다. Agent/cancelcause.go에서 사유를 등록하시고 취소포인트에 접속해주세요", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
