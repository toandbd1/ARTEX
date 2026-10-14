package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv cho phép tiến trình con được kiểm thử khói (smoke test) khởi chạy bỏ qua
// thẳng Bootstrap.
//
// Nói đúng ra thì không thêm cũng không sao: os.Executable() của tiến trình con là
// artex.new, mọi đường dẫn suy ra đều mang tiền tố .new, không đụng tới file nâng cấp
// thật. Nhưng dựa vào sự trùng hợp đó quá mong manh, ngắt mạch tường minh rõ ràng hơn,
// cũng tiết kiệm cho tiến trình con một lần dò đĩa vô ích.
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action là chỉ thị Bootstrap gửi cho main.
type Action int

const (
	// Continue: khởi động server như thường.
	Continue Action = iota
	// Restart: thoát ngay với ExitRestart để script daemon khởi chạy lại.
	Restart
)

// State mô tả trạng thái nâng cấp tại lần khởi động này, để /api/update/check báo
// trung thực cho frontend "lần nâng cấp trước đã thành công hay đã bị khôi phục (rollback)".
type State struct {
	Pending     bool   // đã hoán đổi file nhưng chưa xác nhận ổn định
	RolledBack  bool   // lần khởi động này vừa thực hiện rollback tự động
	FailedStage bool   // bản tạm lưu không qua kiểm tra (xác minh)/smoke test, đã hủy
	Detail      string // một câu giải thích dành cho người dùng
}

// Bootstrap chạy ngay đầu main, phải được gọi trước khi lắng nghe cổng hay mở
// cơ sở dữ liệu bất kỳ.
//
// Ba tình huống:
//
//	① Có bản tạm lưu artex.new  → kiểm tra + smoke test, qua thì hoán đổi và yêu cầu khởi động lại; không qua thì hủy và chạy tiếp bản cũ
//	② Chỉ còn file đánh dấu      → nghĩa là vừa hoán đổi xong, cộng một lần thử; thất bại liên tiếp đủ nhiều lần thì rollback
//	③ Không có gì                → khởi động bình thường
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}
	p, err := ResolvePaths()
	if err != nil {
		log.Printf("[update] bỏ qua bootstrap: %v", err)
		return Continue, State{}
	}

	if _, err := os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok := readMarker(p.Marker)
	if !ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged xử lý tình huống "có bản tạm lưu": kiểm tra qua thì hoán đổi, thất bại thì hủy.
//
// Đây là nơi duy nhất trong toàn bộ chuỗi nâng cấp ghi đè file thực thi, cũng là cổng
// kiểm cuối cùng — smoke test chặn các vấn đề như tải về bị hỏng, chọn sai kiến trúc,
// thiếu liên kết động. Một khi thả qua một binary không chạy được, script daemon sẽ
// không mệt mỏi khởi chạy lại nó liên tục, còn mã Go thì không có cơ hội chạy, nói gì
// đến rollback tự động.
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[update] bản mới tạm lưu không qua kiểm tra, đã hủy, tiếp tục chạy bản hiện tại: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "Bản mới không qua kiểm tra, đã hủy: " + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[update] hoán đổi thất bại, tiếp tục chạy bản hiện tại: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "Hoán đổi thất bại: " + err.Error()}
	}

	// Hoán đổi thành công. Giữ lại file đánh dấu, để lần khởi động sau (chạy bản mới)
	// xác nhận có ổn định không.
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] ghi file đánh dấu nâng cấp thất bại (mất khả năng rollback tự động): %v", err)
	}
	log.Printf("[update] đã hoán đổi sang %s, thoát để khởi động lại (exit %d)", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback xử lý "khởi động sau khi hoán đổi": cộng số lần thử, vượt ngưỡng
// thì đổi lại bản cũ.
//
// Bộ đếm chỉ tăng sau khi mã Go chạy được, nên nó bao phủ loại lỗi "thực thi được
// nhưng crash khi khởi tạo" (cấu hình không tương thích, cổng bị chiếm, migration DB
// hỏng); "không exec được" đã bị smoke test trước khi hoán đổi chặn lại, hai cái cộng
// lại mới đầy đủ.
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			// Rollback cũng thất bại thì đừng khởi động lại nữa, nếu không sẽ rơi vào
			// vòng khởi động lại vô hạn. Xóa file đánh dấu, để tiến trình khởi động theo
			// trạng thái hiện tại — nếu không khởi động được thì người dùng ít nhất còn
			// thấy nguyên nhân trong nhật ký.
			log.Printf("[update] bản mới khởi động thất bại %d lần liên tiếp, và rollback cũng thất bại: %v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "Bản mới khởi động thất bại và rollback cũng thất bại: " + err.Error()}
		}
		log.Printf("[update] bản mới khởi động thất bại %d lần liên tiếp, đã rollback về %s, thoát để khởi động lại (exit %d)",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("Bản mới khởi động thất bại, đã rollback về %s", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] cập nhật file đánh dấu nâng cấp thất bại: %v", err)
	}
	log.Printf("[update] bản mới đang khởi động (lần thử %d/%d), sau khi chạy ổn định sẽ xác nhận nâng cấp",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle xác nhận bản mới đã chạy ổn định, xóa file đánh dấu nâng cấp.
//
// main gọi hàm này trễ một khoảng sau khi HTTP listener dựng lên: sống sót qua khoảng
// thời gian đó mới được tính, nếu không file đánh dấu nằm yên tại chỗ, lần khởi động
// sau tiếp tục cộng số lần thử, cho đến khi kích hoạt rollback.
func Settle() {
	p, err := ResolvePaths()
	if err != nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return // không phải khởi động sau nâng cấp, không có việc gì
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] xóa file đánh dấu nâng cấp thất bại: %v", err)
		return
	}
	log.Printf("[update] bản mới chạy ổn định, nâng cấp hoàn tất (bản trước được giữ lại tại %s)", p.Old)
}

