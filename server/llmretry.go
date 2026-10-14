package server

import (
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// 재시도 전략의 서버측 구문 분석은 docs/LLM Retry Design.md를 참조하세요. 5층:
//   - 연결 설정/빈 응답/동일 제공자 보안 창은 "엔드포인트를 따르며" 각 LLM 구성에 의해 재정의될 수 있습니다.
//     전역 기본값(프로파일의 항목이 비어 있는 경우 전역 항목을 상속하고 전역 항목이 구성되지 않은 경우 내장된 기본값이 사용됩니다)
//   - 회로 차단기/의도된 재실행은 프로세스 수준에 있으며 전역 복사본만 있습니다.
//
// 전역 전략은 한 번에 한 행의 DB 설정을 읽고 콜 포인트는 모두 저주파 경로(빌딩 공급자, 작업 종료,
//구성 저장) 다른 캐싱 계층을 추가할 가치가 없습니다. 예외는 회로 차단기 매개변수입니다. 이는 오류 경로에서 매번 읽어야 하므로
// applyRetryPolicy에 의해 레지스트리로 푸시되었습니다.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// 여기서의 횟수는 "0=default/negative=off"의 원래 의미를 유지합니다: SDK의 MaxRetries /
		// EmptyResponseRetries는 그것과 완전히 동형이므로, 그냥 그 자체로 구문 분석하도록 놔두세요.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// 회로 차단기(폴링 냉각)의 기본값은 llmpool의 내장 값과 일치합니다. 여기서는 "사용자가 값을 구성"하는 경우에만 재정의됩니다.
// 인텐트 재실행에 대한 기본값은engine.go의 modelErrorRetries / modelErrorRetryBackoff를 참조하세요.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// 주입합니다(steerHooks.Stop 참조). 의도적으로 레이어 ②의 손잡이를 재사용합니다 —— "빈 응답
// "재시도 횟수": 둘 다 동일한 작업을 수행하는 두 가지 방법입니다. SDK 계층은 "단일 콘텐츠 블록이 아님"을 관리하며 방법은 다음과 같습니다.
// 동일한 요청이 그대로 재발행됩니다. 여기서는 "생각만 하고 텍스트도 도구도 아닌" 것을 의미하며 방법은 다음과 같은 지침을 추가하는 것입니다.
// 모델은 기존 생각을 계속 이어갑니다(컨텍스트의 모양에 따라 결정되는 유휴 상태인 경우 있는 그대로 다시 보내는 것은 의미가 없습니다). 짧은 구경
// 차이점은 SDK가 "이벤트 발생 여부"를 기반으로 하는 반면 증분 자체는 이벤트라는 것입니다. 그러나 사용자 구성은
// "빈 응답을 몇 번 재시도한다"라고 표현하고 싶은 것은 "모델이 실질적인 내용을 생산하지 못하면 다시 시도한다"는 것이다. 두 레이어는 한 번만 공유합니다.
// 그래야만 이 사고방식과 일치할 수 있습니다.
//
// 특정 프로필의 적용 범위 대신 전역 정책을 읽으십시오. 실행으로 인해 장애 조치로 인해 프로필이 변경될 수 있으며 이는
// 이는 전체 의도의 총량이며 끝점의 변경에 따라 변경되어서는 안 됩니다. 의미 체계는 SDK의 emptyRetries()와 동일합니다.
// 0 = 기본 defaultEmptyTurnNudges;-1(음수) = 유휴 상태를 끄고 계속 실행합니다. >0 = 이 값을 사용합니다.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
