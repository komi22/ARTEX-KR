package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
)

// DTO/serialization layer: each handler emits EXACTLY the frontend's spec shapes
// (artex/web/src/lib/types.ts). These reshape db package structs so the
// internal DB model never leaks over the API. The db structs and the frontend are
// the canonical contracts; this file maps one onto the other.

func i64s(v int64) string { return strconv.FormatInt(v, 10) }

func rfc3339(t time.Time) string { return t.Format(time.RFC3339) }

// rawString stringifies a json.RawMessage, returning "" for empty/nil so omitempty
// fields drop out.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

// ---- Task (frontend "Task") ---- created_at as RFC3339, plus a derived status.
type TaskDTO struct {
	ID                 string             `json:"id"`
	ExplorationID      int64              `json:"exploration_id"`
	Name               string             `json:"name"` // 선택적 작업 이름. 비어 있음 = 이름 없음
	CategoryID         *int64             `json:"category_id,omitempty"`
	CategoryName       string             `json:"category_name,omitempty"`
	Pinned             bool               `json:"pinned"`
	PinnedAt           string             `json:"pinned_at,omitempty"`
	Description        string             `json:"description"`
	Goal               string             `json:"goal"`
	Status             string             `json:"status"` // created | running | paused | done | failed
	CreatedAt          string             `json:"created_at"`
	CreatedUnix        int64              `json:"created_unix"`       // created_at as unix seconds (for run-duration calc)
	CompletedAt        string             `json:"completed_at"`       // RFC3339 finish time (done/failed); "" if unfinished
	CompletedUnix      int64              `json:"completed_unix"`     // completed_at as unix seconds (0 if unfinished)
	LastActivity       int64              `json:"last_activity_unix"` // unix seconds of the last activity (0 if none)
	Paused             bool               `json:"paused"`
	Queued             bool               `json:"queued"`
	Tokens             TokenTotalDTO      `json:"tokens"` // whole-task token consumption
	GoalsTotal         int                `json:"goals_total"`
	GoalsMet           int                `json:"goals_met"`
	InFlight           int                `json:"in_flight"`                //실행 중인 작업자 수(상태 의도=실행 중)
	Findings           FindingSeverityDTO `json:"findings"`                 // 이 작업에 등록된 취약점 수(심각도별로 분류된 발견 항목 테이블)
	LLMProfileID       *int64             `json:"llm_profile_id,omitempty"` // LLM profile used for this task; nil = default
	LLMProfileIDs      []int64            `json:"llm_profile_ids"`
	ActiveLLMProfileID *int64             `json:"active_llm_profile_id,omitempty"`
	LLMFailoverState   string             `json:"llm_failover_state"`
	LLMFailoverReason  string             `json:"llm_failover_reason,omitempty"`
	SourceTaskIDs      []string           `json:"source_task_ids"`
	ArchiveBlockedBy   string             `json:"archive_blocked_by_task_id,omitempty"`
	CompanyIDs         []int64            `json:"company_ids"`
	CoverageEnabled    bool               `json:"coverage_enabled"` // 자산보상 기능 스위치(생성 시 설정)
}

// FindingSeverityDTO는 심각도(심각함/높음/중간/낮음)별로 분류된 작업 목록의 취약점 수입니다.
type FindingSeverityDTO struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
}

func applyTaskArchiveBlocker(dto *TaskDTO, blockers map[int64]int64) {
	if dto == nil || len(blockers) == 0 {
		return
	}
	taskID, err := strconv.ParseInt(dto.ID, 10, 64)
	if err != nil {
		return
	}
	if dependentID := blockers[taskID]; dependentID > 0 {
		dto.ArchiveBlockedBy = strconv.FormatInt(dependentID, 10)
	}
}

// TokenTotalDTO is a whole-task (all agents) token aggregate.
type TokenTotalDTO struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