// SettleDelay là khoảng thời gian chạy cần thiết để phán định "bản mới đã sống sót".
const SettleDelay = 30 * time.Second

// verifyStaged kiểm tra bản tạm lưu: đối chiếu SHA256 trước, rồi thực sự khởi chạy nó một lần.
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("đọc checksum: %w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("tính checksum: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 không khớp (tải về bị hỏng hoặc bị sửa đổi)")
	}
	return smokeTest(p.New)
}

// smokeTest dùng -h để khởi chạy binary mới, xác nhận nó thực sự chạy được trên hệ
// thống hiện tại. Điều này chặn được cả một lớp vấn đề như tải về bị cắt ngắn, chọn
// sai kiến trúc (exec format error), thiếu phụ thuộc.
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("cấp quyền thực thi: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("kiểm thử khói (smoke test) quá thời gian (binary mới không phản hồi)")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("kiểm thử khói (smoke test) thất bại: %v: %s", err, snippet)
	}
	return nil
}

// swap thay binary hiện tại bằng bản mới tạm lưu.
//
// Cả Unix và Windows đều cho phép rename một file thực thi đang chạy (Windows cấm xóa
// và ghi đè, rename không nằm trong đó), nên ở đây không cần tách nền tảng, cũng không
// cần dừng chính mình trước.
func swap(p Paths) error {
	// rename của Windows không ghi đè đích đã tồn tại, file .old từ lần nâng cấp trước
	// phải dọn trước.
	if err := os.Remove(p.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("dọn bản sao lưu cũ %s: %w", p.Old, err)
	}
	if err := os.Rename(p.Current, p.Old); err != nil {
		return fmt.Errorf("sao lưu bản hiện tại: %w", err)
	}
	if err := os.Rename(p.New, p.Current); err != nil {
		// Hoán đổi thất bại nhưng bản hiện tại đã bị chuyển đi, phải đặt lại nguyên
		// trạng, nếu không lần khởi động sau không có file thực thi.
		if rerr := os.Rename(p.Old, p.Current); rerr != nil {
			return fmt.Errorf("nạp bản mới thất bại (%v), và khôi phục bản hiện tại cũng thất bại: %w", err, rerr)
		}
		return fmt.Errorf("nạp bản mới: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// rollback đổi lại bản cũ mà swap đã sao lưu.
func rollback(p Paths) error {
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("không có bản sao lưu để rollback %s: %w", p.Old, err)
	}
	// Chuyển bản mới không khởi động được sang .failed để giữ lại phục vụ điều tra,
	// thay vì xóa thẳng.
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(p.Current, failed); err != nil {
		return fmt.Errorf("chuyển bản thất bại đi: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		return fmt.Errorf("khôi phục bản cũ: %w", err)
	}
	return nil
}

// Rollback là hiện thực của /api/update/rollback: chủ động quay về bản trước.
// Chỉ làm hoán đổi, việc khởi động lại cũng giao cho script daemon (bên gọi sau đó
// thoát với ExitRestart).
func Rollback() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Old); err != nil {
		return errors.New("không có bản trước để rollback (" + p.Old + " không tồn tại)")
	}
	cleanStaged(p)
	if err := smokeTest(p.Old); err != nil {
		return fmt.Errorf("bản trước không thực thi được, từ chối rollback: %w", err)
	}
	// Hoán đổi hiện tại và bản sao lưu: sau rollback vẫn có thể quay lại.
	tmp := p.Current + ".swap"
	_ = os.Remove(tmp)
	if err := os.Rename(p.Current, tmp); err != nil {
		return fmt.Errorf("chuyển bản hiện tại đi: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("nạp bản trước: %w", err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		log.Printf("[update] dọn dẹp bản sao lưu sau rollback thất bại (không ảnh hưởng vận hành): %v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup báo cáo có tồn tại bản trước để rollback hay không, để frontend quyết định
// có hiển thị nút rollback không.
func HasBackup() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "phiên bản không rõ"
	}
	return s
}
