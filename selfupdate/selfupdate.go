// Package selfupdate hiện thực tính năng cập nhật một chạm trên trang của ARTEX:
// tải về binary mới từ GitHub Release, kiểm tra, tạm lưu, và hoán đổi nguyên tử
// ở lần khởi động sau.
//
// Phân chia tổng thể (xem start.sh / start.bat):
//
//	Script khởi động  = vòng daemon đơn giản, chỉ chịu trách nhiệm "sau khi tiến trình thoát, quyết định có khởi chạy lại hay không theo mã thoát"
//	Package này       = toàn bộ logic dễ sai (tải xuống / kiểm tra SHA256 / smoke test / hoán đổi / rollback khi thất bại)
//
// Lý do đặt việc hoán đổi trong Go thay vì trong script: kiểm tra sha256 và smoke test
// trên sh và bat phải viết hai bộ (sha256sum / shasum / certutil), mà đây lại đúng là
// khâu không được phép sai nhất — thay một binary không chạy được, daemon sẽ trung
// thành khởi chạy lại nó liên tục, người dùng chỉ còn cách lên máy cứu thủ công.
//
// Một lần nâng cấp hoàn chỉnh trải qua ba lần khởi động tiến trình:
//
//	① Server cũ nhận /api/update/apply → tải về kiểm tra → tạm lưu artex.new → exit 75
//	② Script khởi chạy lại bản cũ → Bootstrap phát hiện artex.new → kiểm tra + smoke test → hoán đổi → exit 75
//	③ Script khởi chạy lại, lúc này đã là bản mới → Bootstrap ghi một lần thử → sau khi khởi động thành công thì xóa file đánh dấu
//
// Bất kỳ bước nào thất bại đều quay về bản cũ: ② kiểm tra không qua thì xóa bản tạm
// lưu và chạy tiếp bản cũ; ③ liên tiếp 3 lần không sống sót tới lúc xóa file đánh dấu
// (không khởi động nổi là crash) thì tự động đổi artex.old lại.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart là mã thoát "xin daemon khởi chạy lại tôi" (EX_TEMPFAIL). Script khởi
// động thấy nó sẽ chạy lại ngay, không tính vào backoff khi crash. 0 nghĩa là người
// dùng dừng bình thường (script thoát vòng lặp), các giá trị còn lại đều coi là crash.
const ExitRestart = 75

// maxAttempts là số lần thử khởi động được phép sau khi hoán đổi. Mỗi lần khởi động
// bản mới đều cộng bộ đếm +1, sống sót qua settleDelay thì xóa file đánh dấu; crash
// liên tiếp maxAttempts lần nghĩa là bản mới không khởi động nổi, tự động rollback.
const maxAttempts = 3

// Paths là tất cả file liên quan tới một lần nâng cấp, thống nhất đặt dưới **thư mục
// chứa file thực thi**. Cố ý không dùng CWD: khi chạy dạng dịch vụ, thư mục làm việc
// có thể là / hoặc đường dẫn bất kỳ, dùng CWD sẽ khiến bản tạm lưu rơi sang chỗ khác,
// logic hoán đổi mất tác dụng ngay.
type Paths struct {
	Dir     string // thư mục chứa file thực thi
	Current string // binary đang chạy             artex      / artex.exe
	New     string // bản mới tạm lưu              artex.new  / artex.new.exe
	Sum     string // sha256 (hex) của bản mới     artex.new.sha256 / artex.new.exe.sha256
	Old     string // bản cũ sao lưu trước khi hoán đổi  artex.old  / artex.old.exe
	Marker  string // file đánh dấu trạng thái nâng cấp  artex.upgrade.json
}

// ResolvePaths suy ra toàn bộ đường dẫn nâng cấp từ file thực thi hiện tại.
//
// Trên Windows, .new/.old cũng phải mang hậu tố .exe, nếu không smoke test và việc
// thực thi sau khi hoán đổi đều thất bại, nên phải gỡ hậu tố ra rồi mới ghép, cách đặt
// tên của hai nền tảng mới đối xứng.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("định vị file thực thi: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // trên Windows là ".exe", trên Unix thường rỗng
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker ghi lại tiến độ của một lần hoán đổi, dùng để kích hoạt rollback tự động
// khi bản mới không khởi động nổi.
type marker struct {
	From     string `json:"from"`     // phiên bản trước khi nâng cấp
	To       string `json:"to"`       // phiên bản đích
	Attempts int    `json:"attempts"` // số lần đã thử khởi động sau khi hoán đổi
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged dọn bản tạm lưu. Hoán đổi thành công, kiểm tra thất bại, người dùng
// hủy đều đi qua nó, tránh artex.new còn sót lại bị thử lại ở lần khởi động sau.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions so sánh hai số phiên bản, trả về -1/0/1 (a<b / a==b / a>b).
// ok=false nghĩa là ít nhất một bên không phải số phiên bản so sánh được (ví dụ bản
// build phát triển cục bộ "dev" hay "0.3.7-2-gabc1234-dirty" do git describe sinh ra),
// lúc này bên gọi nên tắt cập nhật một chạm, nếu không sẽ "nâng cấp" bản build đang
// phát triển thành bản chính thức, ghi đè mất các thay đổi chưa commit.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion phân tích số phiên bản dạng "v0.3.7" / "0.3.7" thành [3]int.
//
// Chỉ chấp nhận dạng ba đoạn thuần túy: build.sh dùng git describe cho bản build
// không phải tag, sinh ra các phiên bản có hậu tố kiểu "0.3.7-2-gabc1234", chúng phải
// bị phán là không so sánh được, chứ không được coi là 0.3.7 — nếu không bản build
// phát triển sẽ bị phán nhầm là "đã mới nhất" hoặc bị bản chính thức ghi đè.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker báo cáo tiến trình có đang chạy trong container không. Dưới Docker, việc
// hoán đổi ghi vào lớp ghi của container, `docker compose up -d` dựng lại container
// sẽ quay về phiên bản đi kèm image — đây là hành vi mong đợi (lúc đó người dùng vốn
// đang kéo image mới), nhưng frontend cần dựa vào đây để nói rõ ràng.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
