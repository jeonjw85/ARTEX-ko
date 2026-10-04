#!/bin/sh
# ARTEX 데몬 시작 스크립트(Linux / macOS / Docker ENTRYPOINT)
#
# 용법:
#   ./start.sh는 포그라운드에서 실행됩니다(Ctrl-C 중지).
#   nohup ./start.sh >artex.log 2>&1 및 백그라운드 상주
#   ./start.sh -addr:9000 추가 파라미터는 그대로 artex로 투명하게 전송됩니다.
#
# 한 가지 작업만 수행합니다. artex를 실행합니다. 프로세스가 종료된 후 종료 코드를 눌러 다시 시작할지 여부를 결정하세요.
#
#   0 사용자 정상 정지 → 루프 종료
#   75 프로그램이 다시 시작 → 즉시 다시 실행을 요청합니다. (페이지에서 "원클릭 업데이트" 또는 "롤백" 클릭)
#   기타 충돌 → 후퇴 후 재실행 (1→2→4…최대 60초)
#
# 여기서는 의도적으로 다운로드, SHA256 확인 또는 재설치를 수행하지 않습니다. 해당 로직은 sh 및 bat에 두 세트로 작성되어야 합니다.
# 그리고 이는 정확하게 잘못될 수 없는 링크입니다. 일단 실행할 수 없는 바이너리가 교체되면 이 스크립트는 충실하게 작동합니다.
# 반복해서 끌어낸 후에는 사용자가 기계로 가서 수동으로 저장할 수만 있습니다. 따라서 캘리브레이션/교체는 모두 Go(selfupdate 패키지)에 남고,
# 시작 시 artex 자체에 의해 완료되는 스크립트는 완벽한 상태로 유지됩니다.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] 실행 파일 $BIN를 찾을 수 없습니다." >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# 정지 신호를 artex 본체로 전달합니다.
#
# 이는 Docker에서 필요합니다. docker stop는 SIGTERM를 PID 1(즉, 이 스크립트)에만 보냅니다.
# 하위 프로세스로 전송되지 않습니다. 전달되지 않으면 artex는 신호를 수신할 수 없으며 정상적으로 종료될 수 없습니다. 10초 후에 SIGKILL로 교체됩니다.
# 하드킬을 했고, 그가 진행하던 임무가 중간에 중단됐다.
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

	# 신호는 wait를 중단하고 >128을 반환하도록 합니다. 현재 하위 프로세스는 실제로 여전히 정상적으로 종료되고 있습니다.
	# 실제 종료 코드를 얻으려면 wait를 다시 실행해야 합니다.
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
			echo "[artex] 일반 종료"
			exit 0
			;;
		"$RESTART_CODE")
			# 업데이트/롤백 준비: artex는 재실행 후 시작 시 재로드를 완료합니다(selfupdate.Bootstrap 참조).
			echo "[artex] 재시작 요청 중(새 버전 적용)…"
			delay=1
			;;
		*)
			echo "[artex] 비정상 종료(code=$code), ${delay}s 이후 다시 시작" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
