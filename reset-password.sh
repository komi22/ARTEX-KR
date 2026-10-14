#!/usr/bin/env bash
# =============================================================================
# ARTEX 관리자 비밀번호 재설정 스크립트
#
# 로그인 사용자 이름은 ARTEX로 고정되어 있습니다. 비밀번호는 bcrypt 해시와 함께 데이터베이스 설정 테이블에 저장됩니다
# auth.password_hash 키. 이 스크립트가 데이터베이스에 연결되면 pgcrypto를 사용하여 라이브러리에 bcrypt 해시를 생성하세요
# 키를 다시 작성하세요. 백엔드 로그인 확인(golang.org/x/crypto/bcrypt)과 완벽하게 호환됩니다.
#
# 두 가지 배포:
#   local(기본값) - 호스트가 psql을 직접 사용하여 데이터베이스에 연결합니다. 연결 정보는 다음 우선순위로 획득됩니다:
#                    명령줄 매개변수 > --dsn/$ARTEX_PG_DSN > config.json의 데이터베이스.*
#   docker - postgres의 `docker compose exec`(또는 `docker exec`)을 통해
#                    컨테이너에서 psql을 실행합니다(compose는 기본적으로 5432를 호스트에 노출시키지 않으므로 컨테이너에서 실행됩니다).
#
# 사용 예:
#   ./reset-password.sh # 로컬, config.json/environment 자동 읽기, 대화형으로 새 비밀번호 입력
#   ./reset-password.sh -p '새패스!' # 로컬, 새 비밀번호를 직접 입력
#   ./reset-password.sh --dsn postgres://u:p@h:5432/artex
#   ./reset-password.sh -H 127.0.0.1 -P 5433 -U autopentest -W pass -d artex
#   ./reset-password.sh -m docker # docker 배포(.env의 POSTGRES_* 읽기)
#   ./reset-password.sh -m docker -c pg 컨테이너 이름 --exec docker
#
# 보안: 새 비밀번호는 환경 변수 + psql \getenv(프로세스 argv를 입력하지 않음)를 통해 전달되며 :'var'을 사용합니다.
# 자동 이스케이프(SQL 삽입 방지); 데이터베이스 비밀번호는 PGPASSWORD를 통해 전달되며 argv에도 입력되지 않습니다.
# =============================================================================
set -euo pipefail

PASS_KEY="auth.password_hash"
BCRYPT_COST=10

MODE=""            # local | docker(비우면 자동 선택)
DSN=""
HOST="" PORT="" USER="" DBPASS="" DBNAME="" SSLMODE=""
CONFIG=""
CONTAINER=""       # Docker 모드의 PostgreSQL 서비스 또는 컨테이너 이름(기본값: postgres)
EXEC_KIND=""       # compose | docker(Docker 모드에서 사용할 exec, 비우면 자동 선택)
NEWPASS=""
ASSUME_YES=0

die() { echo "오류: $*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# ---- 매개변수 분석 ------------------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--mode)        MODE="${2:-}"; shift 2 ;;
    --dsn)            DSN="${2:-}"; shift 2 ;;
    -H|--host)        HOST="${2:-}"; shift 2 ;;
    -P|--port)        PORT="${2:-}"; shift 2 ;;
    -U|--user)        USER="${2:-}"; shift 2 ;;
    -W|--db-password) DBPASS="${2:-}"; shift 2 ;;
    -d|--dbname)      DBNAME="${2:-}"; shift 2 ;;
    --sslmode)        SSLMODE="${2:-}"; shift 2 ;;
    --config)         CONFIG="${2:-}"; shift 2 ;;
    -c|--container)   CONTAINER="${2:-}"; shift 2 ;;
    --exec)           EXEC_KIND="${2:-}"; shift 2 ;;
    -p|--new-password) NEWPASS="${2:-}"; shift 2 ;;
    -y|--yes)         ASSUME_YES=1; shift ;;
    -h|--help)        usage ;;
    *) die "알 수 없는 매개변수: $1(-h 보기 사용법)" ;;
  esac
done

