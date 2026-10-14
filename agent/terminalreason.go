package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/harness"
)

// runTrace retains the latest tool call so an interrupted run can identify the
// operation that was still in flight.
type runTrace struct {
	startedAt time.Time
	id        string
	name      string
	input     string
	at        time.Time
	pending   bool
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

var reasonHint = map[harness.TerminalReason]string{
	harness.ReasonCompleted:         "모델이 정상적으로 종료했지만 텍스트 요약을 남기지 않았습니다. 확인한 사실과 자산은 이번 실행의 도구 호출 기록에서 확인하세요.",
	harness.ReasonMaxTurns:          "최대 실행 단계(MaxTurns)에 도달했습니다. 마무리 단계에서 확인한 사실과 자산을 기록했으며, 이 의도는 더 진행하지 않는 상태로 표시됩니다.",
	harness.ReasonTimeout:           "실행 시간 제한(MaxDuration)에 도달했습니다. 실행 중인 도구를 중단하고 확인한 사실과 자산을 기록한 뒤 이 의도를 종료합니다.",
	harness.ReasonModelError:        "모델 또는 API 호출이 실패했고 재시도 횟수도 소진되었습니다. 네트워크, 인증, 사용량 제한, 제공업체 오류 등이 원인일 수 있습니다. 의도를 다시 실행하거나 방법을 바꾸기 전에 get_worker_trace로 실행 기록을 확인하세요.",
	harness.ReasonBlockingLimit:     "요청을 보내기 전에 컨텍스트 길이가 제한에 걸렸습니다. 의도 범위를 좁히거나 컨텍스트를 정리해야 합니다.",
	harness.ReasonPromptTooLong:     "프롬프트가 너무 길고 컨텍스트 압축 재시도도 소진되어 실행을 계속할 수 없습니다.",
	harness.ReasonImageError:        "현재 모델이 이 실행의 이미지 콘텐츠를 지원하지 않습니다. 이미지를 지원하는 모델을 사용하거나 도구가 이미지를 반환하지 않도록 설정하세요.",
	harness.ReasonStopHookPrevented: "종료 후크가 실행 종료를 막았지만 이어서 진행할 수도 없었습니다. 작업 보호 규칙을 확인하세요.",
	harness.ReasonHookStopped:       "도구 또는 후크가 실행을 중지했습니다. 범위를 벗어난 대상이나 비활성화된 명령 등이 원인일 수 있으니 마지막 도구 결과를 확인하세요.",
	harness.ReasonAbortedStreaming:  "모델이 응답을 생성하는 중 실행이 취소되었습니다.",
	harness.ReasonAbortedTools:      "도구가 실행되는 중 실행이 취소되었습니다.",
}

// terminalText renders a terminal event with no final text into a compact summary
// and a Markdown detail block.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason := term.Reason
	aborted := reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools
	// Prompt may return ctx.Err directly without a terminal event. Preserve the
	// cancellation cause instead of falling back to an empty/unknown terminal reason.
	if reason == "" && ctx.Err() != nil {
		aborted = true
	}

	var sum string
	if aborted {
		_, short, _, ok := AbortReason(ctx)
		if !ok {
			short = "취소 사유를 확인할 수 없습니다"
		}
		stage := "실행 중"
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = "모델 출력 단계"
		case harness.ReasonAbortedTools:
			stage = "도구 실행 단계"
		}
		sum = "(실행 중단: " + short + "; 중단 시점: " + stage + progressSuffix(term, tr) + "; 작업 미완료)"
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = "(실행 한도 도달(" + string(reason) + "): 확인한 사실을 기록했습니다" + progressSuffix(term, tr) + "; 이번 실행의 텍스트 요약은 없습니다)"
	} else {
		hint := terminalReasonHint(reason)
		sum = "(텍스트 요약 없음; 최종 상태 " + terminalReasonLabel(reason) + ": " + firstLine(hint, 80) + ")"
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	displayReason := terminalReasonLabel(reason)
	fmt.Fprintf(&b, "- **최종 상태**: `%s` - %s\n", displayReason, terminalReasonHint(reason))
	if aborted {
		code, _, why, ok := AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **중단 이유**(`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **중단 이유**: 가져올 수 없습니다. 취소 당사자가 context.WithCancelCause를 통해 명명된 이유를 첨부하지 않았을 수 있습니다.\n")
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, "- **기본 오류**: `%v`\n", term.Err)
	}
	if aborted && strings.TrimSpace(term.Text) != "" {
		b.WriteString("- **중단 전 생성된 출력 일부**:\n\n")
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **실행됨**: %d번의 모델 라운드\n", term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **이번 실행에 걸린 시간**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **누적 토큰**: 입력 %d / 출력 %d / 캐시된 읽기 %d / 캐시된 쓰기 %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString("- **도구 호출**: 도구 호출 없이 이 실행이 종료되었습니다\n")
	} else if tr.pending {
		fmt.Fprintf(&b, "- **중단 시 실행 중인 도구**: `%s`(%s가 실행되었으며 **결과가 반환되지 않음**)\n\n  ```json\n  %s\n  ```\n",
			tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, "- **중단 전 마지막 도구**: `%s`(정상적으로 반환됨)\n", tr.name)
	}
	return sum, b.String()
}

func terminalReasonLabel(reason harness.TerminalReason) string {
	if reason == "" {
		return "context_canceled"
	}
	return string(reason)
}

func terminalReasonHint(reason harness.TerminalReason) string {
	if hint := reasonHint[reason]; hint != "" {
		return hint
	}
	if reason == "" {
		return "실행 중인 컨텍스트가 취소되었지만 내부적으로 터미널 이벤트가 생성되지 않았습니다"
	}
	return "최종 상태를 알 수 없습니다. 하네스에 TerminalReason이 추가되었을 수 있습니다. ReasonHint를 추가하세요."
}

func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d회", term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", 실행 경과 " + strings.Join(parts, " / ")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}
