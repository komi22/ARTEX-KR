#!/usr/bin/env bash
# 개발 모드: 백엔드(:8787) + 트래픽 프록시(:8788) 및 프런트엔드 다음 개발(:5173)이 함께 실행됩니다.
# 프런트엔드/API가 백엔드로 전송됩니다. Ctrl-C가 함께 종료됩니다.
#
# 단일 바이너리(프런트엔드 내장) 방법에 대해서는 README의 "단일 바이너리" 섹션을 참조하세요. 이 스크립트를 사용하지 마십시오.
set -euo pipefail
cd "$(dirname "$0")"

# 종료할 때 이 프로세스 그룹(백엔드 + 프런트엔드)의 모든 하위 프로세스를 종료합니다.
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# 백엔드(정상 실행, 프런트엔드 내장 없음) 동시 작업 에이전트 수는 "시스템 설정"에서 구성됩니다.
go run ./cmd/artex -addr :8787 -proxy 127.0.0.1:8788 &

# 프런트엔드 핫 업데이트(Vite/Next 개발 서버, /api 역생성: 8787).
( cd web && npm run dev ) &

echo "[dev] 백엔드 :8787 / 대리인 :8788 / 프런트 엔드 http://localhost:5173  (Ctrl-C 종료)"
wait