func tokenTotalDTO(u db.TokenUsage) TokenTotalDTO {
	return TokenTotalDTO{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}

func taskDTO(t *Task, status string) TaskDTO {
	lifecycle := t.lifecycleSnapshot()
	llmState := t.llmStateSnapshot()
	sourceIDs := make([]string, 0, len(lifecycle.SourceTaskIDs))
	for _, id := range lifecycle.SourceTaskIDs {
		sourceIDs = append(sourceIDs, i64s(id))
	}
	profileIDs := append(make([]int64, 0, len(llmState.ProfileIDs)), llmState.ProfileIDs...)
	return TaskDTO{
		ID:                 t.ID,
		ExplorationID:      t.ExpID,
		Name:               lifecycle.Name,
		CategoryID:         lifecycle.CategoryID,
		CategoryName:       lifecycle.CategoryName,
		Pinned:             lifecycle.PinnedAt > 0,
		PinnedAt:           completedRFC(lifecycle.PinnedAt),
		Description:        t.Description,
		Goal:               t.Goal,
		Status:             status,
		CreatedAt:          rfc3339(time.Unix(t.CreatedAt, 0)),
		CreatedUnix:        t.CreatedAt,
		CompletedAt:        completedRFC(lifecycle.CompletedAt),
		CompletedUnix:      lifecycle.CompletedAt,
		Paused:             lifecycle.Paused,
		Queued:             lifecycle.Queued,
		LLMProfileID:       llmState.ProfileID,
		LLMProfileIDs:      profileIDs,
		ActiveLLMProfileID: llmState.ActiveID,
		LLMFailoverState:   llmState.FailoverState,
		LLMFailoverReason:  llmState.FailoverReason,
		SourceTaskIDs:      sourceIDs,
		CompanyIDs:         lifecycle.CompanyIDs,
		CoverageEnabled:    t.CoverageEnabled,
	}
}

// completedRFC renders a unix completion time as RFC3339, or "" when unset (0).
func completedRFC(unix int64) string {
	if unix == 0 {
		return ""
	}
	return rfc3339(time.Unix(unix, 0))
}

// ---- TrafficExchange (frontend "TrafficExchange") ---- ts as RFC3339.
type TrafficExchangeDTO struct {
	ID          string `json:"id"`
	TS          string `json:"ts"`
	Host        string `json:"host"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	RespLen     int    `json:"resp_len"`
}

func trafficDTOs(ex []traffic.ExchangeMeta) []TrafficExchangeDTO {
	out := make([]TrafficExchangeDTO, 0, len(ex))
	for _, e := range ex {
		out = append(out, TrafficExchangeDTO{
			ID:          e.ID,
			TS:          rfc3339(time.Unix(e.TS, 0)),
			Host:        e.Host,
			Method:      e.Method,
			URL:         e.URL,
			Status:      e.Status,
			ContentType: e.ContentType,
			RespLen:     e.RespLen,
		})
	}
	return out
}

// ---- TaskNode (frontend "TaskNode") — frontier, intents, exploration graph nodes ----

type TaskNodeDTO struct {
	ID           string `json:"id"`
	Type         string `json:"type"` // db Node.Kind
	Payload      string `json:"payload,omitempty"`
	Priority     int    `json:"priority"`
	State        string `json:"state"`
	Origin       string `json:"origin"`
	TS           string `json:"ts"`
	SourceTaskID string `json:"source_task_id,omitempty"`
	Inherited    bool   `json:"inherited,omitempty"`
	DeleteReason string `json:"delete_reason,omitempty"` // 허위삭제 의도시 삭제사유(상태='삭제됨')
}

func taskNodeDTO(n *db.Node) TaskNodeDTO {
	d := TaskNodeDTO{
		ID:           i64s(n.ID),
		Type:         n.Kind,
		Payload:      rawString(n.Payload),
		DeleteReason: n.DeleteReason,
		Priority:     n.Priority,
		State:        n.State,
		Origin:       n.Origin,
		TS:           rfc3339(n.CreatedAt),
	}
	if n.SourceTaskID > 0 {
		d.SourceTaskID = i64s(n.SourceTaskID)
	}
	d.Inherited = n.Inherited
	return d
}

// GoalDTO is a goal node with its payload unpacked into text/vulnclass — the shape
// 개요 "대상 관리" UI는 (원시 페이로드 JSON을 전달하는 TaskNodeDTO와 비교) 작동합니다.
type GoalDTO struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
	State     string `json:"state"`
	Origin    string `json:"origin,omitempty"`
	TS        string `json:"ts"`
}

func goalDTO(n *db.Node) GoalDTO {
	var p struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return GoalDTO{
		ID:        i64s(n.ID),
		Text:      p.Text,
		VulnClass: p.VulnClass,
		State:     n.State,
		Origin:    n.Origin,
		TS:        rfc3339(n.CreatedAt),
	}
}

func goalDTOs(in []*db.Node) []GoalDTO {
	out := make([]GoalDTO, 0, len(in))
	for _, n := range in {
		out = append(out, goalDTO(n))
	}
	return out
}

// ConstraintDTO는 개요 제약 관리 UI에 대한 하나의 작업 제약 조건(허용/거부)입니다.
type ConstraintDTO struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // allow | deny
	Text   string `json:"text"`
	Origin string `json:"origin,omitempty"`
	TS     string `json:"ts,omitempty"`
}

func taskNodeDTOs(in []*db.Node) []TaskNodeDTO {
	out := make([]TaskNodeDTO, 0, len(in))
	for _, n := range in {
		out = append(out, taskNodeDTO(n))
	}
	return out
}

// ---- Edge (frontend "Edge") — exploration edges and asset edges ----

type EdgeDTO struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
	Rel string `json:"rel"`
}

func edgeDTO(e db.Edge) EdgeDTO {
	return EdgeDTO{Src: i64s(e.From), Dst: i64s(e.To), Rel: e.Rel}
}

func edgeDTOs(in []db.Edge) []EdgeDTO {
	out := make([]EdgeDTO, 0, len(in))
	for _, e := range in {
		out = append(out, edgeDTO(e))
	}
	return out
}

// ---- Coverage asset references ----

type CoverageAssetRefDTO struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Summary      string `json:"summary"`
	SourceTaskID string `json:"source_task_id,omitempty"`
	Inherited    bool   `json:"inherited,omitempty"`
}

func coverageAssetRefDTO(ref db.AssetRef) CoverageAssetRefDTO {
	out := CoverageAssetRefDTO{
		ID: ref.ID, Kind: ref.Kind, State: ref.State, Summary: ref.Summary, Inherited: ref.Inherited,
	}
	if ref.SourceTaskID > 0 {
		out.SourceTaskID = i64s(ref.SourceTaskID)
	}
	return out
}

// ---- Finding (frontend "Finding") ----

type FindingDTO struct {
	TrafficCount          int                        `json:"traffic_count"`
	EvidenceVersion       int64                      `json:"evidence_version"`
	ReportEvidenceVersion int64                      `json:"report_evidence_version"`
	ReportStale           bool                       `json:"report_stale"`
	TrafficBindings       []db.FindingTrafficBinding `json:"traffic_bindings,omitempty"`

	ID        string `json:"id"`
	FindingID string `json:"finding_id,omitempty"` // standalone findings-table id — the handle for status updates
	VulnClass string `json:"vulnclass"`
	Name      string `json:"name,omitempty"` // 취약점 이름 비어 있으면 프런트 엔드는 vulnclass를 표시하도록 대체됩니다.
	Severity  string `json:"severity"`       // critical | high | medium | low
	Status    string `json:"status"`         // pending | in_progress | confirmed | resolved | fixed | false_positive | ignored | duplicate | risk_accepted
	Summary   string `json:"summary"`
	Evidence  string `json:"evidence"`
	Report    string `json:"report,omitempty"` // 상세 보고서(마크다운) 세부정보 인터페이스만 반환되고 목록은 비어 있습니다.

	IntentID        string            `json:"intent_id,omitempty"`
	ParamID         string            `json:"param_id,omitempty"`
	TaskID          string            `json:"task_id,omitempty"`
	TaskDescription string            `json:"task_description,omitempty"`
	SourceTaskID    string            `json:"source_task_id,omitempty"`
	Inherited       bool              `json:"inherited,omitempty"`
	Assets          []FindingAssetDTO `json:"assets,omitempty"`
	TS              string            `json:"ts"`
}

// FindingAssetDTO is one asset a finding is anchored to, pre-labelled for display.
type FindingAssetDTO struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

// assetLabel renders an asset's most identifying field for compact display.
func assetLabel(a *db.Asset) string {
	switch {
	case a.URL != "":
		if a.Method != "" {
			return a.Method + " " + a.URL
		}
		return a.URL
	case a.Domain != "":
		if a.Port != nil && *a.Port > 0 {
			return fmt.Sprintf("%s:%d", a.Domain, *a.Port)
		}
		return a.Domain
	case a.IP != "":
		if a.Port != nil && *a.Port > 0 {
			return fmt.Sprintf("%s:%d", a.IP, *a.Port)
		}
		return a.IP
	case a.AppName != "":
		return a.AppName
	case a.BundleID != "":
		return a.BundleID
	case a.ServiceName != "":
		return a.ServiceName
	case a.RootDomain != "":
		return a.RootDomain
	default:
		return "#" + i64s(a.ID)
	}
}

// findingPayload mirrors the JSON written by the worker's report_finding tool
// (agent/tools.go addFinding): {vulnclass, severity, summary, evidence:{by,poc}}.
type findingPayload struct {
	VulnClass string          `json:"vulnclass"`
	Name      string          `json:"name"`
	Severity  string          `json:"severity"`
	Summary   string          `json:"summary"`
	Evidence  json.RawMessage `json:"evidence"`
}

func findingDTO(n *db.Node) FindingDTO {
	var p findingPayload
	_ = json.Unmarshal(n.Payload, &p)
	d := FindingDTO{
		ID:        i64s(n.ID),
		VulnClass: p.VulnClass,
		Name:      p.Name,
		Severity:  p.Severity,
		Status:    db.FindingPending,
		Summary:   p.Summary,
		Evidence:  rawString(p.Evidence),
		TS:        rfc3339(n.CreatedAt),
	}
	if n.SourceTaskID > 0 {
		d.SourceTaskID = i64s(n.SourceTaskID)
	}
	d.Inherited = n.Inherited
	return d
}

// findingDTOsForTask converts a task's finding nodes to DTOs, stamping each with
// 전역 검색 페이지가 여러 작업을 그룹화할 수 있도록 소유 작업의 ID/설명.
// meta maps node id → the standalone findings row (id + status + asset ids), so the
// per-task view shows the same triage state and anchored assets as the global page;
// nodes with no row keep the 'pending' default and no finding_id (not editable).
// assets pre-resolves the anchored asset rows for label rendering.
func findingDTOsForTask(t *Task, in []*db.Node, meta map[int64]db.FindingMeta, assets map[int64]*db.Asset) []FindingDTO {
	return findingDTOsForOwner(t.ID, t.Description, in, meta, assets)
}

func findingDTOsForOwner(taskID, description string, in []*db.Node, meta map[int64]db.FindingMeta, assets map[int64]*db.Asset) []FindingDTO {
	out := make([]FindingDTO, 0, len(in))
	for _, n := range in {
		d := findingDTO(n)
		d.TaskID = taskID
		d.TaskDescription = description
		if m, ok := meta[n.ID]; ok {
			d.FindingID = i64s(m.ID)
			d.Status = m.Status
			d.TrafficCount = m.TrafficCount
			d.Assets = findingAssetDTOs(m.AssetIDs, assets)
		}
		out = append(out, d)
	}
	return out
}

// findingAssetDTOs maps anchored asset ids to display DTOs, skipping ids whose
// asset row is missing (e.g. deleted).
func findingAssetDTOs(ids []int64, assets map[int64]*db.Asset) []FindingAssetDTO {
	var out []FindingAssetDTO
	for _, aid := range ids {
		if a := assets[aid]; a != nil {
			out = append(out, FindingAssetDTO{ID: i64s(a.ID), Type: a.Type, Label: assetLabel(a)})
		}
	}
	return out
}

// findingFromDB converts a standalone DBFinding row to a FindingDTO. task_id and
// task_description are empty when the originating task has been deleted (NULL).
func findingFromDB(f *db.DBFinding, assets map[int64]*db.Asset) FindingDTO {
	status := f.Status
	if status == "" {
		status = db.FindingPending
	}
	d := FindingDTO{
		ID:           i64s(f.ID),
		FindingID:    i64s(f.ID),
		TrafficCount: f.TrafficCount, EvidenceVersion: f.EvidenceVersion, ReportEvidenceVersion: f.ReportEvidenceVersion,
		ReportStale: f.Report != "" && f.EvidenceVersion != f.ReportEvidenceVersion, TrafficBindings: f.TrafficBindings,
		VulnClass: f.VulnClass,
		Name:      f.Name,
		Severity:  f.Severity,
		Status:    status,
		Summary:   f.Summary,
		Evidence:  f.Evidence,
		Report:    f.Report,
		TS:        rfc3339(f.CreatedAt),
	}
	d.Assets = findingAssetDTOs(f.AssetIDs, assets)
	if f.TaskID != nil {
		d.TaskID = i64s(*f.TaskID)
		d.TaskDescription = f.TaskDescription
	}
	if f.NodeID != nil {
		d.IntentID = "" // node_id is the finding node, not the intent; keep IntentID empty
	}
	return d
}

// ---- Activity (frontend "Activity") ----

type ActivityDTO struct {
	Seq          int64           `json:"seq"`                 // db Activity.ID
	IntentID     string          `json:"intent_id,omitempty"` // db NodeID
	Worker       string          `json:"worker"`
	TS           string          `json:"ts"` // db CreatedAt
	Kind         string          `json:"kind"`
	Tool         string          `json:"tool,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	IsError      bool            `json:"is_error"`
	Summary      string          `json:"summary"`
	Detail       string          `json:"detail,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	SourceTaskID string          `json:"source_task_id,omitempty"`
	Inherited    bool            `json:"inherited,omitempty"`
	MainSeg      *int            `json:"main_seg,omitempty"` // main-agent conversation segment (nil for non-mainagent rows)
	// token usage (set only on kind='result'); used for per-session token totals.
	InputTokens      *int `json:"input_tokens,omitempty"`
	OutputTokens     *int `json:"output_tokens,omitempty"`
	CacheReadTokens  *int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int `json:"cache_write_tokens,omitempty"`
}

func activityDTO(a db.Activity) ActivityDTO {
	intent := ""
	if a.NodeID != nil {
		intent = i64s(*a.NodeID)
	}
	d := ActivityDTO{
		Seq:              a.ID,
		IntentID:         intent,
		Worker:           a.Worker,
		TS:               rfc3339(a.CreatedAt),
		Kind:             a.Kind,
		Tool:             a.Tool,
		ToolUseID:        a.ToolUseID,
		IsError:          a.IsError,
		Summary:          a.Summary,
		Detail:           a.Detail, // list endpoint leaves this empty (lazy)
		Metadata:         a.Metadata,
		InputTokens:      a.InputTokens,
		OutputTokens:     a.OutputTokens,
		CacheReadTokens:  a.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens,
		MainSeg:          a.MainSeg,
	}
	if a.SourceTaskID > 0 {
		d.SourceTaskID = i64s(a.SourceTaskID)
	}
	d.Inherited = a.Inherited
	return d
}

func activityDTOs(in []db.Activity) []ActivityDTO {
	out := make([]ActivityDTO, 0, len(in))
	for _, a := range in {
		out = append(out, activityDTO(a))
	}
	return out
}

// ---- Agent (frontend "Agent") — string id ----

type AgentDTO struct {
	ID               string `json:"id"`
	Key              string `json:"key"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Role             string `json:"role"`
	Builtin          bool   `json:"builtin"`
	Enabled          bool   `json:"enabled"`
	MaxTurns         int    `json:"max_turns"`
	RunSecs          int    `json:"run_seconds"`
	WebSearch        bool   `json:"web_search"`
	InteractiveShell bool   `json:"interactive_shell"`
	LLMProfileID     *int64 `json:"llm_profile_id"` // LLM 구성 바인딩;null=작업/전역 따르기
	// P3 트리거 사후 처리 전략(사용자 지정 에이전트에만 의미 있음)
	TriggerRunMode     string `json:"trigger_run_mode"`
	TriggerMergeMode   string `json:"trigger_merge_mode"`
	TriggerMaxParallel int    `json:"trigger_max_parallel"`
	// binding counts (populated only by the list endpoint) — shown on agent cards.
	McpCount   int `json:"mcp_count"`
	SkillCount int `json:"skill_count"`
	ToolCount  int `json:"tool_count"`
}

func agentDTO(a *db.Agent) AgentDTO {
	return AgentDTO{
		ID:                 i64s(a.ID),
		Key:                a.Key,
		Name:               a.Name,
		Description:        a.Description,
		Role:               a.Role,
		Builtin:            a.Builtin,
		Enabled:            a.Enabled,
		MaxTurns:           a.MaxTurns,
		RunSecs:            a.RunSecs,
		WebSearch:          a.WebSearch,
		InteractiveShell:   a.InteractiveShell,
		LLMProfileID:       a.LLMProfileID,
		TriggerRunMode:     a.TriggerRunMode,
		TriggerMergeMode:   a.TriggerMergeMode,
		TriggerMaxParallel: a.TriggerMaxParallel,
	}
}

func agentDTOs(in []*db.Agent) []AgentDTO {
	out := make([]AgentDTO, 0, len(in))
	for _, a := range in {
		out = append(out, agentDTO(a))
	}
	return out
}

// ---- LLMProfile (frontend "LLMProfile") — string id ----

type LLMProfileDTO struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Format          string  `json:"format"`
	BaseURL         string  `json:"base_url,omitempty"`
	Proxy           string  `json:"proxy,omitempty"`
	Model           string  `json:"model"`
	APIKeyHint      string  `json:"api_key_hint,omitempty"`
	RatePerSecond   float64 `json:"rate_per_second"`
	RatePerMinute   float64 `json:"rate_per_minute"`
	ContextWindowK  int     `json:"context_window_k"`
	ThinkingType    string  `json:"thinking_type"`
	ReasoningEffort string  `json:"reasoning_effort"`
	IsDefault       bool    `json:"is_default"`
	//폴링(장애 조치) 매개변수: 우선순위가 높을수록 가장 먼저 선택됩니다(활성 구성은 항상 체인의 첫 번째 구성임).
	// pool_exclude=true인 경우 장애 조치 대상으로 사용되지 않지만 여전히 에이전트/작업에 의해 명시적으로 바인딩될 수 있습니다.
	Priority    int  `json:"priority"`
	PoolExclude bool `json:"pool_exclude"`
	// 트랜시버 모드: true=스트리밍(SSE) | false=비스트리밍. 생략 금지 - false가 반드시 나타나야 함
	// 응답에서 그렇지 않으면 프런트 엔드가 "비스트리밍"을 읽을 수 없으며 스위치는 기본 스트리밍으로 돌아갑니다.
	Streaming bool `json:"streaming"`
	// 단일 응답 출력의 상한(0=보내지 않음, 서버 기본값에 따라 결정됨) 및 사용하는 요청 필드 이름
	// (''=max_tokens | 'max_completion_tokens', openai 형식만 의미가 있습니다).
	MaxTokens      int    `json:"max_tokens"`
	MaxTokensField string `json:"max_tokens_field"`
	// 사용자 정의 세션 헤더 이름: 이 HTTP 헤더는 비어 있지 않을 때 각 요청에 포함되며 헤더 값 = 현재 세션/의도의 세션 ID입니다.
	// ''=보내지 마세요. 세션 ID 헤더를 기반으로 하는 프롬프트 캐싱/고정 라우팅을 위한 게이트웨이입니다.
	SessionHeaderKey string `json:"session_header_key"`
	// 이 구성에는 재시도(연결 설정/빈 응답/동일 공급자 보안 창)가 포함됩니다. 각 시도:
	// 0=글로벌 전략 상속 | -1=이 레이어를 끄고 다시 시도 | >0=횟수; Interval_ms: 0=기본 인덱스를 사용하여 백오프 |
	// >0=대신 이 고정된 밀리초 간격을 사용하십시오. 모두 0 = 전역, 즉 역사적 동작을 완전히 따릅니다.
	Retry db.RetryOverride `json:"retry"`
}

