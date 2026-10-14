#!/usr/bin/env bash
# Chế độ phát triển: chạy cùng lúc backend(:8787) + proxy lưu lượng(:8788) và frontend next dev(:5173).
# Frontend /api được proxy ngược tới backend; Ctrl-C để thoát tất cả.
#
# Cách binary đơn (frontend nhúng) xem mục "binary đơn" trong README, không dùng script này.
set -euo pipefail
cd "$(dirname "$0")"

# Khi thoát, kết thúc tất cả tiến trình con trong nhóm tiến trình này (backend + frontend).
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Backend (go run thông thường, không nhúng frontend); số work agent đồng thời cấu hình trong "Cài đặt hệ thống".
go run ./cmd/artex -addr :8787 -proxy 127.0.0.1:8788 &

# Frontend hot-reload (Vite/Next dev server, /api proxy ngược tới :8787).
( cd web && npm run dev ) &

echo "[dev] Backend :8787 / proxy :8788 / frontend http://localhost:5173  (Ctrl-C để thoát)"
wait
