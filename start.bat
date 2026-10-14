@echo off
rem Chuyển console sang UTF-8, nếu không tiếng Việt trong file này sẽ bị lỗi hiển thị ở terminal GBK.
chcp 65001 >nul 2>&1
rem Script khởi động daemon ARTEX (Windows)
rem
rem Cách dùng:
rem   start.bat                  Chạy foreground (Ctrl-C để dừng)
rem   start.bat -addr :9000      Các tham số bổ sung được truyền nguyên văn cho artex
rem
rem Nó chỉ làm một việc: chạy artex.exe, sau khi tiến trình thoát sẽ dựa vào mã thoát để quyết định có khởi động lại không.
rem
rem   0      Người dùng dừng bình thường   -> Thoát vòng lặp
rem   75     Chương trình yêu cầu khởi động lại -> Chạy lại ngay (đã bấm "cập nhật một chạm" hoặc "khôi phục (rollback)" trên giao diện)
rem   Khác   Crash              -> Chạy lại sau thời gian backoff (1->2->4... tối đa 60 giây)
rem
rem Tải xuống, kiểm tra SHA256, thay binary đều không nằm ở đây, tất cả do chính artex
rem hoàn tất lúc khởi động (gói selfupdate). Script giữ nguyên đơn giản, chi tiết xem giải thích đầu start.sh.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] Không tìm thấy file thực thi %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] Thoát bình thường
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem Cập nhật/khôi phục đã sẵn sàng: sau khi chạy lại artex sẽ hoàn tất việc thay binary lúc khởi động.
	echo [artex] Yêu cầu khởi động lại (áp dụng phiên bản mới)...
	set /a delay=1
	goto loop
)

echo [artex] Thoát bất thường ^(code=!code!^), khởi động lại sau !delay!s 1>&2
rem timeout sẽ thất bại trong console bị redirect, dùng ping dự phòng (để trễ N giây cần N+1 lần).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
