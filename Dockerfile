# syntax=docker/dockerfile:1
#
# 실행 이미지는 도구와 미리 빌드한 Linux 바이너리만 담습니다.
# CI에서 각 아키텍처용 바이너리를 dist/<TARGETARCH>/artex에 준비합니다.
# 이미지 빌드 중에는 Next.js나 Go를 다시 컴파일하지 않습니다.
#
# 로컬에서 수동으로 이미지를 빌드하는 경우 먼저 바이너리를 직접 준비하세요.
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# 자주 사용하는 네트워크 점검 도구를 설치합니다.
# Node는 NodeSource에서 20.x를 설치합니다. bookworm과 함께 제공되는 apt nodejs는 18이고 Playwright에는 20 이상이 필요합니다.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Playwright MCP, CLI, Chromium과 시스템 의존성을 미리 설치합니다.
# 컨테이너에서 브라우저 도구를 처음 사용할 때도 별도 다운로드가 필요하지 않습니다.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# 현재 아키텍처용 바이너리를 복사합니다.
COPY dist/${TARGETARCH}/artex /app/artex
# 시작 스크립트는 종료 코드에 따라 ARTEX를 재시작하고 SIGTERM을 전달합니다.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# 실행 중 생성되는 데이터의 영구 저장 위치
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
