#!/usr/bin/env bash
# Script cài đặt ARTEX: ① Hoàn toàn bằng Docker  ② Biên dịch và chạy cục bộ
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

# ── Kiểm tra môi trường docker / tự động cài đặt ───────────────────
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "Đã phát hiện docker và docker compose"; return
  fi
  warn "Không phát hiện docker / docker compose"
  case "$(uname -s)" in
    Linux)
      if [ "$(ask 'Tự động cài đặt Docker? (y/n)' y)" = y ]; then
        curl -fsSL https://get.docker.com | sh
        sudo usermod -aG docker "$USER" || true
        ok "Đã cài đặt Docker xong (thay đổi nhóm người dùng cần đăng nhập lại để không cần sudo)"
      else
        die "Vui lòng tự cài đặt docker rồi thử lại"
      fi ;;
    Darwin) die "Trên macOS vui lòng cài Docker Desktop: https://www.docker.com/products/docker-desktop/" ;;
    *)      die "Vui lòng tự cài đặt docker rồi thử lại" ;;
  esac
}

# ── ① Hoàn toàn bằng Docker ───────────────────────────────
install_docker(){
  ensure_docker
  if [ ! -f .env ]; then
    cp .env.example .env 2>/dev/null || true
    local pw key
    pw="$(ask 'Mật khẩu Postgres (Enter để tạo ngẫu nhiên)' "$(rand)")"
    key="$(ask 'ANTHROPIC_API_KEY (có thể để trống, cấu hình sau trong UI)' '')"
    sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pw}|" .env
    sed -i.bak "s|^ANTHROPIC_API_KEY=.*|ANTHROPIC_API_KEY=${key}|" .env
    rm -f .env.bak
    ok "Đã tạo .env (POSTGRES_PASSWORD đã được đặt)"
  else
    info "Tiếp tục dùng .env hiện có"
  fi
  info "Kéo image và khởi động..."
  docker compose pull || true
  docker compose up -d
  ok "Khởi động xong -> http://localhost:8787"
  info "Xem nhật ký: docker compose logs -f artex"
}

# ── ② Biên dịch và chạy cục bộ ──────────────────────────────
install_local(){
  echo "Cách cài đặt cơ sở dữ liệu:"
  echo "  1) Kết nối PostgreSQL đã có"
  echo "  2) Dùng Docker để chạy một PostgreSQL (cần docker)"
  case "$(ask 'Chọn' 1)" in
    2)
      ensure_docker
      local pw; pw="$(ask 'Mật khẩu Postgres (Enter để tạo ngẫu nhiên)' "$(rand)")"
      docker run -d --name artex-pg -p 5432:5432 \
        -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
        -v artex-pg:/var/lib/postgresql/data postgres:16-alpine
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable ;;
    *)
      DB_HOST="$(ask 'Địa chỉ cơ sở dữ liệu' 127.0.0.1)"
      DB_PORT="$(ask 'Cổng' 5432)"
      DB_USER="$(ask 'Tài khoản' artex)"
      DB_PASS="$(ask 'Mật khẩu' '')"
      DB_NAME="$(ask 'Tên cơ sở dữ liệu' artex)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # Tạo config.json
  cat > config.json <<JSON
{
  "database": {
    "host": "${DB_HOST}",
    "port": ${DB_PORT},
    "user": "${DB_USER}",
    "password": "${DB_PASS}",
    "dbname": "${DB_NAME}",
    "sslmode": "${DB_SSL}"
  }
}
JSON
  ok "Đã tạo config.json"

  # Kiểm tra môi trường go
  command -v go >/dev/null 2>&1 || die "Không phát hiện Go, vui lòng cài Go trước (>=1.26): https://go.dev/dl/"
  ok "Go: $(go version)"

  # Frontend nhúng cần node để tạo sản phẩm tĩnh
  if command -v npm >/dev/null 2>&1; then
    info "Đang build sản phẩm tĩnh của frontend..."
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "Đang biên dịch binary đơn nhúng frontend..."
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "Không phát hiện npm: sẽ biên dịch backend **không nhúng frontend** (frontend cần chạy riêng npm run dev)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "Biên dịch xong -> ./artex"

  info "Đang khởi động... (Ctrl-C để thoát)"
  ./artex
}

echo "=============================="
echo "  Cài đặt ARTEX"
echo "  1) Cài đặt hoàn toàn bằng Docker"
echo "  2) Chạy cục bộ (biên dịch bằng go)"
echo "=============================="
case "$(ask 'Chọn' 1)" in
  1) install_docker ;;
  2) install_local ;;
  *) die "Lựa chọn không hợp lệ" ;;
esac
