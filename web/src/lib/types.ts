// ARTEX domain model — types used across the UI.
// 기능 사양에서 파생되었습니다(섹션 7: 주요 데이터 형태).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  id: string;
  name?: string; // 선택적 작업 이름. 비어 있음/기본값 = 이름이 지정되지 않음, 표시 시 설명으로 대체
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  findings?: { critical: number; high: number; medium: number; low: number }; // 등록된 취약점 수(심각도별로 분류)
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // 자산 적용 범위 기능 스위치(생성 시 설정, 기본적으로 활성화됨) false=범위를 계산하지 않음/표시하지 않음
}

export interface TaskCategory {
  id: number;
  name: string;
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  category_id?: number | null; //기본 카테고리; 널/기본값=없음
  intercept_rules?: AssetInterceptRuleInput[]; // 사전 설정된 작업 수준 차단/허용 규칙
  created_at: string;
  updated_at: string;
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // 귀속된 회사 자산 ID; 비어 있음 = 귀속되지 않음
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// ---- Asset coverage graph (per task) ----
// 자산 오버레이의 노드로 강제 전달됩니다. 고유 키: Asset="a:<id>", company="c:<id>",
// 자산 행이 없는 경우 루트 도메인 이름="r:<domain>"입니다. In_scope=false는 와이어에만 사용되는 회색 컨텍스트 노드입니다.
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// 이 작업 탐색 그래프의 자산과 관련된 의도/사실/발견입니다(오버레이 그래프 노드 서랍에 사용됨).
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// 작업 테스트 범위(적용 범위 분모 + 권한 부여 경계) 라인입니다.
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // 백엔드 JOIN 회사 구문 분석, kind=company에만 값이 있음
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// 새로운 기업을 추가할 때 제출된 구조화 자산 범위 규칙.
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// 자산 범위 작성 결과입니다. 오류는 이 제출물에서 잘못된 줄입니다. 경고는 이 제출물과 관련이 없습니다.
// 그러나 속성 결과가 기대에 미치지 못하는 기존 데이터 문제가 있습니다(예: IP 필드에 호스트 이름이 저장된 자산).
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// 회사의 자산 범위 규칙 중 하나입니다(단일 진실 소스에 귀속).
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; //종류=도메인인 경우 값이 있습니다.
  net?: string; // kind=ip|cidr은 다음과 같은 경우에 값을 갖습니다.
  value?: string; // kind=icp|keyword인 경우 백엔드에서 직접 반환될 수 있습니다.
  raw: string; // 표시 및 백필에 사용되는 원시 사용자 입력
  reason?: string;
}

// Enterprise: 유형=회사에 대한 자산 노드 + 아이콘 + 자산 수 + 자산 범위 규칙.
export interface Company {
  id: number;
  name: string;
  logo?: string; // 원격 아이콘 URL; 비어 있으면 프런트 엔드에서 이름의 첫 글자를 사용합니다.
  asset_count: number;
  scope?: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // 허위삭제 의도시 삭제사유(상태='삭제됨')
}

// 게시판의 한 페이지: 생성된 순서대로 페이지가 매겨진 노드 + 이 페이지에 관련된 엣지 + 엣지의 반대쪽 끝에 있는 노드(refs, id로 인덱스됨),
// 이렇게 하면 각 방송마다 전체 화면을 끌어내리지 않고도 '어디에서 왔으며 무엇을 생산하는지'를 명확하게 설명할 수 있다.
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // 노드 ID → 해당 노드에 고정된 자산(게시판이 확장되면 표시되며 이 페이지의 노드와 그 인접 노드도 포함)
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// 대상 관리 카드에서 사용하는 대상(백엔드가 페이로드를 text/vulnclass로 분할했습니다).
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// 제약 조건 관리 카드에 사용되는 운영 제약 조건(허용=허용 / 거부=거부)
export type ConstraintKind = "allow" | "deny";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// 취약점 처리 상태: 보류 중/처리 중/확인됨/처리됨/수정됨/거짓양성/무시/중복/위험 허용됨.
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

