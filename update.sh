#!/usr/bin/env bash
# ARTEX 업데이트 스크립트: ① Docker 업데이트(새 이미지 가져오기 및 재구축) ② 로컬 컴파일 업데이트(바이너리 재구축)
# install.sh에 해당: 설치는 첫 번째 구현을 담당하고 업데이트는 새 버전으로 업그레이드를 담당합니다.
# DB 마이그레이션을 수동으로 수행할 필요가 없습니다. artex는 시작할 때마다 멱등적으로schema.sql(ADD COLUMN/CREATE
# INDEX IF NOT EXISTS), "다시 시작하고 마이그레이션"하세요. 데이터(pgdata 볼륨, ./data, ./skills)는 영향을 받지 않습니다.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── 선택 사항: 웨어하우스를 최신 코드와 동기화합니다(작성/스크립트/로컬로 컴파일된 소스 코드가 모두 업데이트됩니다)───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "git이 아닌 작업 복사본, git pull 건너뛰기"; return; }
  [ "$(ask '최신 코드를 가져올까요? (git pull --ff-only, y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull이 빨리 감기에 실패했습니다(로컬 변경 또는 분기 포크). 수동으로 처리하고 다시 시도하십시오. 이번에는 현재 코드가 사용됩니다."
  fi
}

# ── ① 도커 업데이트 ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "Docker/docker compose가 감지되지 않습니다. 먼저 ./install.sh를 사용하여 설치하고 배포하세요"
  [ -f .env ] || die ".env를 찾을 수 없습니다. 첫 번째 배포를 완료하려면 먼저 ./install.sh를 실행하세요"

  # 선택 사항: 지정된 버전 태그로 업그레이드(입력하지 않으면 .env의 ARTEX_TAG가 사용되며 기본값은 최신입니다)
  local tag; tag="$(ask '이미지 태그(Enter를 누르면 .env/latest 사용)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "ARTEX_TAG가 ${tag}(으)로 설정됨"
  fi

  # artex만 이동: postgres는 16-alpine으로 고정되어 있으며 업그레이드할 필요가 없습니다(끌어올리는 것은 대역폭 낭비입니다.
  # 그리고 주요 버전 변경에도 호환성 위험이 있습니다. artex는 dependency_on postgres를 선언하므로 서비스 이름을 가져오세요
  # PG가 실행 중에 시작되지 않으면 자동으로 당겨집니다. 이미 실행 중인 경우에는 그대로 유지되며 재구축되지 않습니다.
  info "새 이미지 가져오기(artex만 해당)…"
  docker compose pull artex
  info "재구축 및 시작(artex가 다시 시작되면 자동으로 스키마 마이그레이션)..."
  docker compose up -d artex
  ok "업데이트 완료 → http://localhost:8787"
  info "로그 보기: docker compose logs -f artex"
  info "오래된 이미지 정리(선택 사항): docker image prune -f"
}

# ── ② 로컬 컴파일 업데이트 ───────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go가 감지되지 않음(>=1.26): https://go.dev/dl/"
  [ -f config.json ] || warn "config.json을 찾을 수 없습니다. 처음 배포하는 경우 대신 ./install.sh를 사용하십시오."
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "프런트엔드 정적 제품 재구축..."
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "포함된 단일 바이너리 재컴파일..."
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm이 감지되지 않음: 프런트엔드를 포함하지 않고 백엔드를 컴파일합니다(프런트엔드는 별도로 npm run dev를 실행해야 함)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "편집 완료 → ./artex"
  warn "적용하려면 실행 중인 artex 프로세스를 다시 시작하세요(다시 시작하면 스키마가 자동으로 마이그레이션됩니다)"
}

echo "=============================="
echo "ARTEX 업데이트"
echo "1) Docker 업데이트(새 이미지를 가져와 다시 빌드)"
echo "2) 로컬 업데이트(재컴파일로 이동)"
echo "=============================="
case "$(ask '선택' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "잘못된 선택" ;;
esac
