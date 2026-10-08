# syntax=docker/dockerfile:1
#
# 기본 release 타깃은 CI가 준비한 dist/<TARGETARCH>/artex를 사용합니다.
# local 타깃은 현재 소스의 프런트엔드와 Go 백엔드를 이미지 안에서 빌드합니다.
#   docker compose up -d --build
#   docker build --target local -t artex-ko:local .
FROM python:3.12-slim-bookworm AS runtime
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
# Guardian 시작 스크립트: 프로세스가 종료된 후 종료 코드를 눌러 다시 시작할지 여부를 결정하고 이에 따라 페이지의 원클릭 업데이트를 완료합니다.。
# 또한 책임이 있습니다 SIGTERM 앞으로 artex —— docker stop 다음으로만 신호를 보냅니다. PID 1，
# 전달하지 않으면 artex 정상적으로 수신하고 닫을 수 없습니다.，10 몇 초 후 SIGKILL 하드 킬。
COPY start.sh /app/start.sh
RUN chmod +x /app/start.sh
COPY skills/ /app/skills/
# data/（SQLite + jwt.key）지속성 지점
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]

FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm HUSKY=0 npm ci
COPY web/ ./
RUN NEXT_TELEMETRY_DISABLED=1 npm run build:static

FROM --platform=$BUILDPLATFORM golang:1.26.3-bookworm AS backend
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=frontend /src/web/out/ ./server/webui/dist/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags embedui -trimpath -ldflags "-s -w" -o /out/artex ./cmd/artex

FROM runtime AS local
COPY --from=backend /out/artex /app/artex
RUN chmod +x /app/artex

# 마지막 타깃을 release로 유지하여 기존 CI 빌드 방식을 보존합니다.
FROM runtime AS release
ARG TARGETARCH
COPY dist/${TARGETARCH}/artex /app/artex
RUN chmod +x /app/artex
