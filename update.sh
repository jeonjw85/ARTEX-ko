#!/usr/bin/env bash
# ARTEX 업데이트 스크립트: ① Docker 소스 이미지 재빌드 ② 로컬 컴파일 업데이트(바이너리 재구축)
# install.sh에 해당: install는 첫 번째 구현을 담당하고 update는 새 버전으로 업그레이드를 담당합니다.
# DB 마이그레이션은 수동으로 수행할 필요가 없습니다. - artex는 시작될 때마다 멱등적으로 다시 실행됩니다. schema.sql(ADD COLUMN/CREATE 포함)
# INDEX IF NOT EXISTS), "다시 시작하고 마이그레이션"합니다. 데이터(pgdata 볼륨, ./data, ./skills)는 영향을 받지 않습니다.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── 선택 사항: 웨어하우스를 최신 코드로 동기화합니다(compose/ 스크립트/로컬 컴파일된 소스 코드가 업데이트됩니다)───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "git가 아닌 작업 복사본, git pull 건너뛰기"; return; }
  [ "$(ask '최신 코드(git pull --ff-only)를 가져오시겠습니까? (y/n)' y)" = y ] || return 0
  if ! git pull --ff-only; then
    warn "git pull 빨리 감기에 실패했습니다(로컬 변경 사항 또는 분기 분기). 수동으로 처리하고 다시 시도하십시오. 이번에는 현재 코드가 사용됩니다."
  fi
}

# ── ① Docker 업데이트 ──────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "docker / docker compose가 감지되지 않습니다. 먼저 ./install.sh를 사용하여 설치 및 배포하십시오."
  [ -f .env ] || die ".env를 찾을 수 없습니다. 첫 번째 배포를 완료하려면 먼저 ./install.sh를 실행하세요."

  # 현재 소스로 artex만 재빌드합니다. 실행 중인 PostgreSQL과 데이터 볼륨은 유지됩니다.
  info "현재 소스에서 한글판 이미지를 다시 빌드하고 시작합니다(시작 시 schema가 자동으로 마이그레이션됩니다)…"
  docker compose up -d --build artex
  ok "업데이트 완료 → http://localhost:8787"
  info "로그 보기: docker compose logs -f artex"
  info "오래된 이미지 정리(선택 사항): docker image prune -f"
}

# ── ② 로컬 컴파일 업데이트 ─────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go가 감지되지 않음(>=1.26): https://go.dev/dl/"
  [ -f config.json ] || warn "config.json를 찾을 수 없습니다. 처음 배포하는 경우 대신 ./install.sh를 사용하세요."
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "프런트엔드 정적 제품 재구축..."
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "포함된 단일 바이너리를 다시 컴파일합니다..."
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm가 감지되지 않음: 내장된 프런트 엔드 없이 백엔드를 컴파일합니다(프런트 엔드는 별도로 실행해야 함 npm run dev)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "컴파일 완료 → ./artex"
  warn "적용하려면 실행 중인 artex 프로세스를 다시 시작하십시오. (다시 시작하면 schema가 자동으로 마이그레이션됩니다.)"
}

echo "=============================="
echo "  ARTEX 업데이트"
echo "  1) Docker 업데이트(한글판 소스 이미지 재빌드)"
echo "  2) 로컬 업데이트(go 재컴파일)"
echo "=============================="
case "$(ask '선택' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "잘못된 선택" ;;
esac
