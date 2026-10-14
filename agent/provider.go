// Package agent wires real LLM-driven planner and work agents (on top of the
// 에이전트 코어 SDK)를 듀얼 SQLite 그래프로 변환합니다. 문서/ARTEX-Architecture Design.md를 참조하세요.
// §4.3 (planner) and §4.4 (work agent).
//
// Provider configuration is read from the environment so the system runs with
// any Anthropic- or OpenAI-format endpoint. If no key is configured, FromEnv
// returns ok=false and the exploration engine stays idle (an LLM is required).
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/transcript"
)

// Config describes the LLM backend resolved from the environment.
type Config struct {
	Format  llm.Format
	BaseURL string
	APIKey  string
	Model   string
	// Proxy routes all LLM requests through the given proxy URL (http/https/socks5,
	// optionally with user:pass@ credentials). Empty means direct — it does NOT
	// fall back to the standard *_PROXY environment variables.
	Proxy string
	// RatePerSecond / RatePerMinute cap the shared request rate across ALL agents
	// using the provider (0 = that window unlimited).
	RatePerSecond float64
	RatePerMinute float64
	// ContextWindowK is the model's context window in K tokens (user-configured),
	// used to size compaction thresholds. 0 = default; see CompactionWindow.
	ContextWindowK int
	// ThinkingType은 사고 "스위치" 필드(thinking.type)를 독립적으로 제어합니다.
	//   "" = 보내지 않음(기본값), "disabled" = 명시적으로 끔,
	//   "enabled" = 켬. ReasoningEffort와 독립적으로 설정합니다.
	//   사고는 강도 매개변수만으로 활성화될 수 있으므로 둘 다 독립적으로 설정할 수 있습니다.
	ThinkingType string
	// ReasoningEffort는 사고 "강도" 필드를 독립적으로 제어합니다.
	// "" = 보내지 않음(기본값); "low"/"medium"/"high"/"xhigh"/"max" = 각 추론 강도.
	//   OpenAI는 최상위 Reasoning_effort에 매핑됩니다. Anthropic은 output_config.effort에 매핑됩니다.
	ReasoningEffort string
	// Stream은 SSE 사용 여부입니다. true(기본값)는 스트리밍,
	// false는 stream:false로 전체 JSON 응답을 한 번에 받습니다.
	// 비스트리밍은 일부 게이트웨이의 SSE 오류를 피할 수 있지만 실시간 진행 상황은 표시되지 않습니다.
	// agentcore.Options.NonStreaming = !Stream으로 매핑됩니다.
	Stream bool
	// MaxTokens는 단일 응답에 대한 출력(토큰)의 상한입니다. 0 = 서버의 기본값에 따라 이 필드를 보내지 않음
	// (역사적 행위). ContextWindowK와 다릅니다. 후자는 모델의 총 용량이며 압축 임계값을 계산하기 위해 로컬로만 사용됩니다.
	// 요청에 나타나지 않습니다. 이 값은 각 요청마다 내보내집니다. Agentcore.Options.MaxTokens에 매핑됩니다.
	MaxTokens int
	// MaxTokensField는 MaxTokens에 사용할 요청 필드 이름을 선택합니다. format=openai에만 적용됩니다.
	//   "" = max_tokens(기본값); "max_completion_tokens" = 새 필드.
	// OpenAI 추론 모델(o 시리즈/GPT-5)은 후자만 인식하며 max_tokens를 수신할 때 직접 보고합니다.
	//지원되지 않는_매개변수; 대부분의 호환 게이트웨이는 전자만 인식하기 때문에 자동으로 추론하지 않고 엔드포인트에 따라 사용자가 선택하도록 남겨둔다.
	MaxTokensField string
	// 비어 있지 않은 경우 SessionHeaderKey는 각 LLM 요청이 이 값을 가진 사용자 정의 HTTP 헤더를 가져오도록 허용합니다.
	// 헤더 값은 [현재 세션의 세션 ID]입니다(chat session=conv-<id>,worker=exp<x>-worker-i<intent>).
	// 등은 WorkerSessionID를 참조하세요. session-id 헤더를 기반으로 캐싱/고정 라우팅을 요청하는 일부 게이트웨이에 사용됩니다.
	// 비어 있음 = 보내지 않습니다. 값은 transcription.WithSessionID 및 RoundTripper에 의해 요청 컨텍스트에 정지됩니다.
	// 패딩을 읽으면 동일한 공유 공급자가 세션마다 다른 헤더 값을 내보낼 수도 있습니다.
	SessionHeaderKey string
	// Retry는 구성(프로필 적용 범위 → 전역 정책 → 기본 제공 기본값)을 구문 분석한 후 재시도 매개 변수입니다.
	// 서버 측 분석). 세 번째 계층의 의미는 RetryConfig를 참조하세요. 0 값 = 내장된 기본값을 완전히 사용합니다.
	Retry RetryConfig
}

