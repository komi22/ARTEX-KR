// 패키지 db는 ARTEX용 PostgreSQL 데이터 소스입니다(기존 그래프 단일 파일 SQLite 대체).
// 연결을 열고, 스키마를 적용하고, 내장 에이전트 및 변수 디렉터리를 시드합니다.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/artex/config"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver ("pgx")
)

//go:embed schema.sql
var schemaSQL string

const schemaMigrationLockKey int64 = 7337741001

var schemaDeadlockRetryDelays = [...]time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
}

type schemaExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func isPostgresDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

func applySchemaWithRetry(ctx context.Context, execer schemaExecer, sleep func(time.Duration)) error {
	for attempt := 0; ; attempt++ {
		if _, err := execer.ExecContext(ctx, schemaSQL); err != nil {
			if !isPostgresDeadlock(err) || attempt >= len(schemaDeadlockRetryDelays) {
				return err
			}
			sleep(schemaDeadlockRetryDelays[attempt])
			continue
		}
		return nil
	}
}

// withSchemaMigrationLock pins the session-level lock to one checked-out
// connection. Running pg_advisory_lock through *sql.DB is incorrect because a
// later schema or unlock call may use a different pooled PostgreSQL session.
func withSchemaMigrationLock(ctx context.Context, sqlDB *sql.DB, action func(*sql.Conn) error) (err error) {
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer func() {
		if _, unlockErr := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, schemaMigrationLockKey); unlockErr != nil && err == nil {
			err = fmt.Errorf("advisory unlock: %w", unlockErr)
		}
	}()
	return action(conn)
}

// coordinateWithSchemaMigration makes long, multi-table archive transactions
// mutually exclusive with startup DDL while allowing ordinary runtime queries
// to continue normally.
func coordinateWithSchemaMigration(tx *sql.Tx) error {
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("coordinate with schema migration: %w", err)
	}
	return nil
}

// DSN resolves the PostgreSQL connection string and reports where it came from.
// Precedence: env ARTEX_PG_DSN > config file (config.json). There is no
// built-in default — it errors if neither source is configured.
func DSN() (dsn, source string, err error) {
	return config.PostgresDSN()
}

// DB wraps the shared *sql.DB. PG handles its own connection pool + concurrency
// (MVCC), so unlike the old SQLite store there is no process-wide write mutex.
type DB struct{ *sql.DB }

// ensureDatabase connects to the postgres system database and creates the target
// database if it does not exist. dsn must be a postgres:// URL.
func ensureDatabase(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil // unparseable DSN — let the normal Open fail with a clear error
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" || dbName == "postgres" {
		return nil
	}
	// connect to the postgres maintenance database instead
	adminDSN := *u
	adminDSN.Path = "/postgres"
	admin, err := sql.Open("pgx", adminDSN.String())
	if err != nil {
		return nil // best-effort; let Open surface the real error
	}
	defer admin.Close()
	if err := admin.Ping(); err != nil {
		return nil
	}
	var exists bool
	_ = admin.QueryRow(`SELECT true FROM pg_database WHERE datname=$1`, dbName).Scan(&exists)
	if !exists {
		if _, err := admin.Exec(`CREATE DATABASE "` + dbName + `"`); err != nil {
			return fmt.Errorf("create database %q: %w", dbName, err)
		}
	}
	return nil
}

// Open connects, applies the schema (idempotent), and seeds builtin rows.
func Open(dsn string) (*DB, error) {
	if err := ensureDatabase(dsn); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping postgres (%s): %w", config.Redact(dsn), err)
	}
	d := &DB{sqlDB}
	// pgx runs multi-statement Exec via the simple protocol when there are no args.
	// Keep the dedicated lock connection checked out until both DDL and seeding
	// finish so concurrent application instances cannot initialize out of order.
	err = withSchemaMigrationLock(context.Background(), sqlDB, func(conn *sql.Conn) error {
		if err := applySchemaWithRetry(context.Background(), conn, time.Sleep); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		if err := d.seedBuiltins(); err != nil {
			return fmt.Errorf("seed builtins: %w", err)
		}
		return nil
	})
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// builtinAgent describes one of the fixed agents and its prompt-variable catalog.
type builtinAgent struct {
	key, name, role, desc string
	vars                  []promptVar
	interactiveShell      bool // CCB 중 기본 대화형 셸 스위치입니다. ON CONFLICT는 사용자가 이후에 수동으로 전환하는 경우에는 적용되지 않습니다.
	runSeconds            *int // CCB에서 단일 실행에 대한 벽시계의 상한(초)입니다. nil=시드 포함 기본값(1200), 0=시간 제한 없음
}

