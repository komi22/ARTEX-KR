package agent

import (
	"context"
	"fmt"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// MainAgent is the thin human-interface orchestrator (docs §4.2 / §7). The human
// chats with it; it observes (read tools), and steers by injecting hints
// (→planner) or direct high-priority intents (→frontier). It does NOT run the
// autonomous intent-generation loop (that is the planner's job).
type MainAgent struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	tx              *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window          int                                    // context window in tokens (for compaction)
	windowFn        func() int                             // optional dynamic task-chain minimum
	maxTurns        int                                    // max agent turns per run (0 = unlimited)
	proxyAddr       string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert     string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch       WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir         string                                 // shared work dir (surfaced in prompt as artifact-output target)
	steerWork       func(intentID int64, msg string) error // engine callback: steer a running work (nil = off)
	nonStreamingFn  func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn    func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn     func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
}

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (m *MainAgent) SetNoaEnabled(fn func() bool) { m.noaEnabledFn = fn }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (m *MainAgent) SetNonStreaming(fn func() bool) { m.nonStreamingFn = fn }

func (m *MainAgent) nonStreaming() bool { return m.nonStreamingFn != nil && m.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (m *MainAgent) SetMaxTokens(fn func() int) { m.maxTokensFn = fn }

func (m *MainAgent) maxTokens() int {
	if m.maxTokensFn == nil {
		return 0
	}
	return m.maxTokensFn()
}

func NewMainAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *MainAgent {
	return &MainAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns}
}

func (m *MainAgent) SetCompactionWindowResolver(fn func() int) { m.windowFn = fn }

func (m *MainAgent) compactionWindow() int {
	if m.windowFn != nil {
		return m.windowFn()
	}
	return m.window
}