# ---- config.json에서 데이터베이스.* 읽기(로컬 모드에서만, 명시적으로 연결이 제공되지 않은 경우) -----
# 파싱을 위해 python3 사용을 선호합니다(강력함). python3이 없으면 grep이 반환됩니다(config.json에는 일반 필드가 있음).
read_config_json() {
  local path="$1"
  [[ -f "$path" ]] || return 1
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1])).get("database", {})
except Exception:
    sys.exit(1)
# 직접 dsn 또는 하위 필드 지원
if d.get("dsn"):
    print("DSN\t" + d["dsn"]); sys.exit(0)
for k in ("host","port","user","password","dbname","sslmode"):
    if d.get(k) is not None:
        print(k.upper() + "\t" + str(d[k]))
PY
  else
    # 미니멀리스트 대체: 키별 grep(값은 문자열 또는 숫자)
    local k
    for k in host port user password dbname sslmode; do
      local v
      v=$(grep -oE "\"$k\"[[:space:]]*:[[:space:]]*(\"[^\"]*\"|[0-9]+)" "$path" 2>/dev/null \
            | head -1 | sed -E "s/.*:[[:space:]]*//; s/^\"//; s/\"$//") || true
      [[ -n "$v" ]] && echo -e "${k^^}\t$v"
    done
  fi
}

apply_config_fields() {
  local line key val
  while IFS=$'\t' read -r key val; do
    [[ -z "$key" ]] && continue
    case "$key" in
      DSN)      [[ -z "$DSN" ]] && DSN="$val" ;;
      HOST)     [[ -z "$HOST" ]] && HOST="$val" ;;
      PORT)     [[ -z "$PORT" ]] && PORT="$val" ;;
      USER)     [[ -z "$USER" ]] && USER="$val" ;;
      PASSWORD) [[ -z "$DBPASS" ]] && DBPASS="$val" ;;
      DBNAME)   [[ -z "$DBNAME" ]] && DBNAME="$val" ;;
      SSLMODE)  [[ -z "$SSLMODE" ]] && SSLMODE="$val" ;;
    esac
  done
}

# ---- 자동 결정 모드 --------------------------------------------------
if [[ -z "$MODE" ]]; then
  if [[ -n "$DSN$HOST$USER$DBNAME" || -n "${ARTEX_PG_DSN:-}" || -f "${CONFIG:-config.json}" ]]; then
    MODE="local"
  elif command -v docker >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
    MODE="docker"
  else
    MODE="local"
  fi
fi
info "배포 모드: $MODE"

# ----새 비밀번호 수집-----------------------------------------
if [[ -z "$NEWPASS" ]]; then
  read -r -s -p "새 비밀번호 입력(사용자 이름은 ARTEX로 고정됨):" NEWPASS; echo >&2
  [[ -n "$NEWPASS" ]] || die "비밀번호는 비워둘 수 없습니다"
  read -r -s -p "확인을 위해 다시 입력하세요:" NEWPASS2; echo >&2
  [[ "$NEWPASS" == "$NEWPASS2" ]] || die "두 입력이 일치하지 않습니다"
fi
[[ -n "$NEWPASS" ]] || die "비밀번호는 비워둘 수 없습니다"

# 환경 변수를 통해 psql에 비밀번호를 부여합니다(\getenv 읽기, argv/ps 입력 안 함)
export ARTEX_RESET_NEWPASS="$NEWPASS"

# Bcrypt가 라이브러리에 생성되고 upsert됩니다. 비밀번호는 :'newpw'를 사용하여 자동으로 이스케이프됩니다. 멱등성 확장 프로그램 생성,
# 데이터베이스 역할에 확장 권한이 없으면 여기에 오류가 보고됩니다(메시지는 아래 실행 실패 분기 참조).
SQL=$(cat <<SQL
\\set ON_ERROR_STOP on
\\getenv newpw ARTEX_RESET_NEWPASS
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO settings(key, value)
VALUES ('$PASS_KEY', crypt(:'newpw', gen_salt('bf', $BCRYPT_COST)))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
)

