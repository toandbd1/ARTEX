#!/usr/bin/env bash
# =============================================================================
# Script đặt lại mật khẩu quản trị viên ARTEX
#
# Tên đăng nhập cố định là ARTEX; mật khẩu được lưu dưới dạng hash bcrypt trong
# key auth.password_hash của bảng settings trong cơ sở dữ liệu. Script này kết nối
# tới cơ sở dữ liệu, dùng pgcrypto tạo hash bcrypt ngay trong DB và ghi lại key đó —
# hoàn toàn tương thích với kiểm tra đăng nhập của backend (golang.org/x/crypto/bcrypt).
#
# Hai kiểu triển khai:
#   local (mặc định) — máy chủ kết nối trực tiếp bằng psql. Thông tin kết nối lấy theo
#                      thứ tự ưu tiên: tham số dòng lệnh > --dsn/$ARTEX_PG_DSN > database.* trong config.json
#   docker           — thực thi psql trong container postgres qua `docker compose exec`
#                      (hoặc `docker exec`) (compose mặc định không expose cổng 5432 ra host, nên phải vào trong container).
#
# Ví dụ sử dụng:
#   ./reset-password.sh                          # cục bộ, tự đọc config.json/môi trường, nhập mật khẩu mới tương tác
#   ./reset-password.sh -p 'NewPass!'            # cục bộ, cung cấp trực tiếp mật khẩu mới
#   ./reset-password.sh --dsn postgres://u:p@h:5432/artex
#   ./reset-password.sh -H 127.0.0.1 -P 5433 -U autopentest -W pass -d artex
#   ./reset-password.sh -m docker                # triển khai docker (đọc POSTGRES_* trong .env)
#   ./reset-password.sh -m docker -c ten_container_pg --exec docker
#
# Bảo mật: mật khẩu mới được truyền qua biến môi trường + psql \getenv (không lọt vào argv
# của tiến trình), và được tự động escape qua :'var' (chống SQL injection); mật khẩu cơ sở dữ
# liệu truyền qua PGPASSWORD, cũng không lọt vào argv.
# =============================================================================
set -euo pipefail

PASS_KEY="auth.password_hash"
BCRYPT_COST=10

MODE=""            # local | docker (trống=tự động xác định)
DSN=""
HOST="" PORT="" USER="" DBPASS="" DBNAME="" SSLMODE=""
CONFIG=""
CONTAINER=""       # tên service/container postgres ở chế độ docker (mặc định postgres)
EXEC_KIND=""       # compose | docker (chế độ docker dùng loại exec nào; trống=tự động)
NEWPASS=""
ASSUME_YES=0

die() { echo "Lỗi: $*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# ---- Phân tích tham số -------------------------------------------------------------
while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--mode)        MODE="${2:-}"; shift 2 ;;
    --dsn)            DSN="${2:-}"; shift 2 ;;
    -H|--host)        HOST="${2:-}"; shift 2 ;;
    -P|--port)        PORT="${2:-}"; shift 2 ;;
    -U|--user)        USER="${2:-}"; shift 2 ;;
    -W|--db-password) DBPASS="${2:-}"; shift 2 ;;
    -d|--dbname)      DBNAME="${2:-}"; shift 2 ;;
    --sslmode)        SSLMODE="${2:-}"; shift 2 ;;
    --config)         CONFIG="${2:-}"; shift 2 ;;
    -c|--container)   CONTAINER="${2:-}"; shift 2 ;;
    --exec)           EXEC_KIND="${2:-}"; shift 2 ;;
    -p|--new-password) NEWPASS="${2:-}"; shift 2 ;;
    -y|--yes)         ASSUME_YES=1; shift ;;
    -h|--help)        usage ;;
    *) die "Tham số không xác định: $1 (-h để xem cách dùng)" ;;
  esac
done

# ---- Đọc database.* từ config.json (chỉ ở chế độ local và khi chưa cung cấp kết nối) -----
# Ưu tiên dùng python3 để parse (chắc chắn); thiếu python3 thì quay về grep (config.json có trường quy chuẩn).
read_config_json() {
  local path="$1"
  [[ -f "$path" ]] || return 1
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1])).get("database", {})
except Exception:
    sys.exit(1)
