package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), "tavily"
// (TavilyKey required), or "deepseek" (DeepSeek* required, filled from the
// active LLM profile). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy — set it when the search endpoint
// is only reachable via a VPN/SOCKS proxy. Empty = direct.
//
// deepseek 백엔드의 특성은 다른 세 가지 백엔드와 다릅니다. DeepSeek에는 직접 호출 가능한 검색 인터페이스가 없습니다.
// 검색은 Anthropic 호환 메시지 인터페이스(web_search_20250305 서버) 내부에만 존재합니다.
// 도구), 따라서 각 검색은 하나의 모델 호출을 소비하고 검색 요청은 DeepSeek 서버에서 발행됩니다.
// 로컬 프록시를 통과하지 않으며 들어오는 트래픽에 흔적을 남기지 않습니다.
type WebSearchOpts struct {
	Enabled   bool
	Backend   string
	BraveKey  string
	TavilyKey string
	Proxy     string
	// 현재 활성 LLM 구성의 DeepSeek*(인위적 형식의 공식 DeepSeek 엔드포인트만 해당),
	// 별도로 구성되지 않으며, LLM 구성 전환으로 변경됩니다.
	DeepSeekBaseURL string
	DeepSeekAPIKey  string
	DeepSeekModel   string
}

type Worker struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	workDir         string
	proxyAddr       string
	proxyCACert     string            // recording proxy's CA cert path (for WebFetch HTTPS verify)
	webSearch       WebSearchOpts     // web_search tool backend selection (off by default)
	tx              *transcript.Store // raw LLM conversation persistence (nil = off)
	window          int               // context window in tokens (for compaction)
	windowFn        func() int        // optional dynamic task-chain minimum
	maxTurns        int               // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	nonStreamingFn func() bool
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	noaEnabledFn func() bool
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
	maxTokensFn func() int
}

// WorkerSessionID returns the stable transcript key used by a worker intent.
// Worker slots are reusable, so the intent id (rather than work#N) is the
// session identity. Keep this helper public so the Worker message API and UI
// can refer to exactly the conversation that will be resumed.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

const workerChatMarkerPrefix = "<!-- ARTEX_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker := workerChatMarker(requestID)
	for _, message := range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.nonStreamingFn = fn }

func (w *Worker) nonStreaming() bool { return w.nonStreamingFn != nil && w.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.maxTokensFn = fn }

