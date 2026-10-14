package db

import (
	"encoding/json"
	"time"
)

// LLM 재시도 전략: 5계층 재시도의 "횟수 + 간격"에 대한 전역 구성, docs/LLM 재시도 design.md를 참조하세요.
// 이는 설정 테이블의 JSON 값에 존재합니다. 이는 전체 시스템에 대한 실행 매개변수이므로 테이블을 열 가치가 없습니다.
// 내장된 기본 코드를 읽으므로 키가 존재하지 않을 때(새 라이브러리/구성되지 않음)의 동작은 하드 코딩된 상수 시대와 완전히 동일합니다.

const settingLLMRetryPolicy = "llm_retry_policy"

// RetryRule is one layer's knob pair. The zero value means "unset":
//
//	시도 0 = 내장된 기본 시간을 사용합니다. -1 = 레이어를 닫고 다시 시도하세요. >0 = 이 값 사용
//	IntervalMS 0 = 레이어의 원래 간격 전략(일반적으로 지수 백오프)을 사용합니다. >0 = 대신 고정된 밀리초 간격을 사용합니다.
//
// -1은 "0번"이 아닌 "명시적으로 꺼짐"입니다. 0이 이미 "구성 해제됨"에 의해 점유되어 있기 때문입니다.
type RetryRule struct {
	Attempts   int `json:"attempts"`
	IntervalMS int `json:"interval_ms"`
}

// Interval returns the configured fixed interval, or 0 when unset (caller keeps
// its own default ladder).
func (r RetryRule) Interval() time.Duration {
	if r.IntervalMS <= 0 {
		return 0
	}
	return time.Duration(r.IntervalMS) * time.Millisecond
}

// Or returns the rule with each unset field filled in from fallback. Used to
// layer a profile override on top of the global policy field by field, so a
// profile that only pins the interval still inherits the global count.
func (r RetryRule) Or(fallback RetryRule) RetryRule {
	if r.Attempts == 0 {
		r.Attempts = fallback.Attempts
	}
	if r.IntervalMS == 0 {
		r.IntervalMS = fallback.IntervalMS
	}
	return r
}

// retry knob bounds. A count above the cap turns a blip into a token bonfire;
// an interval above an hour outlives any transient failure worth waiting out.
const (
	maxRetryAttempts   = 20
	maxRetryIntervalMS = 3600_000 // 1h
)

// Clamped returns the rule with out-of-range values pulled back into the sane
// band (attempts within [-1, 20], interval within [0, 1h]).
func (r RetryRule) Clamped() RetryRule {
	if r.Attempts < -1 {
		r.Attempts = -1
	}
	if r.Attempts > maxRetryAttempts {
		r.Attempts = maxRetryAttempts
	}
	if r.IntervalMS < 0 {
		r.IntervalMS = 0
	}
	if r.IntervalMS > maxRetryIntervalMS {
		r.IntervalMS = maxRetryIntervalMS
	}
	return r
}

// Clamped bounds a profile's override the same way the global policy is bounded,
// so a hand-crafted API payload can't land a value the CHECK constraint rejects.
func (o RetryOverride) Clamped() RetryOverride {
	o.Connect, o.Empty, o.Stream = o.Connect.Clamped(), o.Empty.Clamped(), o.Stream.Clamped()
	return o
}

// LLMRetryPolicy는 5계층 재시도 구성을 보유합니다. 연결/비어 있음/스트림은
// per-request layers (a profile may override them, see LLMProfile.Retry);
// Breaker and Intent are process-wide by nature and live only here.
type LLMRetryPolicy struct {
	//연결: SDK가 연결 설정을 다시 시도합니다(스트리밍 시작 전 연결 재설정/시간 초과/429/5xx). 기본값은 3회, 지수 백오프입니다.
	Connect RetryRule `json:"connect"`
	// 비어 있음: SDK 빈 응답 재시도(콘텐츠 블록 없이 완료, openai 형식만 해당) 기본값은 2회, 지수 백오프입니다.
	Empty RetryRule `json:"empty"`
	// 스트림: 공급자 안전 창 재시도와 동일합니다(출력이 전달되기 전 스트림 재생). 기본값은 2회이며 인덱스는 0.5초부터 시작합니다(최대 4초).
	Stream RetryRule `json:"stream"`
	// 차단기: 폴링 차단기. 시도=여러 번의 연속적인 순간 실패가 퓨즈를 트리거합니다(기본값 3, -1=순간적인 실패가 융합되지 않음,
	// 잔액 부족/키 무효화와 같은 심각한 오류는 여전히 즉시 융합됩니다. IntervalMS=고정 냉각 시간(0=기본값 1/5/30분 변화).
	Breaker RetryRule `json:"breaker"`
	// 의도: 작업자는 model_error로 끝난 후 전체 의도를 다시 실행합니다. 기본값은 2회, 고정 3초입니다.
	Intent RetryRule `json:"intent"`
}

// Clamped returns the policy with every rule clamped.
func (p LLMRetryPolicy) Clamped() LLMRetryPolicy {
	p.Connect, p.Empty, p.Stream = p.Connect.Clamped(), p.Empty.Clamped(), p.Stream.Clamped()
	p.Breaker, p.Intent = p.Breaker.Clamped(), p.Intent.Clamped()
	return p
}

// LLMRetryPolicy reads the global retry policy. A missing or unparseable value
// yields the zero policy — i.e. every layer on its built-in default.
func (d *DB) LLMRetryPolicy() LLMRetryPolicy {
	var p LLMRetryPolicy
	if d == nil {
		return p
	}
	raw, ok, err := d.GetSetting(settingLLMRetryPolicy)
	if err != nil || !ok || raw == "" {
		return p
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return LLMRetryPolicy{}
	}
	return p.Clamped()
}

// SetLLMRetryPolicy persists the global retry policy (values are clamped first).
func (d *DB) SetLLMRetryPolicy(p LLMRetryPolicy) error {
	raw, err := json.Marshal(p.Clamped())
	if err != nil {
		return err
	}
	return d.SetSetting(settingLLMRetryPolicy, string(raw))
}
