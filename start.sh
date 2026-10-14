#!/bin/sh
# ARTEX 실행 및 재시작 스크립트(Linux, macOS, Docker)
# 사용법: ./start.sh [-addr :9000 등 ARTEX 옵션]
# 백그라운드 실행: nohup ./start.sh >artex.log 2>&1 &
# 종료 코드 0이면 종료하고, 75이면 업데이트 또는 롤백을 적용하기 위해
# 즉시 재시작합니다. 그 밖의 종료 코드에는 최대 60초까지 대기하며 재시도합니다.
# 바이너리 다운로드와 검증은 ARTEX의 selfupdate 패키지가 처리합니다.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] 실행 파일 ${BIN}을 찾을 수 없습니다" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Docker 등의 환경에서 받은 종료 신호를 ARTEX 프로세스에 전달합니다.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# 신호로 wait가 중단되면 자식의 실제 종료 코드를 다시 받습니다.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] 중지됨"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] 정상 종료"
			exit 0
			;;
		"$RESTART_CODE")
			# 다음 실행 시 업데이트 또는 롤백을 적용합니다.
			echo "[artex] 재시작 요청 중(새 버전 적용)…"
			delay=1
			;;
		*)
			echo "[artex] 비정상 종료(code=$code). ${delay}초 후 다시 시작합니다." >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
