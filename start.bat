@echo off
rem 콘솔을 UTF-8로 전환하십시오. 그렇지 않으면 이 파일의 한국어가 GBK 터미널에서 깨질 것입니다.
chcp 65001 >nul 2>&1
rem ARTEX 데몬 시작 스크립트(Windows)
rem
rem 용법:
rem   start.bat가 포그라운드에서 실행됨(Ctrl-C가 중지됨)
rem   start.bat -addr:9000 추가 파라미터는 그대로 artex로 투명하게 전송됩니다.
rem
rem 한 가지 작업만 수행합니다. artex.exe를 실행합니다. 프로세스가 종료된 후 종료 코드를 눌러 다시 시작할지 여부를 결정하세요.
rem
rem   0 사용자가 정상적으로 중지 -> 루프 종료
rem   75 프로그램이 다시 시작 -> 즉시 다시 실행을 요청합니다(페이지에서 "원클릭 업데이트" 또는 "롤백" 클릭).
rem   기타 충돌 -> 후퇴 후 재실행 (1->2->4…최대 60초)
rem
rem 다운로드, SHA256 검증, 교체는 여기에 없으며 시작시 artex 자체에서 모두 완료됩니다.
rem (selfupdate 패키지). 스크립트는 멍청한 상태로 남아 있습니다. 자세한 내용은 start.sh 상단의 지침을 참조하세요.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] 실행 파일을 찾을 수 없습니다 %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] 정상적으로 종료
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem 업데이트/롤백 준비: artex는 재실행 후 시작 시 스왑을 완료합니다.
	echo [artex] 다시 시작 요청(새 버전 적용)）…
	set /a delay=1
	goto loop
)

echo [artex] 예기치 않게 종료 ^(code=!code!^)，!delay!s 이후에 다시 시작 1>&2
rem timeout는 리디렉션된 콘솔에서 실패합니다. ping를 대체 수단으로 사용하세요(N초를 지연하려면 N+1배가 필요함).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