# Hỗ trợ cung cấp trực tiếp dsn, hoặc từng trường riêng
if d.get("dsn"):
    print("DSN\t" + d["dsn"]); sys.exit(0)
for k in ("host","port","user","password","dbname","sslmode"):
    if d.get(k) is not None:
        print(k.upper() + "\t" + str(d[k]))
PY
  else
    # Phương án dự phòng tối giản: grep từng key (giá trị là chuỗi hoặc số)
    local k
    for k in host port user password dbname sslmode; do
      local v
      v=$(grep -oE "\"$k\"[[:space:]]*:[[:space:]]*(\"[^\"]*\"|[0-9]+)" "$path" 2>/dev/null \
            | head -1 | sed -E "s/.*:[[:space:]]*//; s/^\"//; s/\"$//") || true
      [[ -n "$v" ]] && echo -e "${k^^}\t$v"
    done
  fi
}

apply_config_fields() {
  local line key val
  while IFS=$'\t' read -r key val; do
    [[ -z "$key" ]] && continue
    case "$key" in
      DSN)      [[ -z "$DSN" ]] && DSN="$val" ;;
      HOST)     [[ -z "$HOST" ]] && HOST="$val" ;;
      PORT)     [[ -z "$PORT" ]] && PORT="$val" ;;
      USER)     [[ -z "$USER" ]] && USER="$val" ;;
      PASSWORD) [[ -z "$DBPASS" ]] && DBPASS="$val" ;;
      DBNAME)   [[ -z "$DBNAME" ]] && DBNAME="$val" ;;
      SSLMODE)  [[ -z "$SSLMODE" ]] && SSLMODE="$val" ;;
    esac
  done
}

# ---- Tự động xác định chế độ ---------------------------------------------------------
if [[ -z "$MODE" ]]; then
  if [[ -n "$DSN$HOST$USER$DBNAME" || -n "${ARTEX_PG_DSN:-}" || -f "${CONFIG:-config.json}" ]]; then
    MODE="local"
  elif command -v docker >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
    MODE="docker"
  else
    MODE="local"
  fi
fi
info "Chế độ triển khai: $MODE"

# ---- Thu thập mật khẩu mới -----------------------------------------------------------
if [[ -z "$NEWPASS" ]]; then
  read -r -s -p "Nhập mật khẩu mới (tên người dùng cố định là ARTEX): " NEWPASS; echo >&2
  [[ -n "$NEWPASS" ]] || die "Mật khẩu không được để trống"
  read -r -s -p "Nhập lại để xác nhận: " NEWPASS2; echo >&2
  [[ "$NEWPASS" == "$NEWPASS2" ]] || die "Hai lần nhập không khớp"
fi
[[ -n "$NEWPASS" ]] || die "Mật khẩu không được để trống"

# Đưa mật khẩu cho psql qua biến môi trường (đọc bằng \getenv, không lọt vào argv/ps)
export ARTEX_RESET_NEWPASS="$NEWPASS"

# Tạo bcrypt trong DB và upsert; mật khẩu được tự escape qua :'newpw'. CREATE EXTENSION
# là lũy đẳng, nếu role cơ sở dữ liệu không có quyền tạo extension sẽ báo lỗi tại đây
# (gợi ý xem nhánh thất bại của phần run bên dưới).
SQL=$(cat <<SQL
\\set ON_ERROR_STOP on
\\getenv newpw ARTEX_RESET_NEWPASS
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO settings(key, value)
VALUES ('$PASS_KEY', crypt(:'newpw', gen_salt('bf', $BCRYPT_COST)))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
)