// RetryConfig는 LLM 구성과 함께 제공되는 재시도 매개변수입니다. 각 레이어의 "횟수"는 통일된 의미를 갖습니다.
// 0 = 내장된 기본 번호를 사용합니다. 음수 = 레이어를 닫고 다시 시도하세요. >0 = 이 값을 사용합니다. 각 레이어의 "간격":
// 0 = 레이어의 원래 지수 백오프를 사용합니다. >0 = 대신 이 고정 간격을 사용합니다.
type RetryConfig struct {
	// ConnectAttempts/ConnectInterval: SDK 연결 재시도(연결 재설정/시간 초과/429/5xx, 스트림 시작 전),
	//llm.Config.MaxRetries / RetryInterval에 직접 매핑됩니다. 기본값은 0.5초부터 시작하여 3회입니다(최대 8초).
	ConnectAttempts int
	ConnectInterval time.Duration
	// EmptyAttempts/EmptyInterval: SDK 빈 응답 재시도(완료되었지만 콘텐츠 블록이 없고 openai만 있음)
	// 형식), llm.Config.EmptyResponseRetries/EmptyResponseInterval에 매핑됩니다.
	// 기본값은 2배이며 동일한 지수 기울기입니다.
	EmptyAttempts int
	EmptyInterval time.Duration
	// StreamAttempts/StreamInterval: 공급자 보안 창 재시도와 동일 - 이 프로젝트는 SDK 위에 추가됩니다.
	// 하나의 레이어는 "출력이 호출자에게 전달되지 않은 경우" 중단/과부하/인스트림 429만 재생합니다. SDK에서는 볼 수 없습니다.
	// server/task_llm.go에서 사용됩니다. 기본값은 2회이며 인덱스는 0.5초부터 시작합니다(최대 4초).
	StreamAttempts int
	StreamInterval time.Duration
}

// compaction window resolution bounds (in K tokens). Below the floor the
// threshold math (window − summary reserve − buffer) would go non-positive and
// compaction would fire every turn; above the cap it would never fire.
const (
	defaultWindowK = 200  // unset → assume a 200K window (Claude default)
	minWindowK     = 32   // floor so effectiveWindow stays comfortably positive
	maxWindowK     = 1000 // cap at 1M tokens (user request)
)

// CompactionWindow returns the model context window in TOKENS for compaction
// thresholds, resolved from the user-configured size (ContextWindowK). 0/unset →
// a 200K default; otherwise clamped to [32K, 1M] so compaction stays effective.
func (c Config) CompactionWindow() int {
	k := c.ContextWindowK
	if k <= 0 {
		k = defaultWindowK
	}
	if k < minWindowK {
		k = minWindowK
	}
	if k > maxWindowK {
		k = maxWindowK
	}
	return k * 1000
}

// compactionConfig builds the agent-core compaction config for a context window
// in tokens. agentcore.NewSession wires the summarizer (same provider) when this
// is set on Options.Compaction.
func compactionConfig(windowTokens int) *compaction.Config {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return &compaction.Config{ContextWindow: windowTokens}
}

