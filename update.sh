#!/usr/bin/env bash
# Script cập nhật ARTEX: ① Cập nhật Docker (kéo image mới và rebuild)  ② Cập nhật bằng biên dịch cục bộ (rebuild binary)
# Tương ứng với install.sh: install lo lần triển khai đầu tiên, update lo việc nâng cấp lên phiên bản mới.
# Không cần chạy migration DB thủ công — artex mỗi lần khởi động đều chạy lại schema.sql một cách lũy đẳng
# (bao gồm ADD COLUMN/CREATE INDEX IF NOT EXISTS), nên "khởi động lại tức là migrate". Dữ liệu
# (volume pgdata, ./data, ./skills) không bị ảnh hưởng.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── Tùy chọn: đồng bộ repo về mã mới nhất (compose/script/mã nguồn biên dịch cục bộ đều nhờ nó cập nhật)───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "Không phải bản làm việc git, bỏ qua git pull"; return; }
  [ "$(ask 'Kéo mã mới nhất (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull không fast-forward được (có thay đổi cục bộ hoặc nhánh bị phân kỳ) — vui lòng xử lý thủ công rồi thử lại, lần này giữ nguyên mã hiện tại"
  fi
}

# ── ① Cập nhật Docker ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "Không phát hiện docker / docker compose, vui lòng chạy ./install.sh để cài đặt triển khai trước"
  [ -f .env ] || die "Không tìm thấy .env, vui lòng chạy ./install.sh để hoàn tất triển khai lần đầu"

  # Tùy chọn: nâng cấp lên tag phiên bản chỉ định (để trống thì dùng ARTEX_TAG trong .env, mặc định latest)
  local tag; tag="$(ask 'Tag image đích (Enter để dùng .env / latest)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "Đã đặt ARTEX_TAG thành ${tag}"
  fi

  # Chỉ động vào artex: postgres là 16-alpine cố định, không cần nâng cấp theo (kéo nó chỉ phí băng thông,
  # và thay đổi phiên bản lớn còn có rủi ro tương thích). artex khai báo depends_on postgres, nên khi
  # up kèm tên service, nếu pg chưa chạy sẽ tự khởi động; nếu đang chạy thì giữ nguyên, không rebuild.
  info "Kéo image mới (chỉ artex)..."
  docker compose pull artex
  info "Rebuild và khởi động (artex tự migrate schema khi khởi động lại)..."
  docker compose up -d artex
  ok "Cập nhật xong -> http://localhost:8787"
  info "Xem nhật ký: docker compose logs -f artex"
  info "Dọn image cũ (tùy chọn): docker image prune -f"
}

# ── ② Cập nhật bằng biên dịch cục bộ ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Không phát hiện Go (>=1.26): https://go.dev/dl/"
  [ -f config.json ] || warn "Không tìm thấy config.json — nếu là triển khai lần đầu hãy dùng ./install.sh"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "Rebuild sản phẩm tĩnh của frontend..."
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "Biên dịch lại binary đơn nhúng frontend..."
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "Không phát hiện npm: biên dịch backend **không nhúng frontend** (frontend cần chạy riêng npm run dev)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "Biên dịch xong -> ./artex"
  warn "Vui lòng khởi động lại tiến trình artex đang chạy để áp dụng (schema sẽ tự migrate khi khởi động lại)"
}

echo "=============================="
echo "  Cập nhật ARTEX"
echo "  1) Cập nhật Docker (kéo image mới và rebuild)"
echo "  2) Cập nhật cục bộ (biên dịch lại bằng go)"
echo "=============================="
case "$(ask 'Chọn' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "Lựa chọn không hợp lệ" ;;
esac