//FindingAsset은 취약점에 바인딩된 자산입니다(레이블은 백엔드에서 사전 렌더링되었습니다).
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding {
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // 독립 결과 테이블의 행 ID, 상태 업데이트 핸들(작업의 이전 노드가 누락될 수 있음)
  vulnclass: string;
  name?: string; // 취약점 이름 비어 있으면 디스플레이가 vulnclass로 돌아갑니다.
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // 상세 보고서(마크다운) 세부정보 인터페이스만 반환되고 목록은 비어 있습니다.
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage는 검색 목록의 서버측 페이지가 매겨진 응답입니다.
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // 선택적 작업 이름. 비어 있음/기본값 = 이름 없음
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats는 서버 측에서 계산되는 전체 테이블 집계(통계 카드 + 취약점 유형 드롭다운)이며 페이징의 영향을 받지 않습니다.
export interface FindingStats {
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption은 검색 페이지의 "작업별" 필터 드롭다운에 있는 항목입니다: 취약점이 있는 작업(빈 설명은 작업이 삭제되었음을 나타냅니다.
// 프런트엔드 롤백에는 ID)와 취약점 수가 표시됩니다.
export interface FindingTaskOption {
  id: string | number;
  name?: string; // 선택적 작업 이름. 비어 있음/기본값 = 이름 없음
  description: string;
  count: number;
}

// FindingQuery는 검색 목록 페이징/필터링/정렬 매개변수입니다.
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // 작업 ID;"all"/empty = 작업별로 필터링하지 않음
  query?: string;
  sort?: "severity" | "time";
  // 자산 트리 노드 키 노드 선택 = 전체 하위 트리를 선택합니다. 비어 있음 = 자산별로 필터링하지 않습니다.
  assetScope?: string;
}

// ---- 자산별 조사 결과(자산 보기) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

//FindingAssetNode는 자산 트리의 노드입니다. 키는 a:<id>(자산), c:<id>(기업),
// r:<domain>(라이브러리에 자산 행의 루트 도메인 이름이 없음), __none__(연결된 자산이 없음).
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // 이 자산에 직접 연결된 발견 수
  total: number; // 하위 항목 포함, 발견에 따라 중복 제거
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET은 백엔드 db.FindingUnsignedAsset에 해당합니다.
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment는 업로드된 파일입니다. 경로는 세션/작업 작업 디렉터리(즉, 에이전트의 CWD)를 기준으로 합니다.
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // 절대 경로(임시 업로드 시 범위=스테이징이 반환됩니다. 작업을 생성하기 전에 설명에 적어주세요)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- 에이전트 트리거(P3 예약, 사용자 지정 에이전트만 해당) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // 타이밍: N초마다(0=예약되지 않음)
  on_finding: boolean; // 작업에서 발견 항목을 찾으면 트리거됩니다.
  on_goal_met: boolean; // 작업이 목표에 도달하면 트리거됩니다.
  on_task_timeout: boolean; // 작업 시간이 초과되면 트리거됩니다.
  on_tool_call: boolean; //선택한 도구가 호출될 때 트리거됩니다(실행 완료).
  on_task_create: boolean; // 작업이 생성되면 트리거됩니다.
  interval_message: string; // 각 트리거 조건에 대한 독립적인 사용자 메시지
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // on_tool_call 선택한 도구 키(최소 하나)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system";
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// 일괄 재분류 작업별 결과입니다. 실패는 작업이 삭제된 경우에만 가능하며 범주 자체의 작성은 원자적입니다.
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // 에이전트는 기본적으로 꺼져 있는 트래픽 증거를 자동으로 바인딩합니다. 수동 바인딩에는 영향을 미치지 않습니다.
  llm_record: boolean; // LLM 기록 스위치(기본적으로 꺼져 있음) 꺼져 있으면 LLM 호출가 녹음되지 않습니다
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // 검색 엔드포인트에 액세스하기 위한 독립형 송신 프록시(http/https/socks5) 트래픽을 기록하는 MITM 프록시와 독립적입니다. 비어 있음 = 직접 연결.
  web_search_proxy?: string;
  // 글로벌 내보내기 프록시(http/https/socks5, user:pass 가능), 모든 대상 트래픽이 이를 통과합니다. 트래픽 캡처가 켜진 경우
  // MITM 업스트림; 캡처가 꺼진 경우 에이전트를 직접 주입하는 bash/WebFetch. 비어 있음 = 직접 연결.
  global_proxy?: string;
  python_interpreter?: string; // 사용자 정의 스크립트 도구를 위한 Python 인터프리터 경로(비어 있음 = 런타임 감지)
  workers?: number; // 동시 작업 에이전트 수(기본값 3) 나중에 시작된 작업에 적용됩니다.
  // 작업 동시성 상한: 동시에 "실행"되는 작업 수의 상한입니다. 끄기 = 제한 없음; 활성화된 후 새 작업이 제한을 초과하면 대기열에 추가되고 공간이 있으면 자동으로 시작됩니다.
  task_concurrency_enabled?: boolean; // 기본값은 거짓
  task_concurrency_limit?: number; // 개봉 후 기본값은 5입니다.
  //LLM 폴링(장애 조치). 기본적으로 꺼져 있습니다. 켜져 있으면 "지정되지 않은 모델"이 있는 에이전트를 현재 구성에서 사용할 수 없습니다.
  // (잔고부족/키무효/현재제한/서비스 이상) 자동으로 다음 구성으로 전환됩니다.
  llm_pool_enabled?: boolean; // 기본값은 거짓
  // 지정된 구성에 바인딩된 에이전트/작업이 실패할 경우 폴링 체인으로 대체할지 여부입니다. 기본값 false = 경계가 배타적입니다.
  llm_pool_bind_fallback?: boolean;
  // 작업 제약 조건 주입 범위(둘 다 기본적으로 활성화됨): 작업의 허용/거부 제약 조건을 해당 에이전트의 시스템 프롬프트에 넣습니다.
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // 실험적 기능: noa 모델 기반 컨텍스트 압축(기본적으로 꺼져 있음) 4가지 유형의 에이전트(기획자/
  // 작업자/주요 에이전트/대화), noa는 기본 제공 압축을 대체하여 컨텍스트 압축을 대신합니다. 실행당 한 번씩 읽혀지며,
  // 시작된 실행은 나중에 적용됩니다.
  noa_compaction?: boolean;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // 사고 활성화(thinking.type): ""=보내지 않음(기본값) | "disabled"=끔 | "enabled"=켬
  thinking_type?: string;
  // 추론 강도: ""=보내지 않음(기본값) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // 폴링 순서: 값이 클수록 먼저 선택됩니다. 활성 구성은 항상 체인의 헤드이며 이 값과 아무 관련이 없습니다.
  priority?: number;
  // true = 장애 조치 대상으로 사용되지 않습니다(명시적 에이전트/작업 바인딩에서는 계속 사용할 수 있음).
  pool_exclude?: boolean;
  // true(기본값) = 스트리밍(SSE) | false = true 비스트리밍(stream:false, 한 번 반환됨).
  streaming?: boolean;
  //단일 응답에 대한 출력(토큰)의 상한입니다. 0 = 서버의 기본값에 따라 결정된 이 필드를 보내지 않습니다.
  // context_window_k와의 차이점에 유의하세요. 후자는 모델의 총 용량이며 압축 임계값에 대해 로컬로만 사용됩니다.
  max_tokens?: number;
  // 상한에는 어떤 요청 필드 이름이 사용됩니까? format="openai"만 의미가 있습니다.
  // ""=max_tokens(기본값) | "max_completion_tokens"(OpenAI 추론 모델만 인식함)
  max_tokens_field?: string;
  // 사용자 정의 세션 헤더 이름: 이 HTTP 헤더는 비어 있지 않을 때 각 요청에 포함되며 헤더 값 = 현재 세션/의도의 세션 ID입니다.
  // ""=보내지 않습니다. 세션 ID 헤더를 기반으로 하는 프롬프트 캐싱/고정 라우팅을 위한 게이트웨이입니다.
  session_header_key?: string;
  // 이 구성에는 재시도(연결 설정/빈 응답/동일 공급자 보안 창)가 포함됩니다. 비워두기/모두 0 = 글로벌 정책을 따릅니다.
  retry?: LLMRetryOverride;
}

// ---- LLM 재시도 전략 ----
// 다시 시도할 손잡이 2개의 레이어 1개. 둘 다 "0 = 구성되지 않음"입니다.
//   시도 0=기본 시도 횟수 사용 | -1=레이어를 끄고 다시 시도 | >0=재시도 횟수
//   Interval_ms 0=기본 지수 백오프 사용 | >0=대신 이 고정된 밀리초 간격을 사용합니다.
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// 단일 LLM 구성이 처리할 수 있는 세 가지 계층이 있습니다(모두 "끝점 따르기" 재시도임).
export interface LLMRetryOverride {
  connect: LLMRetryRule; // 연결 설정 재시도: 스트림 시작 전 연결 재설정/시간 초과/429/5xx
  empty: LLMRetryRule; //빈 응답 재시도: 콘텐츠 없이 완료됨(openai 형식만 해당)
  stream: LLMRetryRule; // 공급자 안전 창 재시도와 동일: 출력이 전달되기 전 스트림 재생
}

// 글로벌 정책 = 위 세 레이어의 기본값 + 두 레이어는 전역에만 적용됩니다.
//   차단기 폴링 퓨즈(시도=수차례 연속 순간 퓨즈 오류, 간격_ms=고정된 냉각 시간)
//   의도 재실행(작업자가 model_error로 종료된 후 전체 의도가 재실행됨)
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM 폴링(장애 조치)----
// 폴링 체인에서 구성의 위치 및 상태입니다. 상태:
//   알았어 정상
//   성능 저하 연속 오류가 발생했지만 회로 차단기 임계값에 도달하지 않았습니다.
//   Tripped가 끊어져서 냉각 기간 동안 건너뜁니다. (cooldown_secs는 남은 초입니다.)
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // 현재 활성 구성인지 여부(항상 체인의 헤드)
  excluded: boolean; // pool_exclude: 폴링에 참여하지 않습니다.
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; //목표/계획자/메인에이전트/작업자로 내장되어 있습니다. 사용자 정의 키로 사용자 정의
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // LLM 구성 바인딩; null/absent = 작업/세션/전역 따르기
  max_turns?: number; // 0 = 제한 없음
  run_seconds?: number; // 단일 작업자 실행에 대한 최대 벽시계 제한(초)입니다. 0 = 제한 없음
  web_search?: boolean; // 네트워크 검색 활성화 여부(시스템 전역 스위치에 의해 게이트됨)
  interactive_shell?: boolean; // 대화형 셸 활성화 여부(영구 PTY 세션 도구 제품군)
  // P3 사후 트리거 처리 전략(사용자 지정 에이전트에만 의미 있음)
  trigger_run_mode?: "serial" | "parallel"; // 직렬 대기열/각 트리거에는 동시 세션이 있습니다.
  trigger_merge_mode?: "by_task" | "all" | "none"; // 직렬만: 동일한 작업과 병합/모두 병합/병합하지 않음
  trigger_max_parallel?: number; // 병렬만: 에이전트당 동시성의 상한입니다. 0=제한 없음
  // 바인딩 수(목록 인터페이스에서만 반환): Visible MCP / Visible Skill / Binding Tool
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // 바인딩 가능한 LLM 구성 후보("기본 모델" 드롭다운용) 현재 바인딩은 agent.llm_profile_id를 참조하세요.
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // 닫는 프롬프트가 저장되었습니다(비어 있음 = 내장된 기본값 사용).
  wrapup_default?: string; // 내장된 기본 닫기 프롬프트(자리 표시자/기본값 복원)
  wrapup_max_turns?: number; // 저장된 마감 라운드 수(0=내장된 기본값 사용)
  wrapup_max_turns_default?: number; // 내장된 기본 마감 라운드 번호("0=기본 N" 프롬프트의 경우)
  //작업 수준 제한 시간 종료 단어(작업자/계획자만 해당, 이 파티션은 task_timeout_wrapup_supported=true인 경우 표시됨)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // 호출 통계(skill_usage 원장). 호출된 적이 없는 스킬:call=0, last_used 기본값.
  calls: number;
  tasks: number; // 로드된 작업 수(채팅 세션은 계산되지 않음)
  usage_agents: string[]; // 해당 에이전트 키가 로드되었습니다.
  last_used?: string;
}

