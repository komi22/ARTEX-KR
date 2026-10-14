package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// 유휴 라운드(생각만 하고 텍스트도 도구도 없음)를 식별하고 계속하려면 steerHooks.Stop을 참조하세요.

func assistantThinking(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: text, Signature: "sig"},
	}}
}

func TestIsThinkingOnlyTurn(t *testing.T) {
	toolUse := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: "먼저 포트를 스캔하세요"},
		{Type: llm.BlockToolUse, ID: "t1", Name: "run_nuclei"},
	}}
	cases := []struct {
		name string
		msgs []llm.Message
		want bool
	}{
		{"그냥 생각해봐", []llm.Message{llm.UserText("시작"), assistantThinking("생각해 보세요")}, true},
		{"생각하다+도구", []llm.Message{llm.UserText("시작"), toolUse}, false},
		{"생각하다+텍스트", []llm.Message{assistantThinking("생각해 보세요"), {
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("결론")},
		}}, false},
		{"텍스트에는 공백 문자만 있습니다.", []llm.Message{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("  \n ")},
		}}, true},
		{"완전히 비어 있음 assistant 둥근", []llm.Message{{Role: llm.RoleAssistant}}, true},
		// 도구 결과는 사용자 역할이며 판단은 가까운 오판이 아닌 이전 보조자까지 추적되어야 합니다.
		{"마지막 항목은 도구 결과입니다.", []llm.Message{toolUse, {
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1"}},
		}}, false},
		{"아니요 assistant 뉴스", []llm.Message{llm.UserText("시작")}, false},
		{"빈 역사", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isThinkingOnlyTurn(c.msgs); got != c.want {
				t.Fatalf("isThinkingOnlyTurn = %v, want %v", got, c.want)
			}
		})
	}
}

// fakeHooks는 steerHooks가 내부 결정을 존중하는지 확인하는 데 사용되는 프로그래밍 가능한 내부 HookRunner입니다.
type fakeHooks struct {
	prevent  bool
	blocking []string
	msg      string
}

func (f fakeHooks) PreToolUse(context.Context, string, []byte) (bool, string, []byte) {
	return false, "", nil
}
func (f fakeHooks) PostToolUse(context.Context, string, []byte, []byte, bool) {}
func (f fakeHooks) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return f.prevent, f.blocking, f.msg
}

func TestSteerHooksStopNudgesEmptyTurn(t *testing.T) {
	empty := []llm.Message{assistantThinking("먼저 하위 도메인을 열거해야 합니다.")}

	t.Run("유휴 라운드에 계속 명령을 주입합니다.", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges, label: "worker-1 · #1"}
		prevent, blocking, _ := h.Stop(context.Background(), empty)
		if prevent {
			t.Fatal("유휴 라운드는 갑자기 중단되어서는 안 됩니다.")
		}
		if len(blocking) != 1 || blocking[0] != emptyTurnNudge {
			t.Fatalf("blocking = %v, want [emptyTurnNudge]", blocking)
		}
	})

	t.Run("텍스트나 도구가 있을 때 개입하지 마세요.", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		normal := []llm.Message{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{llm.TextBlock("스캔이 완료되었지만 열려 있는 포트가 없습니다.")},
		}}
		if _, blocking, _ := h.Stop(context.Background(), normal); blocking != nil {
			t.Fatalf("노멀엔딩이 아이들링으로 오인되었습니다.: %v", blocking)
		}
		if n := h.nudges.Load(); n != 0 {
			t.Fatalf("관련되지 않은 경우에는 계산되지 않습니다., got %d", n)
		}
	})

	t.Run("상한 도달 후 해제 및 종료", func(t *testing.T) {
		const limit = 5 // 사용자는 "빈 응답 재시도 횟수"를 5로 설정합니다.
		h := steerHooks{nudges: &atomic.Int64{}, limit: limit}
		for i := 1; i <= limit; i++ {
			if _, blocking, _ := h.Stop(context.Background(), empty); len(blocking) != 1 {
				t.Fatalf("아니요. %d 횟수는 여전히 할당량 내에 있어야 합니다., blocking = %v", i, blocking)
			}
		}
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("상한치를 초과한 후에도 여전히 주입 중: %v", blocking)
		}
	})

	// "빈 응답 재시도 횟수"는 -1로 구성됩니다. = 이 레이어가 꺼지고, emptyTurnNudgeLimit는 0으로 확인됩니다.
	t.Run("구성이 닫힐 때 개입하지 마세요.", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: 0}
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("닫혀 있고 여전히 주사 중: %v", blocking)
		}
	})

	t.Run("inner 급제동 시 오버레이하지 않기로 결정", func(t *testing.T) {
		h := steerHooks{inner: fakeHooks{prevent: true, msg: "guard 끝내기를 거부하다"}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		prevent, blocking, msg := h.Stop(context.Background(), empty)
		if !prevent || msg != "guard 끝내기를 거부하다" || blocking != nil {
			t.Fatalf("inner 하드 스톱이 다시 작성되었습니다.: prevent=%v blocking=%v msg=%q", prevent, blocking, msg)
		}
		if n := h.nudges.Load(); n != 0 {
			t.Fatalf("양보하다 inner 할당량은 다음과 같은 경우에 사용되어서는 안 됩니다., got %d", n)
		}
	})

	t.Run("inner 계속 실행 시 중첩 없음", func(t *testing.T) {
		h := steerHooks{inner: fakeHooks{blocking: []string{"guard 계속하는 이유"}}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		_, blocking, _ := h.Stop(context.Background(), empty)
		if len(blocking) != 1 || blocking[0] != "guard 계속하는 이유" {
			t.Fatalf("inner 계속 소식이 다시 작성되었습니다: %v", blocking)
		}
	})

	t.Run("카운터가 설치되지 않은 경우 동작은 변경되지 않습니다.", func(t *testing.T) {
		h := steerHooks{limit: defaultEmptyTurnNudges} // 예를 들어, 미래의 다른 콜 포인트에서는 넛지 전달을 잊어버립니다.
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("카운터가 없을 때는 주입해서는 안 됩니다.: %v", blocking)
		}
	})
}