// SetProxy points the main agent's WebFetch at the recording proxy plus the CA
// cert it trusts to verify HTTPS through it (empty addr = direct).
func (m *MainAgent) SetProxy(addr, caCert string) { m.proxyAddr, m.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the main agent (off by default).
func (m *MainAgent) SetWebSearch(o WebSearchOpts) { m.webSearch = o }

// SetSteerWork wires the engine callback that lets the main agent's steer_work
// tool inject a mid-run course-correction into a running work (nil = tool off).
func (m *MainAgent) SetSteerWork(fn func(intentID int64, msg string) error) { m.steerWork = fn }

// mainAgentDefaultTmpl은 기본 에이전트의 내장된 편집 가능한 본문(섹션 [A])입니다.
// 프롬프트, Agent_prompts에 시드됨. 목표는 {{.Goal}} 템플릿 변수입니다. 중간
// 아티팩트 출력 사양 테일은 코드 소유(artifactSpec)이며 렌더링 후에 추가됩니다.
const mainAgentDefaultTmpl = `당신은 승인된 침투 테스트 작업의 메인 에이전트이며 사용자와 탐색 시스템을 연결합니다. 직접 탐색하거나 지속적으로 새 의도를 만들지 마세요. 지속적인 계획은 플래너의 역할입니다.

1. 현황 파악: graph_overview, list_findings, list_facts, list_assets, get_worker_output으로 실제 진행 상황을 확인하고 사용자의 질문에 답하세요.
2. 사용자 요청 반영:
- 조사 방향, 취약점 유형, 집중 영역을 바꾸고 싶다면 add_hint로 플래너에게 힌트를 전달하세요.
- 특정 대상을 즉시 검증하고 싶다면 add_intent로 우선순위 8~10의 의도를 추가하세요. 시스템이 필요한 경우 완료된 작업을 다시 실행 상태로 바꾸고 Worker가 해당 의도를 수행합니다. 수행 후에는 다시 완료 상태가 됩니다.
- 모든 작업 목표가 이미 달성됐다면, 새 의도가 추가로 달성해야 할 결과를 뜻하는지 확인하세요. 새 목표가 암시됐지만 명확하지 않다면 예상하는 목표를 한 문장으로 되짚고 공식 목표로 등록할지 사용자에게 물으세요. 원하면 set_goals로 등록해 정규 계획을 다시 시작합니다. 일회성 검증만 원하거나 새 목표가 아니라면 add_intent만 사용하세요. 명백한 일회성 검증에는 매번 확인하지 않아도 됩니다.
- 실행 중인 특정 Worker의 진행 방향을 바로잡으려면 steer_work를 사용하세요. 방향 자체가 잘못됐다면 add_intent로 새 의도를 만드세요.
- 최종 목표를 추가하려면 set_goals를 사용하세요. 목표가 작업 그래프에 기록되고 완료·일시 중지된 작업도 자동으로 다시 시작됩니다.
- 허용하거나 금지할 작업을 바꾸려면 set_constraints에 type=allow 또는 type=deny로 등록하세요. 이후 계획자와 Worker에게 제약이 전달됩니다.

현재 작업 목표: {{.Goal}}

한국어로 간결하게 답하고, 수행한 일과 확인된 결과를 설명하세요. 도구가 반환한 실제 데이터만 사용하고 결과를 지어내지 마세요.`

func mainAgentSystem(goal, dataDir, workDir string) string {
	body := renderSystem("mainagent", mainAgentDefaultTmpl, MainVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Chat handles one human message and returns the assistant reply. emit, if
// non-nil, receives each execution step (thinking / tool_use / tool_result /
// text / result) so the main-agent session shows its work — exactly like the
// worker/planner sessions — not just the final answer.
func (m *MainAgent) Chat(ctx context.Context, taskID int64, mainSeg int, as *db.AssetStore, ts *db.ExplorationStore, goal, message string, emit func(db.Activity), notify, resume func(), notifyGoal, notifyHint func([]string)) (string, error) {
	tsx := NewToolSet(ts, "human")
	tsx.SetFindingRecorder(m.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.SetNotify(notify)         // 일반 깨우기(전용 콜백 없이 쓰기 작업이 수행됨, 디바운싱됨)
	tsx.SetResumeTask(resume)     // set_goals는 새로운 목표를 추가하고 → 완료/일시 중지된 작업을 다시 실행하게 합니다.
	tsx.SetNotifyGoal(notifyGoal) // set_goals 새 목표 추가 → 플래너에 "사람들이 새 목표를 추가했습니다:..." 트리거에 메모를 작성합니다.
	tsx.SetNotifyHint(notifyHint) //add_hint 새 힌트 추가 → "사람들이 N개의 전략적 힌트를 추가했습니다:..." 트리거를 플래너에 기록합니다.
	tsx.steerWork = m.steerWork   // enable steer_work tool (nil = unavailable)
	// 도메인 도구 + 기본 기본 도구 세트(읽기/쓰기/편집/MultiEdit/LS/Glob/Grep/Bash)
	// 자산 적용 범위 기능이 꺼진 경우 add_task_scope/list_untested_assets(프롬프트 입력 없이)을 제거하세요.
	base := append(tsx.DropCoverageTools(tsx.MainAgentTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "mainagent", base)
	defer cleanup()
	// 이 작업의 작업 디렉터리인 <workDir>/tasks/<taskID>가 먼저 생성됩니다.
	mainDir := ensureRunDir(m.workDir, taskID, 0)
	ctx = intercept.WithReviewWorkingDirectory(ctx, mainDir)
	system, boundary := deferredSystem(mainAgentSystem(goal, m.workDir, mainDir), def)
	opts := agentcore.Options{
		Provider:        m.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, //프록시 추적을 기록합니다. MITM이 다시 서명한 HTTPS 인증서를 확인하기 위해 프록시 CA를 로드합니다.
		WebFetchProxy:   m.proxyAddr,
		WebFetchCACert:  m.proxyCACert,
		// 인터넷 검색(선택사항). ddgs에는 키가 필요하지 않습니다. Brave-Free에는 BraveKey가 필요합니다. TavilyKey가 필요합니다.
		// WebSearchProxy는 독립적인 내보내기 프록시(http/https/socks5)이며 트래픽을 기록하는 MITM 프록시와 아무 관련이 없습니다. 비어 있으면 직접 연결됩니다.
		EnableWebSearch:       m.webSearch.Enabled,
		WebSearchBackend:      m.webSearch.Backend,
		BraveSearchAPIKey:     m.webSearch.BraveKey,
		TavilySearchAPIKey:    m.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: m.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  m.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   m.webSearch.DeepSeekModel,
		WebSearchProxy:        m.webSearch.Proxy,
		BashEnv:               proxyEnv(m.proxyAddr, m.proxyCACert), // Bash 하위 명령의 기본값은 프록시 + 신뢰할 수 있는 CA입니다.
		WorkingDir:            mainDir,                              // 이 작업의 작업 디렉터리 <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(mainDir),
		MaxTurns:              m.maxTurns,                             // 0 = unlimited (configurable in agent management)
		Compaction:            compactionConfig(m.compactionWindow()), // long chats stay within the window
		Todos:                 actool.NewTodoStore(),                  // 순전히 계획 목적으로 사용되는 세션 수준 임시 작업(TodoWrite)은 종료 시 손실됩니다.
		// 적중 예산(단계 수) → SDK 실행 종료: 사용자에게 진행 요약을 출력합니다. 프롬프트 및 마감 라운드 수는 백그라운드에서 편집할 수 있습니다(기본값은 10라운드).
		Settlement:   wrapupSettlement("mainagent", nil),
		NonStreaming: m.nonStreaming(), // Provider.Complete는 프로필이 비스트리밍일 때 사용됩니다.
		MaxTokens:    m.maxTokens(),    // 0 = 서버의 기본값에 의해 결정된 상한값을 보내지 않음
	}
	if m.tx != nil { // persist raw human↔AI conversation; one accumulating file per segment
		opts.Transcript = m.tx
		// Segment 0 keeps the legacy "exp%d-main" name so existing transcripts still
		// load; each new session (seg>=1) gets its own file for a clean context.
		opts.SessionID = fmt.Sprintf("exp%d-main", ts.ID())
		if mainSeg > 0 {
			opts.SessionID = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
		}
	}
	// 실험 기능: 켜진 후 noa가 컨텍스트 압축을 대신합니다(아카이브는 <workDir>/noa/<SessionID>에 집중되어 있으며 지속됩니다).
	// 세션 ID와 기록에는 동일한 규칙(세그먼트 인식)이 있으므로 보관 및 복원이 정렬됩니다.
	noaSession := fmt.Sprintf("exp%d-main", ts.ID())
	if mainSeg > 0 {
		noaSession = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
	}
	enableNoa(&opts, m.noaEnabledFn, m.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload the prior conversation from the transcript so the agent has context
	// across turns (each Chat is a fresh session; without this it can't see earlier
	// messages). First turn: no file yet → Resume loads nothing and proceeds.
	if m.tx != nil {
		_ = s.Resume(opts.SessionID)
	}
	// C2: this session is fresh each turn; re-unlock skill-gated MCPs from prior
	// Skill() calls in the reloaded history so revealed tools stay callable.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = "mainagent"
			emit(r)
		}
	})
	return text, err
}
