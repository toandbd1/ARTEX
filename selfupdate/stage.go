package selfupdate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// sumsAsset là danh sách checksum do release.yml sinh ra, bao phủ toàn bộ zip trong Release.
const sumsAsset = "SHA256SUMS"

// maxBinarySize giới hạn dung lượng binary sau khi giải nén, chống zip dị dạng ghi đầy đĩa.
const maxBinarySize = 512 << 20 // 512 MiB

// Phase là một giai đoạn trong quá trình nâng cấp, dùng trực tiếp làm trường phase
// trong sự kiện SSE.
type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseDownload Phase = "downloading"
	PhaseVerify   Phase = "verifying"
	PhaseExtract  Phase = "extracting"
	PhaseStaged   Phase = "staged"
	PhaseFailed   Phase = "failed"
)

// Progress do bên gọi cung cấp, dùng để đẩy tiến độ về frontend. pct chỉ có ý nghĩa
// ở giai đoạn tải xuống (0-100), các giai đoạn khác truyền -1.
type Progress func(ph Phase, pct int, msg string)

// Stage tải gói phát hành cho nền tảng hiện tại của Release chỉ định, sau khi kiểm
// tra thì tạm lưu binary mới thành artex.new.
//
// Dùng zip nguyên gói thay vì binary trần, có hai lý do: SHA256SUMS của các Release
// hiện có vốn chỉ bao phủ zip, đi theo zip không cần sửa CI, cũng tương thích với các
// phiên bản lịch sử đã phát hành; trong zip còn kèm skills/, chừa sẵn cửa cho việc
// đồng bộ skill tích hợp về sau. Cái giá chỉ là tải thêm vài trăm KB của skills.
//
// Hàm trả về nghĩa là tạm lưu đã xong, bên gọi sau đó tắt êm và thoát với ExitRestart.
func Stage(ctx context.Context, c *http.Client, rel *Release, currentVersion string, prog Progress) error {
	if prog == nil {
		prog = func(Phase, int, string) {}
	}
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if err := checkWritable(p.Dir); err != nil {
		return err
	}

	name := AssetName(rel.TagName, runtime.GOOS, runtime.GOARCH)
	asset, ok := rel.FindAsset(name)
	if !ok {
		return fmt.Errorf("phiên bản này không cung cấp gói phát hành cho %s/%s (thiếu %s)", runtime.GOOS, runtime.GOARCH, name)
	}

	prog(PhaseDownload, 0, "Đang lấy danh sách checksum…")
	sums, err := fetchSums(ctx, c, rel)
	if err != nil {
		return err
	}
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("%s không chứa %s, từ chối cài đặt binary chưa được kiểm tra", sumsAsset, name)
	}

	// Mọi file tạm đều nằm trong thư mục đích, đảm bảo rename cuối cùng là thao tác
	// nguyên tử trong cùng một hệ thống file (rename chéo thiết bị sẽ thất bại, mà
	// /tmp thường là mount point riêng).
	zipPath := p.New + ".zip.part"
	binPath := p.New + ".part"
	defer func() {
		_ = os.Remove(zipPath)
		_ = os.Remove(binPath)
	}()

	prog(PhaseDownload, 0, fmt.Sprintf("Đang tải xuống %s (%s)…", name, humanSize(asset.Size)))
	got, err := download(ctx, c, asset, zipPath, prog)
	if err != nil {
		return err
	}

	prog(PhaseVerify, -1, "Đang kiểm tra SHA256…")
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA256 không khớp: mong đợi %s, thực tế %s (tải về bị hỏng hoặc bị sửa đổi)", short(want), short(got))
	}

	prog(PhaseExtract, -1, "Đang giải nén và kiểm thử khói…")
	if err := extractBinary(zipPath, binPath); err != nil {
		return err
	}
	if err := smokeTest(binPath); err != nil {
		return fmt.Errorf("phiên bản mới không chạy được trên hệ thống hiện tại: %w", err)
	}

	// sha256 của chính bản tạm lưu được lưu riêng một bản: trước khi hoán đổi ở lần
	// khởi động sau còn phải kiểm tra lại một lần nữa, chống việc file bị sửa hoặc
	// ghi hỏng trong khoảng từ lúc tạm lưu tới lúc khởi động lại.
	binSum, err := fileSHA256(binPath)
	if err != nil {
		return fmt.Errorf("tính checksum của binary mới: %w", err)
	}
	if err := os.WriteFile(p.Sum, []byte(binSum), 0o644); err != nil {
		return fmt.Errorf("ghi checksum: %w", err)
	}
	if err := os.Rename(binPath, p.New); err != nil {
		_ = os.Remove(p.Sum)
		return fmt.Errorf("tạm lưu bản mới: %w", err)
	}

	if err := writeMarker(p.Marker, marker{
		From:     currentVersion,
		To:       strings.TrimPrefix(rel.TagName, "v"),
		StagedAt: time.Now().Unix(),
	}); err != nil {
		// File đánh dấu chỉ ảnh hưởng khả năng rollback tự động, bản tạm lưu đã sẵn
		// sàng, không vì thế mà ngắt nâng cấp.
		prog(PhaseStaged, -1, "Cảnh báo: ghi file đánh dấu nâng cấp thất bại, lần nâng cấp này sẽ không có bảo vệ rollback tự động")
	}

	prog(PhaseStaged, 100, "Phiên bản mới đã sẵn sàng, đang khởi động lại…")
	return nil
}

