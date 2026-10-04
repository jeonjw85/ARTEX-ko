#!/usr/bin/env bash
# 개발 모드: 백엔드(:8787) + 프런트엔드 next dev(:5173)로 실행되는 트래픽 프록시(:8788).
# 프런트엔드 /api가 백엔드로 전송됩니다. Ctrl-C가 함께 종료됩니다.
#
# 단일 바이너리(프런트 엔드 임베디드) 방법에 대해서는 README의 "단일 바이너리" 섹션을 참조하세요. 이 스크립트를 사용하지 마십시오.
set -euo pipefail
cd "$(dirname "$0")"

# 종료할 때 이 프로세스 그룹(백엔드 + 프런트엔드)의 모든 하위 프로세스를 종료합니다.
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# 백엔드(일반 go run, 프런트엔드 내장 없음); 동시 work agent 수는 "시스템 설정"에서 구성됩니다.
go run ./cmd/artex -addr :8787 -proxy 127.0.0.1:8788 &

# 프런트 엔드 핫 업데이트(Vite/Next dev server, /api는 8787로 반전됨).
( cd web && npm run dev ) &

echo "[dev] 백엔드:8787 / 에이전트:8788 / 프론트엔드 http://localhost:5173 (Ctrl-C 종료)"
wait