# ------실행 -----------------------------------------
if [[ "$MODE" == "local" ]]; then
  # 연결 정보 우선 순위: 명령줄 > --dsn/$ARTEX_PG_DSN > config.json
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    [[ -n "${ARTEX_PG_DSN:-}" ]] && DSN="$ARTEX_PG_DSN"
  fi
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    cfg="${CONFIG:-config.json}"
    if [[ -f "$cfg" ]]; then
      info "${cfg}에서 데이터베이스 구성 읽기"
      apply_config_fields < <(read_config_json "$cfg")
    fi
  fi

  command -v psql >/dev/null 2>&1 || die "이 시스템에서 psql을 찾을 수 없습니다(postgresql-client를 설치하거나 대신 -m docker를 사용하십시오)"

  declare -a PSQL_ARGS=()
  if [[ -n "$DSN" ]]; then
    PSQL_ARGS=("$DSN")
    target="$DSN"
  else
    [[ -n "$USER"   ]] || die "누락된 데이터베이스 사용자(-U) 또는 유효한 config.json/DSN"
    [[ -n "$DBNAME" ]] || die "데이터베이스 이름(-d) 또는 유효한 config.json/DSN이 누락되었습니다."
    HOST="${HOST:-127.0.0.1}"; PORT="${PORT:-5432}"; SSLMODE="${SSLMODE:-disable}"
    PSQL_ARGS=(-h "$HOST" -p "$PORT" -U "$USER" -d "$DBNAME")
    [[ -n "$SSLMODE" ]] && export PGSSLMODE="$SSLMODE"
    [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
    target="$USER@$HOST:$PORT/$DBNAME"
  fi

  info "대상 데이터베이스: $target"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "이 라이브러리에서 ARTEX 비밀번호를 재설정하시겠습니까? [예/아니요]" ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "취소됨"
  fi

  if ! printf '%s\n' "$SQL" | psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 -q >/dev/null; then
    die "쓰기에 실패했습니다. pgcrypto 권한/누락이 보고되면 확장 프로그램 생성 권한이 있는 역할을 사용하거나 먼저 CREATE EXTENSION pgcrypto를 수동으로 실행하세요."
  fi

else
  # ---- docker ----
  command -v docker >/dev/null 2>&1 || die "도커를 찾을 수 없습니다"
  CONTAINER="${CONTAINER:-postgres}"

  # exec 방법 선택: docker compose exec(서비스 이름)에 우선순위를 부여하고, 그렇지 않으면 docker exec(컨테이너 이름)
  if [[ -z "$EXEC_KIND" ]]; then
    if docker compose version >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
      EXEC_KIND="compose"
    else
      EXEC_KIND="docker"
    fi
  fi

  # 컨테이너의 psql 자격 증명: 먼저 명령줄, .env의 POSTGRES_*, 그런 다음 기본 구성(artex)으로 돌아갑니다.
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; . ./.env; set +a
  fi
  DUSER="${USER:-${POSTGRES_USER:-artex}}"
  DNAME="${DBNAME:-${POSTGRES_DB:-artex}}"
  [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
  [[ -z "${PGPASSWORD:-}" && -n "${POSTGRES_PASSWORD:-}" ]] && export PGPASSWORD="$POSTGRES_PASSWORD"

  info "대상: 컨테이너 $CONTAINER psql -U $DUSER -d $DNAME (exec=$EXEC_KIND)"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "이 컨테이너 데이터베이스에서 ARTEX 비밀번호를 재설정하시겠습니까? [예/아니요]" ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "취소됨"
  fi

  # -e는 값 없이 이름만 사용 → 현재 환경에서 상속됨, 비밀번호는 docker 명령 argv에 표시되지 않습니다.
  declare -a EXEC_CMD
  if [[ "$EXEC_KIND" == "compose" ]]; then
    EXEC_CMD=(docker compose exec -T -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  else
    EXEC_CMD=(docker exec -i -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  fi

  if ! printf '%s\n' "$SQL" | "${EXEC_CMD[@]}" >/dev/null; then
    die "쓰기에 실패했습니다. 컨테이너 이름(-c), 데이터베이스 계정(.env의 POTGRES_*) 및 역할에 pgcrypto 권한이 있는지 확인하세요."
  fi
fi

unset ARTEX_RESET_NEWPASS
echo "✓ ARTEX 관리자 비밀번호가 재설정되었습니다. 사용자 이름 ARTEX + 새 비밀번호로 로그인하세요(서비스를 다시 시작할 필요 없음)."