func (w *Worker) maxTokens() int {
	if w.maxTokensFn == nil {
		return 0
	}
	return w.maxTokensFn()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "예산 소진으로 인해 종료가 곧 종료됩니다. 더 이상 명령/프로브를 실행하지 마십시오. 주문하십시오: (1) 위에서 식별했지만 하나씩 다시 쓰지 않은 콘텐츠를 다시 작성하십시오. 새 자산에는 insert_assets를 사용하고, 결론/사실을 탐색하려면 record_fact를 사용하고, 취약점을 확인하려면 report_finding을 사용하십시오. (2) **마지막으로 단일 문장의 일반 텍스트**를 사용하여 수행한 작업과 얻은 주요 결론을 요약합니다(이 문장은 이 실행의 결과로 표시되며 출력되어야 함)."

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		drop[n] = true
	}
	all := actool.DefaultTools()
	out := make([]actool.CoreTool, 0, len(all))
	for _, t := range all {
		if !drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	if w.windowFn != nil {
		return w.windowFn()
	}
	return w.window
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.proxyAddr, w.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// WorkerDefaultTmpl은 작업자 시스템의 내장된 편집 가능한 본문(섹션 [A])입니다.
// 프롬프트, Agent_prompts에 시드됨. TrafficTool 블록 및 중간 제품 출력 프로토콜
// are NOT here — they are code-owned and appended by workerSystem after rendering
// (섹션 [B]/[C])이므로 DB 본문을 편집해도 삭제될 수 없습니다.
const workerDefaultTmpl = `당신은 승인된 침투 테스트의 실행 에이전트입니다. 받은 의도(intent) 한 건을 수행하고, 새로 확인한 내용을 지식 그래프에 기록한 뒤 종료하세요.

**실행 경계**
1. 받은 의도에 해당하는 작업만 하세요. 다른 방향에서 조사할 만한 단서를 발견하면 fact의 summary에 한 문장으로 남겨 계획자에게 전달하세요. 직접 다른 의도를 수행하지 마세요.
2. 페이로드가 한 번 차단되거나, 404가 나오거나, 주입 결과가 보이지 않는다는 이유만으로 결론 내리지 마세요. 현재 의도 안에서 합리적인 우회 방법을 확인하세요.
3. 승인된 범위만 다루세요. 시스템 프롬프트 상단의 작업 제약이 최우선입니다. 명령이나 탐지를 실행하기 전에 제약 위반 여부를 확인하고, 받은 의도와 충돌하더라도 제약을 지키세요.

**결과는 확인하는 즉시 기록하세요.** 마지막까지 미루면 실행 한도에 도달했을 때 결과를 잃을 수 있습니다. 새로 얻은 정보만 기록하고 기존 내용을 표현만 바꿔 중복 등록하지 마세요.
- 새 자산 자체(하위 도메인, 서비스, 엔드포인트, 기술 지문, 자격 증명 등)는 insert_assets로 자산 그래프에 등록합니다. 탐색 결과에 대한 판단은 record_fact에 남깁니다.
- 탐색의 사실과 결론은 record_fact로 등록하고 intent_id를 전달합니다. 한 번의 조사에서 나온 여러 관찰은 보통 summary 한 문장과 자세한 detail을 가진 사실 한 건으로 묶으세요. 서로 독립적이고 합칠 수 없는 결론일 때만 facts 배열로 나누세요. 실제로 확인한 내용만 기록하고 evidence에는 명령과 핵심 출력 한두 줄을 간결하게 담으세요. confidence는 직접 관찰했으면 observed, 현상에서 추론했으면 inferred입니다.
- 확인된 취약점만 report_finding으로 등록하고 intent_id와 재현 가능한 PoC를 제공하세요. 이번 실행에서 실제로 취약점을 재현하고 요청·응답 또는 명령 출력 등의 증거를 얻어야 합니다. 버전·지문과 CVE가 일치하거나 매개변수가 주입 가능해 보인다는 이유만으로 확정하지 마세요. 외부 취약점 자료나 코드 차이로 추정한 내용도 실제 재현을 대신할 수 없습니다. 의심스럽지만 재현하지 못했다면 record_fact에 inferred 사실로 남기고 이유를 설명하세요.

의도를 마치면 수행한 작업과 기록한 사실을 한 문장으로 요약하세요.`

// WorkerTrafficBlock은 세그먼트 [B]입니다. 트래픽 도구 참고, 다음 경우에만 코드가 삽입됩니다.
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if !recording {
		return ""
	}
	return "\n\n**트래픽 도구**:\n- traffic_search / traffic_get / traffic_blob로 이미 방문한 URL의 요청과 응답을 확인하세요. **먼저 기록된 트래픽을 살펴보고 같은 URL에 불필요하게 다시 요청하지 마세요.** traffic_search에는 host가 필요하며 기본적으로 내용이 없는 인덱스 3건(id/method/url/status/resp_len)을 반환합니다. 더 필요하면 limit을 지정하세요. body_contains는 요청·응답 본문에서 3자 이상인 부분 문자열을 검색할 수 있습니다. 원문은 traffic_get(id)으로 읽으세요. 큰 본문이 @blob sha256:<hash>로 표시되면 traffic_blob(hash)로 나누어 읽으세요."
}

// ArtifactSpec은 섹션 [C]입니다. 모든 항목에 추가된 코드 소유의 편집 불가능한 꼬리입니다.
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**중간 산출물 저장 규칙**: 스크립트, 페이로드, 수집한 응답 본문, 임시 데이터는 모두 이 작업의 디렉터리 **" + dir + "**에 저장하세요. 상대 경로로 쓰거나 이 절대 경로를 사용하세요. **/tmp나 다른 절대 경로에는 쓰지 마세요.**"
}

// WorkerArtifactSpec은 작업자 섹션 [C]입니다. 인텐트별 실행 디렉터리가 미리 생성되어 있습니다.
// by the engine (ensureRunDir), so it just writes relative paths there — no manual
// mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**중간 산출물 저장 규칙**: 스크립트, 페이로드, 수집한 응답 본문, 임시 데이터는 모두 이번 의도의 전용 작업 디렉터리 **" + runDir + "**에 저장하세요. 디렉터리는 이미 만들어져 있으므로 상대 경로로 바로 쓰면 됩니다. **/tmp나 다른 절대 경로에는 쓰지 마세요.**"
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

func workerSystem(proxyAddr, caCert, dataDir, runDir string) string {
	body := renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr, DataDir: dataDir, Now: nowStr()})
	// caCert is present only when the recording MITM is on, which is exactly when
	// the traffic_* tools are registered — so it gates the traffic-tool note.
	// Optional finding guidance is added for every role after tool resolution.
	return body + workerTrafficBlock(caCert != "") + workerArtifactSpec(runDir)
}