# ---- Thực thi -----------------------------------------------------------------
if [[ "$MODE" == "local" ]]; then
  # Thứ tự ưu tiên thông tin kết nối: dòng lệnh > --dsn/$ARTEX_PG_DSN > config.json
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    [[ -n "${ARTEX_PG_DSN:-}" ]] && DSN="$ARTEX_PG_DSN"
  fi
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    cfg="${CONFIG:-config.json}"
    if [[ -f "$cfg" ]]; then
      info "Đọc cấu hình cơ sở dữ liệu từ $cfg"
      apply_config_fields < <(read_config_json "$cfg")
    fi
  fi

  command -v psql >/dev/null 2>&1 || die "Không tìm thấy psql trên máy này (vui lòng cài postgresql-client, hoặc đổi sang -m docker)"

  declare -a PSQL_ARGS=()
  if [[ -n "$DSN" ]]; then
    PSQL_ARGS=("$DSN")
    target="$DSN"
  else
    [[ -n "$USER"   ]] || die "Thiếu người dùng cơ sở dữ liệu (-U) hoặc config.json/DSN hợp lệ"
    [[ -n "$DBNAME" ]] || die "Thiếu tên cơ sở dữ liệu (-d) hoặc config.json/DSN hợp lệ"
    HOST="${HOST:-127.0.0.1}"; PORT="${PORT:-5432}"; SSLMODE="${SSLMODE:-disable}"
    PSQL_ARGS=(-h "$HOST" -p "$PORT" -U "$USER" -d "$DBNAME")
    [[ -n "$SSLMODE" ]] && export PGSSLMODE="$SSLMODE"
    [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
    target="$USER@$HOST:$PORT/$DBNAME"
  fi

  info "Cơ sở dữ liệu đích: $target"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "Xác nhận đặt lại mật khẩu ARTEX trong cơ sở dữ liệu này? [y/N] " ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "Đã hủy"
  fi

  if ! printf '%s\n' "$SQL" | psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 -q >/dev/null; then
    die "Ghi thất bại. Nếu báo lỗi quyền/thiếu pgcrypto, hãy dùng role có quyền tạo extension, hoặc chạy thủ công CREATE EXTENSION pgcrypto trước."
  fi

else
  # ---- docker ----
  command -v docker >/dev/null 2>&1 || die "Không tìm thấy docker"
  CONTAINER="${CONTAINER:-postgres}"

  # Chọn cách exec: ưu tiên docker compose exec (tên service), nếu không thì docker exec (tên container)
  if [[ -z "$EXEC_KIND" ]]; then
    if docker compose version >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
      EXEC_KIND="compose"
    else
      EXEC_KIND="docker"
    fi
  fi

  # Thông tin xác thực psql trong container: ưu tiên dòng lệnh, sau đó POSTGRES_* trong .env, cuối cùng về mặc định compose (artex)
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; . ./.env; set +a
  fi
  DUSER="${USER:-${POSTGRES_USER:-artex}}"
  DNAME="${DBNAME:-${POSTGRES_DB:-artex}}"
  [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
  [[ -z "${PGPASSWORD:-}" && -n "${POSTGRES_PASSWORD:-}" ]] && export PGPASSWORD="$POSTGRES_PASSWORD"

  info "Đích: psql trong container $CONTAINER -U $DUSER -d $DNAME (exec=$EXEC_KIND)"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "Xác nhận đặt lại mật khẩu ARTEX trong cơ sở dữ liệu của container này? [y/N] " ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "Đã hủy"
  fi

  # -e chỉ mang tên không mang giá trị -> kế thừa từ môi trường hiện tại, mật khẩu không xuất hiện trong argv của lệnh docker.
  declare -a EXEC_CMD
  if [[ "$EXEC_KIND" == "compose" ]]; then
    EXEC_CMD=(docker compose exec -T -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  else
    EXEC_CMD=(docker exec -i -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  fi

  if ! printf '%s\n' "$SQL" | "${EXEC_CMD[@]}" >/dev/null; then
    die "Ghi thất bại. Vui lòng kiểm tra tên container (-c), tài khoản cơ sở dữ liệu (POSTGRES_* trong .env), và quyền pgcrypto của role."
  fi
fi

unset ARTEX_RESET_NEWPASS
echo "✓ Đã đặt lại mật khẩu quản trị viên ARTEX. Hãy đăng nhập bằng tên người dùng ARTEX + mật khẩu mới (không cần khởi động lại dịch vụ)."
