package selfupdate

import (
	"archive/zip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testPaths dựng một thư mục nâng cấp cách ly. Không thể dùng trực tiếp
// ResolvePaths() — cái đó sẽ trỏ vào chính binary của test, chạy một phát là đổi tên
// file thực thi của go test luôn.
func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		Dir:     dir,
		Current: filepath.Join(dir, "artex"),
		New:     filepath.Join(dir, "artex.new"),
		Sum:     filepath.Join(dir, "artex.new.sha256"),
		Old:     filepath.Join(dir, "artex.old"),
		Marker:  filepath.Join(dir, "artex.upgrade.json"),
	}
}

// fakeBin ghi một shell script thực thi được giả làm artex. smokeTest chỉ dùng -h để
// khởi chạy nó và xem mã thoát, script hoàn toàn đủ dùng, và nhanh hơn nhiều so với
// biên dịch một binary thật.
func fakeBin(t *testing.T, path, marker string, exitCode int) {
	t.Helper()
	script := "#!/bin/sh\necho " + marker + "\nexit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("ghi binary giả %s: %v", path, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

// stage bố trí bin thành dáng vẻ "đã tạm lưu chờ hoán đổi": ghi sẵn artex.new và
// checksum của nó.
func stage(t *testing.T, p Paths, marker string, exitCode int) {
	t.Helper()
	fakeBin(t, p.New, marker, exitCode)
	sum, err := fileSHA256(p.New)
	if err != nil {
		t.Fatalf("tính checksum: %v", err)
	}
	if err := os.WriteFile(p.Sum, []byte(sum), 0o644); err != nil {
		t.Fatalf("ghi checksum: %v", err)
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("đọc %s: %v", path, err)
	}
	return string(b)
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("binary giả dùng script sh, chạy không được trên Windows")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b       string
		want       int
		comparable bool
	}{
		{"0.3.7", "0.3.8", -1, true},
		{"0.3.8", "0.3.7", 1, true},
		{"0.3.7", "0.3.7", 0, true},
		{"v0.3.7", "0.3.8", -1, true}, // build.sh bỏ v, tag có v, cả hai phía đều phải nhận
		{"0.3.7", "v0.3.7", 0, true},
		{"0.9.0", "0.10.0", -1, true}, // so theo số chứ không theo thứ tự từ điển
		{"1.0.0", "0.99.99", 1, true},
		// Bản build phát triển phải bị phán là không so sánh được, nếu không sẽ bị bản
		// chính thức ghi đè mất các thay đổi chưa commit.
		{"dev", "0.3.8", 0, false},
		{"0.3.7-2-gabc1234", "0.3.8", 0, false},
		{"0.3.7-dirty", "0.3.8", 0, false},
		{"0.3", "0.3.8", 0, false},
		{"", "0.3.8", 0, false},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if ok != c.comparable {
			t.Errorf("CompareVersions(%q,%q) comparable=%v, mong đợi %v", c.a, c.b, ok, c.comparable)
			continue
		}
		if ok && got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d, mong đợi %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolvePathsNaming(t *testing.T) {
	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	// Bất biến then chốt: mọi file nâng cấp đều cùng thư mục với file thực thi. Rơi
	// vào CWD sẽ khiến việc hoán đổi khi chạy dạng dịch vụ (thư mục làm việc có thể
	// là /) mất tác dụng hoàn toàn.
	for name, path := range map[string]string{"New": p.New, "Sum": p.Sum, "Old": p.Old, "Marker": p.Marker} {
		if filepath.Dir(path) != p.Dir {
			t.Errorf("%s không nằm trong thư mục file thực thi: %s (mong đợi %s)", name, path, p.Dir)
		}
	}
	// Trên Windows, .new/.old phải giữ .exe, nếu không smoke test và việc thực thi sau
	// khi hoán đổi đều thất bại.
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(p.New, ".exe") || !strings.HasSuffix(p.Old, ".exe") {
			t.Errorf("trên Windows .new/.old phải kết thúc bằng .exe: new=%s old=%s", p.New, p.Old)
		}
	}
}

func TestVerifyStagedRejectsTamperedBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "new", 0)

	// Ghi checksum xong rồi sửa file, mô phỏng tải về bị hỏng / bị tráo.
	fakeBin(t, p.New, "tampered", 0)
	if err := verifyStaged(p); err == nil {
		t.Fatal("mong đợi SHA256 không khớp bị từ chối, nhưng lại qua được")
	}
}

func TestVerifyStagedRejectsUnrunnableBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "broken", 1) // thực thi được nhưng mã thoát khác 0

	if err := verifyStaged(p); err == nil {
		t.Fatal("mong đợi smoke test thất bại bị từ chối, nhưng lại qua được")
	}
}