// SkillCall은 Skill() 호출(단일 스킬에 대한 최근 호출 목록)입니다.
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = 미션이 아닌 시나리오(대화 세션)
  session_id: string;
  args_len: number;
}

// MissingSkill은 이름은 있지만 존재하지 않는 스킬, 즉 "사용하고 싶지만 가지고 있지 않은" 공백입니다.
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- 도구(내장 도구 디렉터리) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // 맞춤형 도구 유형
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // 사용자 정의 도구 실행 사양(종류!=내장)
  deferred?: boolean; // 스키마 지연(SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

// ---- 자산 차단 규칙(자산 차단: 글로벌 블랙리스트) ----
export type AssetInterceptKind =
  | "exact_domain"
  | "exact_ip"
  | "exact_url"
  | "fuzzy_domain"
  | "fuzzy_ip"
  | "fuzzy_url"
  | "cidr";

// 작업은 작업 수준 규칙인 block=가로채기(금지된 테스트) 허용=허용(허용 목록)에만 사용됩니다.
export type AssetInterceptAction = "block" | "allow";

//작업 수준 자산 차단/허용 규칙에 대한 입력 항목(작업 생성 및 작업 세부정보 편집 시 사용)
export interface AssetInterceptRuleInput {
  action: AssetInterceptAction;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  enabled: boolean;
}

