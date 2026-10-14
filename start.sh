#!/bin/sh
# Script khởi động daemon ARTEX (Linux / macOS / Docker ENTRYPOINT)
#
# Cách dùng:
#   ./start.sh                       Chạy foreground (Ctrl-C để dừng)
#   nohup ./start.sh >artex.log 2>&1 &   Chạy thường trú ở nền
#   ./start.sh -addr :9000           Các tham số bổ sung được truyền nguyên văn cho artex
#
# Nó chỉ làm một việc: chạy artex, sau khi tiến trình thoát sẽ dựa vào mã thoát để quyết định có khởi động lại không.
#
#   0      Người dùng dừng bình thường   -> Thoát vòng lặp
#   75     Chương trình yêu cầu khởi động lại -> Chạy lại ngay (đã bấm "cập nhật một chạm" hoặc "khôi phục (rollback)" trên giao diện)
#   Khác   Crash               -> Chạy lại sau thời gian backoff (1->2->4... tối đa 60 giây)
#
# Cố ý không thực hiện tải xuống, kiểm tra SHA256 hay thay binary tại đây: những logic đó
# phải viết hai bản cho sh và bat, mà chúng lại chính là khâu không được phép sai sót — một khi
# thay bằng binary không chạy được, script này sẽ trung thành khởi động lại nó liên tục, người dùng
# chỉ còn cách lên máy xử lý thủ công. Vì vậy việc kiểm tra/thay binary đều để trong Go (gói selfupdate),
# do chính artex hoàn tất lúc khởi động, script giữ nguyên đơn giản.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] Không tìm thấy file thực thi $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Chuyển tiếp tín hiệu dừng cho tiến trình artex.
#
# Trong Docker điều này là bắt buộc: docker stop chỉ gửi SIGTERM cho PID 1 (tức script này),
# không gửi cho tiến trình con. Nếu không chuyển tiếp, artex sẽ không nhận được tín hiệu, không thể
# tắt êm, sau 10 giây bị SIGKILL giết cứng, các tác vụ đang chạy bị đứt giữa chừng.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# Tín hiệu sẽ ngắt wait và khiến nó trả về >128. Lúc này tiến trình con thực ra vẫn đang tắt êm,
	# phải wait thêm một lần nữa mới lấy được mã thoát thật của nó.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] Đã dừng"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] Thoát bình thường"
			exit 0
			;;
		"$RESTART_CODE")
			# Cập nhật/khôi phục đã sẵn sàng: sau khi chạy lại artex sẽ hoàn tất việc thay binary lúc khởi động (xem selfupdate.Bootstrap).
			echo "[artex] Yêu cầu khởi động lại (áp dụng phiên bản mới)..."
			delay=1
			;;
		*)
			echo "[artex] Thoát bất thường (code=$code), khởi động lại sau ${delay}s" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