// FromEnv resolves the LLM provider config:
//
//	ARTEX_LLM_PROVIDER = anthropic|openai (default: inferred from keys)
//	ARTEX_LLM_MODEL    = model id        (default: per provider)
//	ARTEX_LLM_BASE_URL = endpoint        (optional)
//	ARTEX_LLM_PROXY    = proxy URL        (optional; http/https/socks5)
//	ANTHROPIC_API_KEY / OPENAI_API_KEY         = credentials
func FromEnv() (Config, bool) {
	prov := os.Getenv("ARTEX_LLM_PROVIDER")
	anthKey := os.Getenv("ANTHROPIC_API_KEY")
	oaiKey := os.Getenv("OPENAI_API_KEY")

	if prov == "" {
		switch {
		case anthKey != "":
			prov = "anthropic"
		case oaiKey != "":
			prov = "openai"
		default:
			return Config{}, false
		}
	}

	c := Config{
		BaseURL: os.Getenv("ARTEX_LLM_BASE_URL"),
		Model:   os.Getenv("ARTEX_LLM_MODEL"),
		Proxy:   strings.TrimSpace(os.Getenv("ARTEX_LLM_PROXY")),
		// 기본 스트리밍; ARTEX_LLM_STREAM=false/0/off는 비스트리밍을 명시적으로 끕니다.
		Stream: !isFalsy(os.Getenv("ARTEX_LLM_STREAM")),
	}
	switch prov {
	case "openai":
		c.Format = llm.FormatOpenAI
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		c.APIKey = anthKey
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	if c.APIKey == "" {
		return Config{}, false
	}
	return c, true
}

// ConfigFrom builds a Config from UI-provided strings (provider defaults to
// anthropic; model defaults per provider). Inputs are trimmed and the base URL
// is normalized to the API base the provider expects (the provider appends the
// endpoint path itself), so a full endpoint URL is tolerated.
func ConfigFrom(provider, model, baseURL, apiKey, proxy string) Config {
	c := Config{
		Model:   strings.TrimSpace(model),
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  strings.TrimSpace(apiKey),
		Proxy:   strings.TrimSpace(proxy),
		Stream:  true, // 기본 스트리밍; 발신자는 프로필로 재정의됩니다.
	}
	switch strings.TrimSpace(provider) {
	case "openai":
		c.Format = llm.FormatOpenAI
		// provider appends "/chat/completions"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/chat/completions"), "/")
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		// provider appends "/responses"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/responses"), "/")
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		// provider appends "/v1/messages".
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/v1/messages"), "/")
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	return c
}

// isFalsy reports whether an env-var string explicitly requests "off". Empty or
// unrecognized → false (so an unset var keeps the streaming default).
func isFalsy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// Provider returns the short provider name ("anthropic"/"openai").
func (c Config) Provider() string {
	switch c.Format {
	case llm.FormatOpenAI:
		return "openai"
	case llm.FormatOpenAIResponses:
		return "openai-responses"
	}
	return "anthropic"
}

// NewProvider builds an llm.Provider from the config. When a rate is set, the
// limiter lives on the single provider instance — so planner + all workers +
// main agent (which share this provider) are bounded by one shared rate limit.
func (c Config) NewProvider() (llm.Provider, error) {
	client, err := quotaAwareHTTPClient(c.Proxy, c.SessionHeaderKey)
	if err != nil {
		return nil, err
	}
	lc := llm.Config{
		Format:     c.Format,
		BaseURL:    c.BaseURL,
		APIKey:     c.APIKey,
		Model:      c.Model,
		HTTPClient: client,
	}
	// 스위치 및 강도의 두 필드가 투명하게 전송된다는 점을 고려하십시오(비어 있음 = 이 필드가 전송되지 않음). 두 가지를 분리하면 다음과 같습니다.
	// Thinking.type만 보내거나, 노력만 보내거나, 둘 다 보내거나 둘 다 보내지 않을 수 있습니다.
	lc.ThinkingType = c.ThinkingType
	lc.ReasoningEffort = c.ReasoningEffort
	// 출력 상한에 대한 필드 이름 선택(null = max_tokens 사용) 상한의 "값"은 여기에 없습니다. 매 라운드마다 변경됩니다.
	//Agentcore.Options.MaxTokens를 통해 공급자는 어떤 키를 넣을지 결정합니다.
	lc.MaxTokensField = c.MaxTokensField
	// 재시도 매개변수는 SDK와 동일한 의미(회수 0=기본값/음수=꺼짐, 간격 0=지수 백오프/>0=고정)를 가지며 있는 그대로 투명하게 전송됩니다.
	lc.MaxRetries = c.Retry.ConnectAttempts
	lc.RetryInterval = c.Retry.ConnectInterval
	lc.EmptyResponseRetries = c.Retry.EmptyAttempts
	lc.EmptyResponseInterval = c.Retry.EmptyInterval
	if c.RatePerSecond > 0 || c.RatePerMinute > 0 {
		lc.RateLimit = &llm.RateLimit{PerSecond: c.RatePerSecond, PerMinute: c.RatePerMinute}
	}
	return llm.NewProvider(lc)
}

// IsQuotaExhaustedMessage deliberately recognizes only explicit balance,
// billing, credit, or quota-exhaustion signals. Generic 429/rate-limit text,
// authentication failures, network errors, and server failures are excluded.
var nonFailoverHTTPStatus = regexp.MustCompile(`(?:status(?:\s+code)?|http(?:\s+status)?)\s*[=:]?\s*(?:401|403|5\d\d)\b`)
var transientQuotaLimit = regexp.MustCompile(`(?i)(?:\b(?:rpm|tpm|rpd|qps)\b|quota[_\s-]*metric|rate[_\s-]*limit|too many requests|(?:requests?|tokens?)\s+(?:per|/)\s*(?:second|minute)|(?:per|/)\s*(?:second|minute)\s+(?:requests?|tokens?)|generate[_\s-]*requests[_\s-]*per[_\s-]*(?:minute|second)|tokens?[_\s-]*per[_\s-]*(?:minute|second))`)

func IsQuotaExhaustedMessage(message string) bool {
	message = strings.ToLower(message)
	// Authentication/authorization and provider-side 5xx failures never rotate,
	// even when a gateway happens to echo a quota-looking phrase in the body.
	if nonFailoverHTTPStatus.MatchString(message) {
		return false
	}
	// Provider APIs frequently describe an ordinary rate limit as "quota
	// exceeded", especially Google-style responses containing a quota metric.
	// These limits recover with time and must stay on the current provider.
	if transientQuotaLimit.MatchString(message) {
		return false
	}
	markers := []string{
		"insufficient_quota", "quota_exceeded", "quota exceeded", "quota exhausted",
		"exceeded your current quota", "billing_hard_limit_reached",
		"billing hard limit", "billing_not_active", "credit balance", "insufficient credit",
		"insufficient balance", "balance is too low", "payment required", "status 402",
		"잔액 부족", "할당량 부족", "할당량이 소진되었습니다", "연체료",
		"\u4f59\u989d\u4e0d\u8db3", "\u989d\u5ea6\u4e0d\u8db3", "\u989d\u5ea6\u5df2\u7528\u5c3d", "\u6b20\u8d39",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	// gRPC RESOURCE_EXHAUSTED is overloaded for both account quota and ordinary
	// request-rate limiting. Preserve it as an explicit exhaustion signal only
	// when the same error does not identify a transient rate limit.
	return strings.Contains(message, "resource_exhausted") &&
		!strings.Contains(message, "rate limit") &&
		!strings.Contains(message, "too many requests")
}

// quotaAwareTransport preserves Norma's normal retry behavior except for a 429
// whose body explicitly says the account quota/balance is exhausted. Norma's
// retry loop treats every 429 as transient; normalizing only that response to
// 402 lets a task router fail over immediately while retaining the original
// response body for provider-specific classification and audit logs.
type quotaAwareTransport struct {
	base http.RoundTripper
	// sessionHeaderKey, when non-empty, is the HTTP header name each request
	// carries; its value is the session id read from the request context. Empty
	// disables it. See Config.SessionHeaderKey.
	sessionHeaderKey string
}

func (t quotaAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Custom session-id header: name is user-configured, value is THIS run's
	// session id (norma stashes it on the context via transcript.WithSessionID).
	// Stable across a session's turns and distinct across sessions — exactly what
	// a session-keyed prompt cache wants. Skipped when no session id is present.
	if t.sessionHeaderKey != "" {
		if sid := transcript.SessionIDFrom(req.Context()); sid != "" {
			req.Header.Set(t.sessionHeaderKey, sid)
		}
	}
	// When LLM recording is on, the Recorder puts a Capture on the context so the
	// raw wire bodies can be persisted. This is the only layer that still sees
	// them: norma builds the request body internally and decodes the SSE response
	// before either reaches the recorder.
	capt := llmrec.CaptureFrom(req.Context())
	capt.SetRequest(requestBodySnapshot(req))

	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	// Tee rather than read: a 200 is an SSE stream that must keep streaming. The
	// 429 branch below reads through this wrapper, so its body lands in the
	// capture before being replaced.
	resp.Body = capt.TeeResponse(resp.StatusCode, resp.Body)

	if resp.StatusCode != http.StatusTooManyRequests {
		return resp, nil
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	if readErr != nil {
		return resp, nil
	}
	if IsQuotaExhaustedMessage(string(body)) {
		resp.StatusCode = http.StatusPaymentRequired
		resp.Status = "402 Payment Required"
	}
	return resp, nil
}

// requestBodySnapshot copies an outgoing request body without consuming it.
// norma builds every model request from a *bytes.Reader, so net/http populates
// GetBody and the copy has no effect on what gets sent.
func requestBodySnapshot(req *http.Request) string {
	if req.GetBody == nil {
		return ""
	}
	rc, err := req.GetBody()
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	return string(b)
}

func quotaAwareHTTPClient(proxy, sessionHeaderKey string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		transport.Proxy = nil // 비워두기 = 직접 연결, 대체 HTTP_PROXY/HTTPS_PROXY 환경 변수 없음
	} else {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("llm: invalid proxy %q: %w", proxy, err)
		}
		switch proxyURL.Scheme {
		case "http", "https", "socks5":
		case "":
			return nil, fmt.Errorf("llm: proxy %q missing scheme (use http://, https:// or socks5://)", proxy)
		default:
			return nil, fmt.Errorf("llm: unsupported proxy scheme %q (use http, https or socks5)", proxyURL.Scheme)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: quotaAwareTransport{base: transport, sessionHeaderKey: strings.TrimSpace(sessionHeaderKey)}}, nil
}

// logTestConnection prints the raw HTTP status code(s) and response body of a
// 서버 로그에 대한 연결 테스트를 수행하므로 "클릭 테스트"는 진단 가능한 흔적을 남깁니다.
// exactly what the gateway returned — 401 bodies, quota text, empty frames — not
// just the collapsed ok/err the UI shows. Bodies are clipped to keep a chatty
// SSE stream from flooding the log.
func logTestConnection(c Config, capt *llmrec.Capture) {
	attempts := capt.Attempts()
	if len(attempts) == 0 {
		log.Printf("[llm-test] %s / %s @ %s — HTTP 요청이 이루어지지 않았습니다(구성 구문 분석 또는 연결 설정 실패)",
			c.Provider(), c.Model, c.BaseURL)
		return
	}
	for i, a := range attempts {
		log.Printf("[llm-test] %s / %s @ %s — %d/%d HTTP %d를 시도하세요\n응답 본문: %s",
			c.Provider(), c.Model, c.BaseURL, i+1, len(attempts), a.Status, clipBody(a.Body))
	}
}

// clipBody trims a wire body for logging. 4K is plenty to show an error JSON or
// the head of an SSE stream while bounding a runaway response.
func clipBody(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(비어 있음)"
	}
	const max = 4096
	if len(s) > max {
		return s[:max] + fmt.Sprintf("…(잘림, 총 %d바이트)", len(s))
	}
	return s
}

// TestConnection makes a minimal real completion to verify the provider/model/
// endpoint/key actually work. Returns the round-trip latency and the model's
// reply text.
func TestConnection(ctx context.Context, c Config) (time.Duration, string, error) {
	prov, err := c.NewProvider()
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// 원본 연결 메시지 캡처: 연결을 테스트할 때 가장 확인해야 할 것은 게이트웨이가 반환하는 것(상태 코드 + 응답 본문)입니다.
	// 이는 norma가 응답을 StreamEvent로 디코딩한 후에 사라집니다. quotaAwareTransport는
	// 컨텍스트에서 이 캡처를 찾아 각 HTTP 시도의 상태 코드와 본문을 입력하세요.
	ctx, capt := llmrec.NewCapture(ctx)
	defer logTestConnection(c, capt)
	// 연결 테스트는 싱글샷 경로이며 Agentcore의 세션 루프를 거치지 않으므로 컨텍스트에 매달리는 사람이 없습니다.
	// 세션 ID SessionHeaderKey로 구성된 엔드포인트(예: opencode zen)에 대해 필수입니다.
	// x-opencode-session 헤더, 400 MissingSessionID가 직접 누락됨), 이로 인해 "대화가 정상입니다.
	// 테스트하려면 클릭하여 400인치 드롭을 확인하세요. 테스트가 실제 대화와 일치하도록 여기에 일회성 무작위 세션 ID가 추가됩니다.
	// 헤더 로직 세트. SessionHeaderKey가 없는 엔드포인트는 읽지 않으며 부작용도 없습니다.
	ctx = transcript.WithSessionID(ctx, "conntest-"+transcript.NewSessionID())
	start := time.Now()
	//MaxTokens는 충분해야 합니다. 추론 모델(예: deepseek-v4-pro)은 답변을 제공하기 전에 큰 단락을 생성합니다.
	// 생각하기(실제 측정은 단 한 번의 "핑"으로 최대 2900개의 토큰을 태울 수 있음). 32만 주어지면 모델은 항상 '사고 단계'에 머물게 됩니다.
	// 출력 상한(마감=길이)에 도달하여 잘렸습니다. 연결 테스트는 통과했지만(err=nil) 다음과 같이 표시되었습니다.
	// "중단됨/길이/이력서"는 엉망입니다. OK(마침=중지)를 뱉어낼 만큼 충분한 예산을 투자하세요.
	// EscalateMaxTokens는 false로 유지됩니다. 빈 재개 루프를 피하기 위해 잘림으로 인해 재시도하지 마십시오.
	reply, err := agentcore.Run(ctx, agentcore.Options{
		Provider:       prov,
		SystemPrompt:   []string{"연결을 테스트하고 있습니다. 생각하거나 설명하거나 다른 어떤 것도 하지 않고 OK라는 두 문자를 출력하면 됩니다."},
		PermissionMode: acperm.ModeBypass,
		MaxTurns:       1,
		MaxTokens:      8192,
		NonStreaming:   !c.Stream, // 이 프로필의 실제 트랜시버 모드를 사용하여 연결 테스트를 수행하세요.
	}, "ping")
	lat := time.Since(start)
	if err != nil {
		return lat, "", err
	}
	// err==nil만으로는 충분하지 않습니다. 요청이 통과되었지만 모델이 한 마디도 내뱉지 않는 상황이 현실입니다(예산을 낭비하는 것을 생각해 보세요.
	// 보안 정책에 의해 본문이 삼켜지고, 호환성 계층에 의해 내용이 손실됩니다. 이 구성은 세션에서 "응답 없음"을 의미합니다.
	// 테스트는 성공적으로 보고되었습니다. 이는 이 프로젝트가 없애고자 하는 격차와 정확히 일치합니다. 표시되는 텍스트가 없으면 실패합니다.
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return lat, "", fmt.Errorf("모델에 응답 내용이 없습니다(요청이 통과되었지만 텍스트가 반환되지 않았습니다)")
	}
	return lat, reply, nil
}
