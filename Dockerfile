# syntax=docker/dockerfile:1
#
# 이미지 실행(이미지에서 컴파일하지 않음): 일반 도구만 설치하고**미리 컴파일된 Linux 단일 바이너리**。
# 바이너리 기준 CI ~의 binaries job 크로스 컴파일(순수 Go、없음 QEMU），대상 아키텍처에 따라 배치
# 맥락 구축 dist/<TARGETARCH>/artex。이러한 다중 아키텍처를 구축할 때 arm64 그냥 시뮬레이션해 보세요 apt 층，
# 더 이상 시뮬레이션이 필요하지 않습니다. Next/Go 훨씬 더 빠른 컴파일。
#
# 로컬에서 이미지를 수동으로 빌드하는 경우 먼저 바이너리를 직접 준비하세요.：
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# 일반적인 도구：ripgrep / curl / vim，배치 추가 recon 일반 예비 부품(필요에 따라 추가 또는 삭제)）。
# Node ~에서 NodeSource 팩 20.x：bookworm 함께 제공됩니다 apt nodejs 예 18，Playwright 필요하다 >=20。
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# 사전 설치됨 Playwright MCP 그리고 CLI（전역), 더 이상 런타임에 없음 npx 인터넷 다운로드。
# @playwright/mcp：browser MCP 직접 `npx @playwright/mcp`（이미 전역적으로 설치되어 있으므로 필요하지 않습니다. -y/@latest）。
# @playwright/cli：공급 playwright-cli，그런데 설치 후에는 --help 실행 파일 확인。
# 재설치 playwright（브라우저 관리 제공), 설치 후 사용 --with-deps 프리셋 chromium 및 시스템 종속성，
# 이 용기에는 MCP/CLI 처음 시작할 때 사용할 수 있으며 온라인으로 브라우저를 다운로드할 필요가 없습니다.。
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# 사전 컴파일된 해당 아키텍처 바이너리（dist/amd64/artex 또는 dist/arm64/artex）
COPY dist/${TARGETARCH}/artex /app/artex
# Guardian 시작 스크립트: 프로세스가 종료된 후 종료 코드를 눌러 다시 시작할지 여부를 결정하고 이에 따라 페이지의 원클릭 업데이트를 완료합니다.。
# 또한 책임이 있습니다 SIGTERM 앞으로 artex —— docker stop 다음으로만 신호를 보냅니다. PID 1，
# 전달하지 않으면 artex 정상적으로 수신하고 닫을 수 없습니다.，10 몇 초 후 SIGKILL 하드 킬。
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data/（SQLite + jwt.key）지속성 지점
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
