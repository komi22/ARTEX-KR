@echo off
rem 한국어 메시지가 깨지지 않도록 콘솔을 UTF-8로 전환합니다.
chcp 65001 >nul 2>&1
rem ARTEX 감독 실행 스크립트(Windows)
rem
rem 사용법:
rem   start.bat                  포그라운드 실행(Ctrl-C로 종료)
rem   start.bat -addr :9000      추가 인수를 artex에 그대로 전달
rem
rem artex.exe를 실행하고 종료 코드에 따라 재시작 여부를 결정합니다.
rem
rem   0      사용자가 정상 종료 -> 반복 종료
rem   75     재시작 요청       -> 즉시 재실행(화면에서 "원클릭 업데이트" 또는 "롤백" 선택)
rem   기타   충돌              -> 잠시 대기 후 재실행(1->2->4…최대 60초)
rem
rem 다운로드, SHA256 확인, 바이너리 교체는 artex가 시작할 때 수행합니다.
rem 자세한 내용은 start.sh 상단 설명을 참고하세요.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] 실행 파일 %BIN%을(를) 찾을 수 없습니다 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] 일반 종료
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem 업데이트 또는 롤백이 준비됐습니다. 재실행 시 artex가 바이너리를 교체합니다.
	echo [artex] 재시작 요청 중(새 버전 적용)…
	set /a delay=1
	goto loop
)

echo [artex]가 비정상적으로 종료되었습니다 ^(code=!code!^), !delay!s 후에 다시 시작됨 1>&2
rem 리디렉션된 콘솔에서 timeout이 실패할 수 있어 ping으로 대기합니다(N초에는 N+1회 필요).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