type promptVar struct{ name, desc, example, source string }

// intp는 buildinAgent 선택적 필드(예: runSeconds)에 대한 값을 명시적으로 설정하는 데 사용되는 v에 대한 포인터를 반환합니다.
func intp(v int) *int { return &v }

// 내장 에이전트는 문서 §5(a)를 미러링합니다. 내장 도구는 라이브러리에 포함되어 있지 않습니다. 여기에는 시드 에이전트 + 변수 디렉토리만 포함됩니다.
// 참고: planner/worker/mainagent/auto의 대화형 셸은 아래의 Interactive_shell_default_v1로 기본 설정됩니다.
// 블록은 모두 true로 설정됩니다(후속 토글 고려). 여기의 InteractiveShell은 "CCB가 기본적으로 활성화됩니다"가 필요한 새 에이전트에만 사용됩니다.
var builtinAgents = []builtinAgent{
	{"goals", "타겟 분해", "goals", "침투 임무 목표를 여러 개의 독립적이고 검증 가능한 하위 목표로 분해합니다.", []promptVar{
		{"EngagementDescription", "작업 설명(테스트 개체/배경)", "example.com 사이트 테스트", "exploration"},
		// 이제 전역 런타임 변수(server.globalPromptVars 참조)이며 더 이상 각 에이전트 디렉터리에 없습니다.
		//정의를 반복하세요. 그렇지 않으면 withGlobalVars가 추가될 때 전역 항목과 충돌합니다.
	}, false, nil},
	{"planner", "계획", "planner", "상황을 읽고, 목표를 결정하고, 다루지 않는 새로운 방향이 있는 경우에만 탐색 의도를 추가합니다(각 작업에 대해 하나의 계획 주기).", []promptVar{
		{"Goal", "전체 임무 목표", "example.com 관리자 권한 얻기", "exploration"},
		{"AssetSummary", "자산 개수/유형 분포 요약(선택사항)", "domain:3 ip:5 site:2", "distilled"},
	}, false, nil},
	{"mainagent", "메인", "main", "인간-컴퓨터 인터페이스: 진행 상황을 관찰하고 인간의 의도를 힌트 또는 우선순위가 높은 의도로 구현합니다.", []promptVar{
		{"Goal", "현재 미션 목표", "example.com 관리자 권한 얻기", "exploration"},
		{"AssetSummary", "개통 상황 요약(선택)", "domain:3 ip:5", "distilled"},
		{"FindingsSummary", "확인된 취약점 요약(선택사항)", "high:1 medium:2", "distilled"},
	}, false, nil},
	{"worker", "실행", "worker", "실행 의도를 받고 발견된 사실/취약점을 다시 지식 그래프에 기록한 다음 중지합니다.", []promptVar{
		{"ProxyAddr", "프록시 주소 기록(이중 복사할 경우 드라이브)", "127.0.0.1:8080", "runtime"},
		{"WorkerName", "근로자 본인확인(선택사항)", "worker-1", "runtime"},
	}, false, nil},
	// 자동: 내장된 "플랫폼 작업" 에이전트. 침투 조정 주기에는 참여하지 않습니다. 대화 페이지에 의해 구동되며 도구를 사용하여 플랫폼을 운영합니다.
	{"auto", "Auto", "assistant", "플랫폼 운영 도우미: 도구를 사용하여 작업(생성/보기/일시 중지/프롬프트 제공) 및 자산을 관리하고 기술, 사용자 정의 도구 및 MCP를 생성/수정합니다.", nil, false, nil},
	// 침투 테스트: 내장된 "독립 침투" 에이전트. 대화 페이지를 통해 한 사람이 정찰부터 결론, 계획, 실행 및 검증까지 전체 침투 체인을 직접 살펴볼 수 있습니다. 대화형 셸은 기본적으로 활성화되어 있습니다.
	{"pentest", "침투 테스트", "assistant", "독립 침투 에이전트: 한 사람이 정찰 → 공격 표면 찾기 → 심층 활용 → 검증 → 종결, 계획, 실행 및 검증까지 전체 체인을 스스로 진행합니다.", nil, true, intp(0)},
}