// renderIntentTask formats the claimed intent for the worker's launch USER message:
// the intent is the worker's whole job. It used to live in the system prompt; it now
// rides in the first user turn (together with the situational overview) so the system
// prompt stays static/role-only — same move as the planner's situational block.
// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

func renderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n[받은 의도(이번에 유일한 작업: 이것만 하고, 사실만 생산하고, 완료되면 중지)]:\n%s\n인텐트 ID: %d(record_fact/report_finding을 다시 쓸 때 전달)", string(intent.Payload), intent.ID)
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	//Coverage는 기획자가 "어떤 측정 유형이 적은지 / 범위를 확장할지"를 판단하는 신호이고 작업자는 "받은 것만 수행합니다"
	// "밝혀지지 않은 점을 쫓지 말라"는 의도는 책임 경계를 위반합니다. → 작업자 보기에서 제거됩니다. 데이터는 이 작업자입니다
	// 독점적인 새 지도, 키 삭제는 플래너에 영향을 미치지 않습니다.
	delete(data, "coverage")
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n[글로벌 탐색 상황(읽기 전용, 전체 상황에 의도를 넣는 데 도움이 됨)]:\n" +
		"다음은 전체 임무의 현재 탐사에 대한 개요입니다. 여기에는 두 가지 용도가 있습니다. 첫째, 다른 사람들이 발견한 내용을 알고 이를 반복하지 않는 것입니다. 둘째, 자신의 의도를 탐구할 때 그것이 전체 상황과의 관계를 생각해 볼 수 있다.\n" +
		"**다양성은 좋은 것입니다**: 원래 의도를 탐색할 때 깊이 생각하고 깊이 생각해 보세요. 유일한 한계는 - 실제로 다른 의도를 실행하기 위한 조치를 취하지 않는다는 것입니다(이것은 기획자가 계획한 다른 작업자의 업무입니다). 귀중한 단서(자산 간 연결, 다른 활용 체인으로의 의심되는 진입, 글로벌 수준의 의심스러운 지점)가 떠오를 때마다 사실을 작성하여 계획자에게 제출해야 합니다. 이는 필수가 아닌 중요한 결과물입니다. 스스로 삼키는 것보다 한 가지라도 더 보고하고 기획자에게 판단을 맡기는 것이 좋다.\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	tsx := NewToolSet(ts, name)
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	coverageEnabled := as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID)         // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr)                  // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotifyFinding(notifyFinding) // Report_finding은 라이브러리가 떨어졌을 때 그 자리에서 플래너를 깨우고 "어떤 의도 + 발견"을 가져옵니다.
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base := append(tsx.WorkerTools(), w.extraTools...)
	// 작업자는 의도적으로 MultiEdit/Glob/Grep을 제공하지 않습니다. Bash(grep/find) 대신 Edit를 사용하고 파일을 검색합니다.
	// 도구 표면을 통합하고 가치가 낮은 호출을 줄입니다. 나머지 SDK 기본 도구(읽기/쓰기/편집/LS/Bash/Sleep)는 평소와 같습니다.
	base = append(base, defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID})
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	defer cleanup()

	// 의도는 작업자의 [유일한 책임, 실행 전반에 걸쳐 불변] → 시작 명령 및 의도에 의해 고정된 대상 자산과 함께
	// 원래 데이터를 시스템 프롬프트에 함께 넣으십시오. 시스템은 실행될 때마다 다시 철자가 표시되며 압축으로 인해 표시되지 않습니다.
	// 의도는 장기 실행 중에 항상 존재하며 실행을 계속할 때 기록 기록에 첫 번째 메시지가 유지되는지 여부에 의존하지 않습니다. 가격은 시스템이다
	// 의도별 휘발성 데이터가 혼합되어 의도 간 캐시 재사용이 손실됩니다. 이는 고의적인 절충입니다(의도 상실은 토큰을 저장하는 것보다 훨씬 더 심각합니다).
	// 플래너의 "자세 블록 및 사용자 회전"이 포함된 포크는 의도적입니다. 플래너 자체가 의도를 생성하는 것이며 단일 명령은 없습니다.
	// 노동자들은 있습니다. 시작 사용자 메시지에는 [글로벌 상황 개요]만 남습니다. 이는 다운그레이드될 수 있고, 오래된 것을 허용하며, 문제 없이 억제될 수 있습니다.
	//이 의도에 대한 단독 작업 디렉터리인 <workDir>/tasks/<taskID>/i<intentID>가 엔진 측에서 먼저 생성됩니다.
	runDir := ensureRunDir(w.workDir, taskID, intent.ID)
	// The run-wide intent is not the current tool action. Do not forward it or
	// inherit a parent run's background into the action reviewer.
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	overview := renderWorkerGraphOverview(tsx.graphOverviewData())
	sysBody := workerSystem(w.proxyAddr, w.proxyCACert, w.workDir, runDir)
	if w.wantConstraints() {
		sysBody += constraintBlock(ts) // 작업 제약 조건(있는 경우)이 시스템 프롬프트에 삽입되고 작업자는 실행 중에 해당 제약 조건을 엄격히 준수합니다.
	}
	// 의도 블록 → 의도 고정 자산 블록 → 시작 지침이 시스템 끝에 순서대로 추가됩니다(constraintBlock과 동일한 추가 방법).
	sysBody += renderIntentTask(intent)
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
				if b, err := json.Marshal(assets); err == nil {
					sysBody += "\n\n이 인텐션의 asset_ids에 해당하는 대상 자산:\n" + string(b)
				}
				// 인텐트에 의해 명시적으로 타겟팅된 이러한 자산은 → 작업 테스트 범위에 자동으로 포함됩니다(insertAssets와 동일하게 설정됨).
				// 보수적인 세분성). upsertTaskScope의 ON CONFLICT 아무것도 하지 않음 + uq_task_scope
				// 고유 인덱스는 반복적으로 추가되지 않도록 보장됩니다. 재실행/재시도 역시 멱등성 no-op입니다.
				// 자산 커버리지 기능이 꺼지면 테스트 커버리지(분모)가 더 이상 누적되지 않습니다.
				if coverageEnabled {
					for _, a := range assets {
						_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
					}
				}
			}
		}
	}
	sysBody += "\n\n위의 의도를 실행하기 시작하세요. 그냥 하세요. 사실, 자산, 발견만 생성하고 완료되면 중지하세요."
	system, boundary := deferredSystem(sysBody, def)
	// 작업 수준 기한(ctx를 통해 주입됨)은 이 실행의 벽시계 예산을 고정하고 종료 단어를 결정합니다(taskclock.go 참조).
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	settle := wrapupSettlement("worker", []string{"Bash"})
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", []string{"Bash"}, clamped)
	}
	opts := agentcore.Options{
		Provider:        w.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		// WebFetch는 녹음 프록시를 사용하며 해당 HTTP는 컬과 동일하게 추적됩니다. 프록시 CA를 로드하면 MITM을 통과할 수 있습니다.
		// 다시 서명된 HTTPS 인증서는 [일반 확인을 통과]할 수 있습니다(확인을 끄는 대신). 프록시가 비어 있으면 직접 연결됩니다.
		EnableWebFetch: true,
		WebFetchProxy:  w.proxyAddr,
		WebFetchCACert: w.proxyCACert,
		// 인터넷 검색(선택사항). ddgs에는 키가 필요하지 않습니다. Brave-Free에는 BraveKey가 필요합니다. TavilyKey가 필요합니다.
		// WebSearchProxy는 독립적인 내보내기 프록시(http/https/socks5)이며 트래픽을 기록하는 MITM 프록시와 아무 관련이 없습니다. 비어 있으면 직접 연결됩니다.
		EnableWebSearch:       w.webSearch.Enabled,
		WebSearchBackend:      w.webSearch.Backend,
		BraveSearchAPIKey:     w.webSearch.BraveKey,
		TavilySearchAPIKey:    w.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: w.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  w.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   w.webSearch.DeepSeekModel,
		WebSearchProxy:        w.webSearch.Proxy,
		// Bash 하위 명령의 HTTP 기본값은 로깅 프록시로 이동하고 해당 CA를 신뢰합니다(도구에는 -x/-k가 필요하지 않음).
		BashEnv:    proxyEnv(w.proxyAddr, w.proxyCACert),
		WorkingDir: runDir,
		MaxTurns:   w.maxTurns, // 0 = unlimited (configurable in agent management)
		//벽시계 예산, 경계 판단, 중단 없음; 0 = 제한 없음. 작업 수준 마감일이 있는 경우 최소(자체 예산,
		// 마감일 전에 남은 경우) 작업이 끝에 도달하면 이 실행이 자연스럽게 끝나도록 하세요(taskclock.go 참조).
		MaxDuration: maxDur,
		// 적중 예산(라운드 또는 기간) → SDK는 마감 라운드를 실행하고(Bash 숨기기) 완료되지 않은 작업을 피하기 위해 식별된 항목을 다시 작성합니다.
		// PromptByReason은 고정될 때 사용됩니다(작업 기한에 의해 고정됨): 시간 초과 = 작업 도착 → 작업 시간 초과 단어이므로,
		// 단계 수 = 클램핑 창 내의 단계 수가 먼저 소진되므로 실행 단어당 폴백됩니다. 비클램핑은 순수한 실행당 유지를 유지합니다.
		Settlement: settle,
		// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
		// 전체 출력이 디스크에 보존됩니다. 잘림의 상한은 SDK 기본값(30000자)입니다.
		ToolOutputDir: cmdOutDir(runDir),
		Compaction:    compactionConfig(w.compactionWindow()), // long tool-heavy runs stay within the window
		Todos:         actool.NewTodoStore(),                  // 순전히 계획 목적으로 사용되는 세션 수준 임시 작업(TodoWrite)은 종료 시 손실됩니다.
		NonStreaming:  w.nonStreaming(),                       // Provider.Complete는 프로필이 비스트리밍일 때 사용됩니다.
		MaxTokens:     w.maxTokens(),                          // 0 = 서버의 기본값에 의해 결정된 상한값을 보내지 않음
	}
	if hooks != nil { // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.tx != nil { // persist raw LLM conversation; one file per worked intent
		opts.Transcript = w.tx
		opts.SessionID = WorkerSessionID(ts.ID(), intent.ID)
	}
	intentID := intent.ID
	emitWrap := func(r db.Activity) {
		if emit != nil {
			r.NodeID, r.Worker = &intentID, name
			emit(r)
		}
	}
	// 의도/시작 명령/의도 고정 자산이 시스템 프롬프트와 함께 전달되었습니다(위의 sysBody 어셈블리 참조).
	// 이 시작 사용자 메시지는 [글로벌 상황 개요]만 전달합니다. 전체 정보를 이해하기 위해 다운그레이드할 수 있으며 문제 없이 표시되지 않습니다.
	// 드문 경우지만 개요 마샬이 비어 있지 않으면 첫 번째 라운드에서 빈 사용자 메시지를 피하기 위해 시작 단어가 롤백됩니다.
	input := overview
	if strings.TrimSpace(input) == "" {
		input = "시스템에 수신된 의도를 실행하기 시작하세요. 그냥 실행하고 사실, 자산 및 결과만 생성하고 완료되면 중지하세요."
	}

	// 실험 기능: 켜진 후 noa가 컨텍스트 압축을 대신합니다(아카이브는 <workDir>/noa/<SessionID>에 집중되어 있으며 지속됩니다).
	noaSession := WorkerSessionID(ts.ID(), intent.ID)
	enableNoa(&opts, w.noaEnabledFn, w.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Resume prior conversation if this intent was paused/blocked/exhausted and is
	// being re-run. The transcript ID is deterministic per intent, so if a prior
	// session exists the worker continues from where it left off instead of
	// restarting from scratch.
	alreadyRecorded := false
	if w.tx != nil {
		_ = s.Resume(opts.SessionID)
		alreadyRecorded = requestID != "" && hasWorkerChatMessage(s.Messages(), requestID)
		if len(s.Messages()) > 0 && message == "" {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
			input = "계속 실행합니다."
		} else if len(s.Messages()) > 0 {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
		}
	}
	if message != "" {
		if alreadyRecorded {
			input = "마지막 대화에서 입력한 새 인텐트를 계속 실행합니다. 이미 한 행동을 반복하지 마세요."
		} else if len(s.Messages()) > 0 {
			input = workerChatMarker(requestID) + "\n[인위적인 대화입력의 새로운 의도]\n" + message +
				"\n\n이 수동 입력을 즉시 실행하십시오. 완료 후 상황에 따라 원래 작업을 계속해야 하는지 여부를 결정할 수 있습니다."
		} else {
			input += "\n\n" + workerChatMarker(requestID) + "\n[인위적인 대화입력의 새로운 의도]\n" + message +
				"\n\n이 수동 입력을 우선으로 해주세요."
		}
	}

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
	// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
	// still settles (no external hard-timeout backstop needed). ctx itself carries only
	// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.
	_, reason, err := captureRunSession(ctx, s, input, emitWrap)
	return reason, tsx.Writes(), err
}