// fetchSums tải xuống và phân tích SHA256SUMS, trả về tên file → digest hex.
func fetchSums(ctx context.Context, c *http.Client, rel *Release) (map[string]string, error) {
	asset, ok := rel.FindAsset(sumsAsset)
	if !ok {
		return nil, fmt.Errorf("Release này không có %s, không kiểm tra được tính toàn vẹn, từ chối nâng cấp", sumsAsset)
	}
	body, err := get(ctx, c, asset.downloadURL())
	if err != nil {
		return nil, fmt.Errorf("tải xuống %s: %w", sumsAsset, err)
	}
	defer body.Close()

	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("đọc %s: %w", sumsAsset, err)
	}
	out := parseSums(string(raw))
	if len(out) == 0 {
		return nil, fmt.Errorf("%s rỗng hoặc định dạng không nhận diện được", sumsAsset)
	}
	return out, nil
}

// parseSums phân tích danh sách kiểu sha256sum, trả về tên file → digest hex.
//
// Trường đầu tiên phải là hex 64 ký tự mới được nhận. Chỉ phán đoán theo "đúng hai
// trường" là không đủ — bất kỳ dòng chữ giải thích hai từ nào cũng sẽ bị coi là mục
// hợp lệ, nhét giá trị rác vào bảng digest, asset thật ngược lại có thể khớp nhầm digest.
func parseSums(raw string) map[string]string {
	out := map[string]string{}
	for line := range strings.Lines(raw) {
		// Định dạng là "<sha256>  <filename>" (sha256sum dùng hai dấu cách; chế độ
		// binary của shasum thêm tiền tố * vào tên file).
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || !isHexSHA256(fields[0]) {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == "" {
			continue
		}
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// download ghi asset vào dst, đồng thời tính SHA256 và báo tiến độ theo Content-Length.
func download(ctx context.Context, c *http.Client, a Asset, dst string, prog Progress) (string, error) {
	body, err := get(ctx, c, a.downloadURL())
	if err != nil {
		return "", fmt.Errorf("tải xuống %s: %w", a.Name, err)
	}
	defer body.Close()

	f, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("tạo file tạm: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	pw := &progressWriter{total: a.Size, prog: prog, name: a.Name, last: time.Now()}
	if _, err := io.Copy(io.MultiWriter(f, h, pw), body); err != nil {
		return "", fmt.Errorf("tải xuống bị gián đoạn: %w", err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("ghi xuống đĩa thất bại: %w", err)
	}
	if a.Size > 0 && pw.written != a.Size {
		return "", fmt.Errorf("tải xuống không đầy đủ: mong đợi %d byte, thực tế %d byte", a.Size, pw.written)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// get phát một GET chịu ràng buộc danh sách trắng, trả về thân phản hồi.
func get(ctx context.Context, c *http.Client, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-selfupdate")
	req.Header.Set("Accept", "application/octet-stream")
	setUpdateAuth(req)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// extractBinary lấy file thực thi artex ra từ gói phát hành.
//
// Cấu trúc trong gói là artex-<phiên bản>-<os>-<arch>/artex, nhưng ở đây khớp theo
// **tên cơ sở** thay vì ghép đường dẫn đầy đủ: số phiên bản đã xuất hiện một lần trong
// tên gói, ghép sai một ký tự là cả lần nâng cấp thất bại, tìm theo tên cơ sở bền hơn
// trước thay đổi.
func extractBinary(zipPath, dst string) error {
	want := "artex"
	if runtime.GOOS == "windows" {
		want = "artex.exe"
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("mở gói phát hành: %w", err)
	}
	defer zr.Close()

	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() || !strings.EqualFold(path.Base(entry.Name), want) {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return fmt.Errorf("đọc %s: %w", entry.Name, err)
		}
		defer rc.Close()

		f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return fmt.Errorf("ghi binary mới ra: %w", err)
		}
		defer f.Close()

		n, err := io.Copy(f, io.LimitReader(rc, maxBinarySize+1))
		if err != nil {
			return fmt.Errorf("giải nén %s: %w", entry.Name, err)
		}
		if n > maxBinarySize {
			return fmt.Errorf("file thực thi trong gói phát hành vượt quá %s, từ chối giải nén", humanSize(maxBinarySize))
		}
		if n == 0 {
			return fmt.Errorf("%s trong gói phát hành là file rỗng", want)
		}
		return f.Sync()
	}
	return fmt.Errorf("không tìm thấy %s trong gói phát hành", want)
}

// checkWritable xác nhận trước thư mục có thể ghi. Không có bước này, khi chạy không
// phải root, hoặc binary được đặt trong thư mục hệ thống, sẽ đến khoảnh khắc hoán đổi
// mới thất bại sau khi đã tải xuống vài chục MB.
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".artex-update-probe-*")
	if err != nil {
		return fmt.Errorf("thư mục chương trình %s không ghi được, không thể tự động cập nhật (hãy kiểm tra quyền hoặc chuyển sang nâng cấp thủ công): %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// progressWriter đếm số byte đã ghi và báo theo giới hạn tần suất, tránh mỗi khối
// 32KiB lại đẩy một sự kiện SSE.
type progressWriter struct {
	total   int64
	written int64
	name    string
	prog    Progress
	last    time.Time
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.written += int64(len(b))
	if time.Since(w.last) < 300*time.Millisecond {
		return len(b), nil
	}
	w.last = time.Now()
	pct := -1
	if w.total > 0 {
		pct = int(w.written * 100 / w.total)
	}
	w.prog(PhaseDownload, pct, fmt.Sprintf("Đang tải xuống %s / %s", humanSize(w.written), humanSize(w.total)))
	return len(b), nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12] + "…"
	}
	return sum
}