// seedBuiltins inserts the fixed built-in agents and their variable catalog (idempotent).
func (d *DB) seedBuiltins() error {
	for _, a := range builtinAgents {
		var agentID int64
		err := d.QueryRow(`
INSERT INTO agents(key, name, description, role, builtin, enabled, interactive_shell, run_seconds)
VALUES ($1, $2, NULLIF($3,''), $4, true, true, $5, COALESCE($6, 1200))
ON CONFLICT (key) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description
RETURNING id`, a.key, a.name, a.desc, a.role, a.interactiveShell, a.runSeconds).Scan(&agentID)
		if err != nil {
			return fmt.Errorf("agent %s: %w", a.key, err)
		}
		for _, v := range a.vars {
			if _, err := d.Exec(`
INSERT INTO agent_prompt_vars(agent_id, var_name, description, example, source)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (agent_id, var_name) DO UPDATE
  SET description = EXCLUDED.description, example = EXCLUDED.example, source = EXCLUDED.source`,
				agentID, v.name, v.desc, v.example, v.source); err != nil {
				return fmt.Errorf("agent %s var %s: %w", a.key, v.name, err)
			}
		}
	}
	// Drop catalog entries for variables that were renamed, so the white-list no
	// longer advertises a name templates can't resolve (EngagementTitle→Description).
	// '현재'가 각 에이전트 디렉터리에서 전역 런타임 변수로 승격된 후에도 이전 라이브러리의 목표에는 여전히 하나의 '현재'가 있습니다.
	// 전역 항목 이름과 충돌합니다(프런트엔드 변수 목록 키가 반복됨). 함께 지워집니다.
	if _, err := d.Exec(`DELETE FROM agent_prompt_vars WHERE var_name IN ('EngagementTitle', 'CoverageGaps', 'Now')`); err != nil {
		return fmt.Errorf("cleanup renamed vars: %w", err)
	}
	// Default-on interactive_shell for the runtime agents (planner/worker/mainagent/auto)
	// ONCE — respects a later user toggle-off (guarded by a settings flag). goals(one-shot
	// decomposer) stays off. Runs after the column exists (schema applied before seed).
	if v, _, _ := d.GetSetting("interactive_shell_default_v1"); v != "true" {
		if _, err := d.Exec(`UPDATE agents SET interactive_shell=true WHERE key IN ('planner','worker','mainagent','auto')`); err != nil {
			return fmt.Errorf("seed interactive_shell defaults: %w", err)
		}
		_ = d.SetSetting("interactive_shell_default_v1", "true")
	}
	// 내장 브라우저(극작가) MCP를 한 번 시드합니다. 기본적으로 비활성화되어 있습니다(사용자
	// 필요할 때 활성화), 기본적으로 프록시는 없습니다. 트래픽 캡처 토글은 삽입/제거합니다.
	// the recording proxy + CA at runtime (server.Manager.syncBrowserMCPProxy).
	// Insert only if absent so we never clobber user edits (args/env/enabled/
	// visibility) on restart.
	if _, err := d.Exec(`
INSERT INTO mcp_servers(name, transport, command, args, env, enabled)
VALUES ('browser', 'stdio', 'npx', $1, '{}', false)
ON CONFLICT (name) DO NOTHING`,
		`["@playwright/mcp","--headless"]`); err != nil {
		return fmt.Errorf("seed browser mcp: %w", err)
	}
	// NOTE: the placeholder ScopeSentry data-source MCP (empty URL + empty X-API-Key,
	// disabled) is seeded directly in schema.sql §F so a raw `psql < schema.sql` init
	// also gets it. schema.sql is Exec'd on every startup, so it stays idempotent.
	if err := d.seedBuiltinSkillVisibility(); err != nil {
		return fmt.Errorf("seed skill visibility: %w", err)
	}
	if err := d.seedDefaultInterceptRules(); err != nil {
		return fmt.Errorf("seed intercept rules: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV2(); err != nil {
		return fmt.Errorf("seed intercept rules v2: %w", err)
	}
	if err := d.seedDefaultAssetInterceptRules(); err != nil {
		return fmt.Errorf("seed asset intercept rules: %w", err)
	}
	return nil
}

// seedDefaultAssetInterceptRules inserts the built-in asset blocklist (fuzzy
// domain matches for government / education sites) once on first startup. Gated
// by a settings flag so a user's later disable/delete is never resurrected on
// restart — same policy as the intercept-rule seed.
func (d *DB) seedDefaultAssetInterceptRules() error {
	if v, _, _ := d.GetSetting("asset_intercept_default_rules_v1"); v == "done" {
		return nil
	}
	rules := []struct {
		kind    string
		pattern string
		note    string
	}{
		{"fuzzy_domain", ".gov", "[내장] 정부 웹사이트(.gov)"},
		{"fuzzy_domain", ".gov.cn", "[내장] 정부 웹사이트(.gov.cn)"},
		{"fuzzy_domain", ".edu", "[내장] 교육 웹사이트(.edu)"},
		{"fuzzy_domain", ".edu.cn", "[내장] 교육 웹사이트(.edu.cn)"},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO asset_intercept_rules(enabled, kind, pattern, note, builtin)
VALUES (true, $1, $2, $3, true)
ON CONFLICT DO NOTHING`, r.kind, r.pattern, r.note); err != nil {
			return fmt.Errorf("asset rule %q: %w", r.pattern, err)
		}
	}
	return d.SetSetting("asset_intercept_default_rules_v1", "done")
}

// builtinSkillVisibility maps a shipped skill's directory name → the built-in
// agent keys that should see it by default. The skill FILES themselves live on the
// filesystem (SkillDir, loaded by norma at runtime); DB only carries this visibility
// binding. Skills omitted here (e.g. playwright-cli, scopesentry) ship invisible by
// default — the user turns them on per-agent when needed. scopesentry additionally
// declares `mcps: ScopeSentry`, which only takes effect once it's made visible and
// that MCP is enabled/configured.
var builtinSkillVisibility = map[string][]string{
	"api-recon": {"auto", "pentest", "worker"},
}

// seedBuiltinSkillVisibility binds the shipped built-in skills to their default
// agents. Insert-if-absent (ON CONFLICT DO NOTHING) so a user's later toggle-off is
// never resurrected on restart — matches the browser-MCP / intercept-rule seed policy.
func (d *DB) seedBuiltinSkillVisibility() error {
	for skillName, agentKeys := range builtinSkillVisibility {
		for _, key := range agentKeys {
			if _, err := d.Exec(`
INSERT INTO agent_skill_visibility(agent_id, skill_name, enabled)
SELECT id, $2, true FROM agents WHERE key=$1
ON CONFLICT (agent_id, skill_name) DO NOTHING`, key, skillName); err != nil {
				return fmt.Errorf("skill %s → agent %s: %w", skillName, key, err)
			}
		}
	}
	return nil
}

// seedDefaultInterceptRules inserts built-in safety intercept rules once on
// first startup. The seed is gated by a settings flag so user edits (disable,
// delete, re-order) are never overwritten on subsequent restarts.
func (d *DB) seedDefaultInterceptRules() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v1"); v == "done" {
		return nil
	}
	type rule struct {
		name     string
		target   string // tool_name | tool_input
		typ      string // string | regex
		pattern  string
		action   string
		message  string
		priority int
	}
	rules := []rule{
		// ── 시스템 파괴 명령(우선순위 100) ────────────────────────────────
		{
			name:     "[내장] 재귀 강제 삭제 rm -rf",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\brm\b.{0,80}(?:-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|--recursive|--no-preserve-root)`,
			action:   "deny",
			message:  "반복적 강제 삭제(rm -rf / rm --recursive)는 시스템이나 대상 환경에 영구적인 손상을 줄 수 있는 금지입니다.",
			priority: 100,
		},
		{
			name:     "[내장] 시스템 키 디렉토리 삭제",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\brm\b[^"'\n]{0,60}["'\s](/|/etc|/bin|/usr|/boot|/var|/lib|/sys|/proc|/dev|/sbin|/root)`,
			action:   "deny",
			message:  "시스템 중요 경로 삭제 금지",
			priority: 100,
		},
		{
			name:     "[내장] 디스크 포맷 mkfs",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bmkfs\b`,
			action:   "deny",
			message:  "디스크 포맷 비활성화(mkfs)",
			priority: 100,
		},
		{
			name:     "[내장] 디스크 장치 dd 덮어쓰기",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bdd\b[^|\n]{0,100}\bof=\s*/dev/[a-zA-Z]`,
			action:   "deny",
			message:  "dd를 사용하여 디스크 장치 덮어쓰기 비활성화",
			priority: 100,
		},
		{
			name:     "[내장] 포크 폭탄",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `:\(\)\s*\{[^}]*:\|:`,
			action:   "deny",
			message:  "포크 폭탄은 금지됩니다",
			priority: 100,
		},
		{
			name:     "[내장] 종료/다시 시작",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\b(?:shutdown|reboot|halt|poweroff|init\s+[06])\b`,
			action:   "deny",
			message:  "종료 또는 재시작 명령 실행 금지",
			priority: 100,
		},
		{
			name:     "[내장] 모든 프로세스 종료",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bkill\s+-9\s+-1\b|\bkillall\s+-9\b`,
			action:   "deny",
			message:  "kill -9 -1 또는 killall -9 비활성화(모든 프로세스 종료)",
			priority: 100,
		},
		{
			name:     "[내장] 디스크 와이프 파쇄/와이프",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\b(?:shred|wipe)\b[^|\n]{0,80}/dev/[a-zA-Z]`,
			action:   "deny",
			message:  "디스크 장치 파쇄/삭제 비활성화",
			priority: 100,
		},
		{
			name:     "[내장] 방화벽 규칙 지우기",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\biptables\s+(?:-F|--flush)\b|\bnft\s+flush\s+ruleset\b`,
			action:   "deny",
			message:  "방화벽 규칙 지우기 비활성화(iptables -F / nft 플러시)",
			priority: 100,
		},
		// ── 파괴적인 데이터베이스 작업(우선순위 90) ───────────────────────────────
		{
			name:     "[내장] SQL DROP DATABASE / TABLE / SCHEMA",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bDROP\s+(?:DATABASE|TABLE|SCHEMA|INDEX|VIEW|TABLESPACE|USER|ROLE)\b`,
			action:   "deny",
			message:  "DROP 작업은 금지되어 있으며 데이터베이스 개체를 되돌릴 수 없게 파괴할 수 있습니다",
			priority: 90,
		},
		{
			name:     "[내장] SQL TRUNCATE",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bTRUNCATE\s+(?:TABLE\s+)?\w`,
			action:   "deny",
			message:  "데이터 테이블의 모든 데이터가 삭제될 수 있는 TRUNCATE 실행은 금지되어 있습니다.",
			priority: 90,
		},
		{
			name:     "[내장] MongoDB 드롭/dropDatabase",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\.(?:dropDatabase|dropCollection|drop)\s*\(`,
			action:   "deny",
			message:  "MongoDB 삭제 작업 비활성화",
			priority: 90,
		},
		{
			name:     "[내장] Redis FLUSHALL / FLUSHDB",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\b(?:FLUSHALL|FLUSHDB)\b`,
			action:   "deny",
			message:  "Redis FLUSHALL / FLUSHDB 실행을 금지하고 캐시된 데이터를 모두 지울 수 있습니다",
			priority: 90,
		},
		// ── HTTP 파괴 요청 (우선순위 80) ────────────────────────────────
		// 에이전트가 DELETE 요청을 보내는 일반적인 방법은 세 가지가 있습니다.
		//   1. 컬 -X DELETE / --request DELETE (Bash 도구는 스크립트를 직접 실행하거나 작성합니다)
		//2. Python HTTP 클라이언트 .delete() 메소드
		//   3. JS/일반 스크립트의 메소드: 'DELETE' / method="DELETE"
		{
			name:     "[내장] 컬/wget이 DELETE 요청을 보냅니다",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bcurl\b[^|\n&;"]{0,300}(?:-X\s*DELETE|--request\s+DELETE|-XDELETE)|\bwget\b[^|\n&;"]{0,300}--method[=\s]+DELETE`,
			action:   "deny",
			message:  "curl/wget을 통해 HTTP DELETE 요청을 보내는 것은 금지되어 있으며, 이로 인해 대상 시스템 데이터가 삭제될 수 있습니다.",
			priority: 80,
		},
		{
			name:     "[내장] Python HTTP 클라이언트 DELETE(requests/httpx/aiohttp)",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\b(?:requests|httpx|aiohttp|urllib\.request)\.delete\s*\(|session\.delete\s*\(|client\.delete\s*\(`,
			action:   "deny",
			message:  "Python HTTP 클라이언트를 사용하여 DELETE 요청 비활성화",
			priority: 80,
		},
		{
			name:     "[내장] 스크립트에 HTTP DELETE 메소드 선언(JS/Universal)",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)axios\.delete\s*\(|method\s*[:=]\s*['"]DELETE['"]`,
			action:   "deny",
			message:  "스크립트에서 HTTP DELETE 요청 선언 및 전송 비활성화",
			priority: 80,
		},
		{
			name:     "[내장] 일괄 지우기/지우기 인터페이스 경로",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)/(?:clear|wipe|flush|purge|truncate|drop|destroy|factory[-_]reset|reset[-_]all)(?:[/?#"'\s]|$)`,
			action:   "deny",
			message:  "클래스 인터페이스 일괄 삭제 또는 삭제(/clear /wipe /flush /purge 등)를 호출하는 것은 금지되어 있습니다.",
			priority: 80,
		},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, true, $2, $3, $4, $5, $6, $7, false, 60, 'deny')
ON CONFLICT DO NOTHING`,
			r.name, r.priority, r.target, r.typ, r.pattern, r.action, r.message,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v1", "done")
}

// seedDefaultInterceptRulesV2 migrates the two safety patterns that used to be
// hard-coded in guard.go (destructive shell + data-exfil pipe) into ordinary
// intercept rules. Gated by its own flag so it also lands on DBs that already ran
// v1. 이전 Guard.go 플로어와 달리 이는 단순한 [내장] 규칙입니다. — 사용자는 다음을 수행할 수 있습니다.
// disable or delete them. The exfil rule ships DISABLED by default (its
// curl/wget/nc pipe pattern mis-fires on legitimate CTF/pentest reverse-shell and
// data-transfer pipes); enable it manually when exfil gating is actually wanted.
func (d *DB) seedDefaultInterceptRulesV2() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v2"); v == "done" {
		return nil
	}
	rules := []struct {
		name     string
		pattern  string
		action   string
		message  string
		enabled  bool
		priority int
	}{
		{
			name:     "[내장] 파괴적인 시스템 명령",
			pattern:  `(?i)\b(rm\s+-rf\s+/|mkfs|dd\s+if=|:\(\)\s*\{|shutdown|reboot|>\s*/dev/sd)`,
			action:   "deny",
			message:  "파괴적인 명령이 거부되었습니다(rm -rf / / mkfs / dd / 포크 폭탄 / 종료 및 다시 시작 / 디스크 장치 덮어쓰기)",
			enabled:  true,
			priority: 100,
		},
		{
			name:     "[내장] 데이터 유출 파이프라인",
			pattern:  `(?i)(curl|wget|nc|ncat)\b[^|]*\b(\|\s*(curl|wget|nc))`,
			action:   "deny",
			message:  "의심되는 데이터 유출 파이프라인은 거부됩니다(명령어 출력은 컬/wget/nc를 통해 전송됨)",
			enabled:  false,
			priority: 80,
		},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, $2, $3, 'tool_input', 'regex', $4, $5, $6, false, 60, 'deny')
ON CONFLICT DO NOTHING`,
			r.name, r.enabled, r.priority, r.pattern, r.action, r.message,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v2", "done")
}