export interface AssetInterceptRule {
  id: number;
  enabled: boolean;
  action?: AssetInterceptAction; // 전역 규칙에는 이 필드가 없습니다(항상 가로채기). 작업 수준 규칙은 차단/허용을 구별합니다.
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  reason: string; // 규칙 메시지 또는 모델 판단 이유(모델 판단 앞에 [모델]이 붙음)
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: 모델 승인의 전역 구성(차단 규칙이 적용되지 않는 경우에만 모델에 의해 판단됨)
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = 활성/기본 구성을 따릅니다.
  prompt: string; // 결정 프롬프트; GET 백엔드는 기본 제공 템플릿이 설정되지 않은 경우 전체 텍스트를 백필합니다.
  timeout_seconds: number; // 모델 호출 시간 초과
  fail_action: "allow" | "ask" | "deny"; // 모델 오류/시간 초과/파싱할 수 없는 경우 대체
  ask_timeout_seconds: number; // 모델 평가 요청 후 승인 대기 시간 초과가 수동으로 전환됨
  ask_timeout_action: "allow" | "deny"; // 승인 시간 초과 후 기본 작업
}

export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
}

// ── 자산 동기화(ScopeSentry 데이터 소스) ──────────────────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// 단일 도구의 호출 통계(/commands/stats) 오류는 실패 횟수입니다.
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  //공급자가 HTTP 원문을 번역했습니다.
  // 스키마), 응답은 원본 SSE 프레임입니다.
  // 도구 스키마 및 tool_use 블록이 삭제되었습니다. 이전 레코드가 비어 있습니다.
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/** GET /api/update/check: 현재 버전과 GitHub 최신 정식 버전의 비교 결과입니다. */
export interface UpdateCheck {
  /** 현재 실행 중인 버전입니다. 개발 빌드는 "dev" 또는 Git 설명이 붙은 형태입니다. */
  current: string;
  /** 달리기 형태. Docker에서 다시 설치하면 컨테이너의 쓰기 가능한 레이어에만 영향을 미치며 컨테이너를 다시 빌드하면 이미지 버전이 반환됩니다. */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  repo: string;
  /** 롤백 가능한 이전 버전(artex.old)이 있는지 나타냅니다. */
  has_backup: boolean;
  /** 이번 시작 중 자동 업데이트의 결과입니다. 교체 실패 또는 롤백 등이 없으면 비어 있습니다. */
  boot_notice?: string;
  rolled_back?: boolean;
  /** GitHub 조회 실패 원인입니다. 실패한 경우 아래 버전 정보는 제공되지 않습니다. */
  error?: string;
  latest?: string;
  notes?: string;
  html_url?: string;
  published_at?: string;
  /** 현재 플랫폼의 배포 파일 이름과 해당 릴리스에 실제로 포함되어 있는지 나타냅니다. */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /** 버전 번호를 비교할 수 있는지 나타냅니다. 개발 빌드는 false이며 원클릭 업데이트가 비활성화됩니다. */
  comparable?: boolean;
  /** 비교가 거짓인 경우의 설명입니다.*/
  reason?: string;
}

/** /api/update/stream에 의해 업데이트 진행 상황이 푸시됩니다. */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /** 다운로드 단계만 의미가 있습니다(0-100). 나머지 단계는 -1입니다. */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}
