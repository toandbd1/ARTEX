# syntax=docker/dockerfile:1
#
# Image chạy (không biên dịch trong image): chỉ cài công cụ thường dùng, đưa vào **binary Linux đơn đã biên dịch sẵn**.
# Binary được biên dịch chéo bởi job binaries của CI (Go thuần, không QEMU), đặt theo kiến trúc đích
# tại dist/<TARGETARCH>/artex trong build context. Như vậy khi build đa kiến trúc, arm64 chỉ cần mô phỏng tầng apt,
# không còn mô phỏng biên dịch Next/Go, nhanh hơn nhiều.
#
# Khi build image thủ công ở cục bộ, hãy tự chuẩn bị binary trước:
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# Công cụ thường dùng: ripgrep / curl / vim, thêm một số công cụ recon quen thuộc (tùy nhu cầu thêm bớt).
# Node cài bản 20.x từ NodeSource: apt nodejs có sẵn của bookworm là 18, Playwright yêu cầu >=20.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Cài sẵn Playwright MCP và CLI (toàn cục), khi chạy không cần npx tải về qua mạng nữa.
# @playwright/mcp: browser MCP chạy trực tiếp `npx @playwright/mcp` (đã cài toàn cục, không cần -y/@latest).
# @playwright/cli: cung cấp playwright-cli, cài xong kiểm tra bằng --help là chạy được.
# Tiếp theo cài playwright (cung cấp quản lý trình duyệt), cài xong dùng --with-deps để cài sẵn chromium và các
# dependency hệ thống, như vậy MCP/CLI trong container khởi động lần đầu là dùng được ngay, không phải tải trình duyệt qua mạng.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# Binary đã biên dịch sẵn theo kiến trúc tương ứng (dist/amd64/artex hoặc dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# Script khởi động daemon: sau khi tiến trình thoát, dựa vào mã thoát để quyết định có khởi động lại không; tính năng cập nhật một chạm trên giao diện dựa vào nó để thay binary.
# Nó cũng chịu trách nhiệm chuyển tiếp SIGTERM cho artex — docker stop chỉ gửi tín hiệu cho PID 1,
# nếu không chuyển tiếp thì artex không nhận được, không thể tắt êm, sau 10 giây bị SIGKILL giết cứng.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# Điểm lưu trữ bền vững cho data/ (SQLite + jwt.key)
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