func llmProfileDTO(p *db.LLMProfile) LLMProfileDTO {
	return LLMProfileDTO{
		ID:               i64s(p.ID),
		Name:             p.Name,
		Format:           p.Format,
		BaseURL:          p.BaseURL,
		Proxy:            p.Proxy,
		Model:            p.Model,
		APIKeyHint:       p.APIKeyHint,
		RatePerSecond:    p.RatePerSecond,
		RatePerMinute:    p.RatePerMinute,
		ContextWindowK:   p.ContextWindowK,
		ThinkingType:     p.ThinkingType,
		ReasoningEffort:  p.ReasoningEffort,
		IsDefault:        p.IsDefault,
		Priority:         p.Priority,
		PoolExclude:      p.PoolExclude,
		Streaming:        p.Streaming,
		MaxTokens:        p.MaxTokens,
		MaxTokensField:   p.MaxTokensField,
		SessionHeaderKey: p.SessionHeaderKey,
		Retry:            p.Retry,
	}
}

func llmProfileDTOs(in []*db.LLMProfile) []LLMProfileDTO {
	out := make([]LLMProfileDTO, 0, len(in))
	for _, p := range in {
		out = append(out, llmProfileDTO(p))
	}
	return out
}

// idStrings formats a slice of int64 ids as strings (for visibility agent ids).
func idStrings(in []int64) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, i64s(v))
	}
	return out
}