func TestApplyStagedHappyPath(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8"}); err != nil {
		t.Fatalf("ghi file đánh dấu: %v", err)
	}

	action, st := applyStaged(p)
	if action != Restart {
		t.Fatalf("mong đợi Restart, nhận được %v", action)
	}
	if !st.Pending {
		t.Error("sau khi hoán đổi, trạng thái phải là Pending")
	}
	if !strings.Contains(readAll(t, p.Current), "new") {
		t.Error("artex đáng lẽ đã được thay bằng bản mới")
	}
	if !strings.Contains(readAll(t, p.Old), "old") {
		t.Error("bản cũ đáng lẽ đã được sao lưu vào artex.old")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("sau khi hoán đổi, artex.new đáng lẽ đã biến mất")
	}
	if _, err := os.Stat(p.Sum); !os.IsNotExist(err) {
		t.Error("sau khi hoán đổi, file checksum đáng lẽ đã được dọn")
	}
	// File đánh dấu phải được giữ lại, lần khởi động sau (chạy bản mới) dựa vào nó để
	// đếm, rollback khi cần.
	if _, ok := readMarker(p.Marker); !ok {
		t.Error("sau khi hoán đổi, file đánh dấu nâng cấp phải được giữ lại")
	}
}

func TestApplyStagedKeepsCurrentWhenVerifyFails(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	fakeBin(t, p.New, "tampered", 0) // phá checksum

	action, st := applyStaged(p)
	if action != Continue {
		t.Fatalf("khi kiểm tra thất bại, mong đợi Continue, nhận được %v", action)
	}
	if !st.FailedStage {
		t.Error("trạng thái phải được đánh dấu FailedStage")
	}
	if !strings.Contains(readAll(t, p.Current), "old") {
		t.Fatal("khi kiểm tra thất bại, tuyệt đối không được động vào bản hiện tại")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("bản tạm lưu không qua kiểm tra phải bị dọn, nếu không lần khởi động sau sẽ thử lại")
	}
}

func TestSwapOverwritesPreviousBackup(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0) // bản sao lưu từ lần nâng cấp trước
	stage(t, p, "v3", 0)

	if err := swap(p); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if !strings.Contains(readAll(t, p.Current), "v3") {
		t.Error("đáng lẽ đã hoán đổi sang v3")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("bản sao lưu đáng lẽ đã cập nhật thành v2 vừa bị thay ra")
	}
}

func TestConfirmCountsAttemptsThenRollsBack(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "broken-new", 0)
	fakeBin(t, p.Old, "good-old", 0)
	m := marker{From: "0.3.7", To: "0.3.8"}

	// maxAttempts lần khởi động đầu chỉ cộng bộ đếm, cho bản mới cơ hội tự đứng vững.
	for i := 1; i <= maxAttempts; i++ {
		action, st := confirmOrRollback(p, m)
		if action != Continue {
			t.Fatalf("lần thử thứ %d mong đợi Continue, nhận được %v", i, action)
		}
		if !st.Pending {
			t.Errorf("lần thử thứ %d trạng thái phải là Pending", i)
		}
		got, ok := readMarker(p.Marker)
		if !ok || got.Attempts != i {
			t.Fatalf("sau lần thử thứ %d, attempts=%d (ok=%v), mong đợi %d", i, got.Attempts, ok, i)
		}
		m = got
	}

	// Crash thêm một lần nữa là vượt ngưỡng, tự động đổi lại bản cũ.
	action, st := confirmOrRollback(p, m)
	if action != Restart {
		t.Fatalf("khi vượt ngưỡng số lần thử, mong đợi Restart, nhận được %v", action)
	}
	if !st.RolledBack {
		t.Error("trạng thái phải được đánh dấu RolledBack")
	}
	if !strings.Contains(readAll(t, p.Current), "good-old") {
		t.Fatal("đáng lẽ đã rollback về bản cũ")
	}
	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Error("sau rollback, file đánh dấu phải bị xóa, nếu không sẽ rollback vô hạn")
	}
	// Bản không khởi động nổi được giữ lại để điều tra, không xóa thẳng.
	if _, err := os.Stat(p.Current + ".failed"); err != nil {
		t.Error("bản thất bại phải được giữ lại thành .failed để điều tra")
	}
}

func TestManualRollbackIsReversible(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0)

	// Rollback() đi qua ResolvePaths(), ở đây test trực tiếp ngữ nghĩa hoán đổi tầng dưới.
	tmp := p.Current + ".swap"
	if err := os.Rename(p.Current, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readAll(t, p.Current), "v1") {
		t.Error("sau rollback, bản hiện tại phải là v1")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("sau rollback, bản sao lưu phải trở thành v2, như vậy còn quay lại được nữa")
	}
}

func TestParseSums(t *testing.T) {
	const (
		linuxSum = "1111111111111111111111111111111111111111111111111111111111111111"
		winSum   = "ABCDEF0000000000000000000000000000000000000000000000000000000000"
	)
	// Đầu ra của sha256sum phân tách bằng hai dấu cách; shasum -a 256 ở chế độ binary
	// thêm * vào tên file.
	raw := linuxSum + "  artex-0.3.8-linux-amd64.zip\n" +
		winSum + " *artex-0.3.8-windows-amd64.zip\n" +
		"\n" +
		"garbage line\n" + // đúng hai trường, nhưng trường đầu không phải digest
		"deadbeef  artex-0.3.8-darwin-arm64.zip\n" // độ dài digest không đúng

	out := parseSums(raw)
	if out["artex-0.3.8-linux-amd64.zip"] != linuxSum {
		t.Errorf("phân tích mục linux sai: %v", out)
	}
	// Digest thống nhất chữ thường, khi đối chiếu mới không bị phán nhầm là không khớp
	// vì hoa/thường.
	if got := out["artex-0.3.8-windows-amd64.zip"]; got != strings.ToLower(winSum) {
		t.Errorf("mục windows sai (tiền tố * phải được gỡ, digest phải chuyển thành chữ thường): %q", got)
	}
	if len(out) != 2 {
		t.Errorf("phải bỏ qua dòng trống, dòng không phải digest và dòng sai độ dài, nhận được %v", out)
	}
}

func TestExtractBinaryFindsNestedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tên cơ sở trong gói trên Windows là artex.exe, ca test này dựng theo cách đặt tên Unix")
	}
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// Cấu trúc của gói phát hành thật: artex-<phiên bản>-<os>-<arch>/artex, kèm vài
	// file nhiễu.
	for name, body := range map[string]string{
		"artex-0.3.8-linux-amd64/README.md":           "readme",
		"artex-0.3.8-linux-amd64/skills/a.md":         "skill",
		"artex-0.3.8-linux-amd64/artex":               "#!/bin/sh\nexit 0\n",
		"artex-0.3.8-linux-amd64/config.example.json": "{}",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dst := filepath.Join(dir, "out")
	if err := extractBinary(zipPath, dst); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if got := readAll(t, dst); !strings.Contains(got, "exit 0") {
		t.Errorf("thứ giải nén ra không phải file thực thi artex: %q", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("binary giải nén ra phải mang bit thực thi")
	}
}

func TestExtractBinaryMissingEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("artex-0.3.8-linux-amd64/README.md")
	_, _ = w.Write([]byte("readme"))
	_ = zw.Close()
	f.Close()

	if err := extractBinary(zipPath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("khi trong gói không có file thực thi thì phải báo lỗi")
	}
}

func TestCheckURLRejectsNonGitHub(t *testing.T) {
	bad := []string{
		"http://github.com/x",           // không phải HTTPS
		"https://evil.com/artex.zip",    // tên miền không nằm trong danh sách trắng
		"https://github.com.evil.com/x", // giả mạo bằng hậu tố
		"https://raw.githubusercontent.com.evil.com/x",
	}
	for _, raw := range bad {
		u := mustParse(t, raw)
		if err := checkURL(u); err == nil {
			t.Errorf("checkURL(%q) đáng lẽ phải từ chối", raw)
		}
	}
	good := []string{
		"https://api.github.com/repos/x/releases/latest",
		"https://objects.githubusercontent.com/blah",
		"https://GitHub.com/x", // tên miền không phân biệt hoa thường
	}
	for _, raw := range good {
		u := mustParse(t, raw)
		if err := checkURL(u); err != nil {
			t.Errorf("checkURL(%q) đáng lẽ phải cho qua, nhưng lại báo lỗi: %v", raw, err)
		}
	}
}

func TestAssetNameMatchesBuildScript(t *testing.T) {
	// package_binary của build.sh dùng artex-<phiên bản>-<os>-<arch>.zip, và số phiên
	// bản đã bỏ tiền tố v. Ở đây sai một ký tự là cập nhật một chạm của mọi nền tảng
	// đều không tìm thấy asset.
	if got := AssetName("v0.3.8", "linux", "amd64"); got != "artex-0.3.8-linux-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
	if got := AssetName("0.3.8", "windows", "amd64"); got != "artex-0.3.8-windows-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("phân tích %q: %v", raw, err)
	}
	return u
}

func TestSettleClearsMarkerAndStopsRollback(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "new", 0)
	fakeBin(t, p.Old, "old", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8", Attempts: 2}); err != nil {
		t.Fatal(err)
	}

	settle(p)

	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Fatal("sau khi xác nhận ổn định, file đánh dấu nâng cấp phải bị xóa")
	}
	// File đánh dấu đã mất, các lần khởi động lại bình thường sau đó sẽ không cộng số
	// lần nữa, cũng không kích hoạt nhầm rollback.
	if _, ok := readMarker(p.Marker); ok {
		t.Error("đọc file đánh dấu đáng lẽ phải thất bại")
	}
	// Bản sao lưu phải giữ lại, người dùng vẫn có thể rollback thủ công.
	if _, err := os.Stat(p.Old); err != nil {
		t.Error("sau khi xác nhận ổn định vẫn phải giữ bản sao lưu trước")
	}
}

func TestSettleIsNoopWithoutMarker(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "cur", 0)
	settle(p) // đường khởi động bình thường, không được panic cũng không được động vào file nào
	if _, err := os.Stat(p.Current); err != nil {
		t.Error("khi không có file đánh dấu, settle không được ảnh hưởng bất kỳ file nào")
	}
}
