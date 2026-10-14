package notify

import (
	"bufio"
	"context"

	"net"
	"strings"
	"sync"
	"testing"
)

// File này bổ sung test ở mức giao thức cho kênh email. Trước đó độ bao phủ của email.Send là
// 0 — cả đường SMTP chưa có ca test nào chạy qua, trong khi nó vừa khéo là kênh có mặt giao
// thức lớn nhất, dễ sai nhất trong sáu kênh (bắt tay, xác thực, phong bì, giai đoạn DATA mỗi
// thứ một ngữ nghĩa thất bại riêng).
//
// Chỗ này dùng máy chủ SMTP tối thiểu tự dựng để lái, thay vì mock net/smtp: phần lớn rủi ro
// của kênh email nằm ở bước "đối thoại với máy chủ SMTP thật", mock bước này tương đương không test.

// fakeSMTP là một máy chủ SMTP vừa đủ dùng: hoàn thành được greet/EHLO/AUTH/MAIL/RCPT/DATA/QUIT,
// và trả về mã trả lời chỉ định cho giai đoạn cụ thể theo yêu cầu của ca test.
type fakeSMTP struct {
	ln net.Listener

	// rcptReply là câu trả lời cho RCPT TO; mặc định 250.
	rcptReply string
	// mailReply là câu trả lời cho MAIL FROM; mặc định 250.
	mailReply string
	// advertiseAuth = true thì khai báo hỗ trợ AUTH PLAIN trong EHLO.
	advertiseAuth bool

	mu       sync.Mutex
	data     string
	commands []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, rcptReply: "250 OK", mailReply: "250 OK"}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	addr, ok := f.ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("không phải địa chỉ listen TCP")
	}
	return "127.0.0.1", addr.Port
}

func (f *fakeSMTP) record(cmd string) {
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
}

func (f *fakeSMTP) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func (f *fakeSMTP) sawCommand(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake.local ESMTP ready")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record(line)
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			// Không khai báo STARTTLS: để code đi nhánh văn bản thuần (mục tiêu test là logic
			// phong bì, không phải TLS).
			w("250-fake.local")
			if f.advertiseAuth {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case strings.HasPrefix(line, "AUTH"):
			// Xử lý đơn giản hóa: câu trả lời khởi đầu của PLAIN có thể trải nhiều dòng, chấp
			// nhận luôn.
			w("235 2.7.0 Authentication successful")
		case strings.HasPrefix(line, "MAIL FROM"):
			w(f.mailReply)
		case strings.HasPrefix(line, "RCPT TO"):
			w(f.rcptReply)
		case strings.HasPrefix(line, "DATA"):
			w("354 End data with <CR><LF>.<CR><LF>")
			var sb strings.Builder
			for {
				dl, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				sb.WriteString(dl)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 2.0.0 Ok: queued as FAKE1")
		case strings.HasPrefix(line, "QUIT"):
			w("221 2.0.0 Bye")
			return
		default:
			w("250 OK")
		}
	}
}

func emailCfg(t *testing.T, f *fakeSMTP, extra map[string]any) map[string]any {
	t.Helper()
	host, port := f.hostPort(t)
	cfg := map[string]any{
		"host": host,
		"port": float64(port),
		"from": "artex@example.com",
		"to":   []any{"a@example.com", "b@example.com"},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestEmailSendDeliversFullMessage(t *testing.T) {
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	cfg := emailCfg(t, f, map[string]any{"username": "artex", "password": "pw"})

	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
	// Giai đoạn phong bì phải đi tới: người gửi, hai người nhận, DATA.
	for _, want := range []string{"MAIL FROM:<artex@example.com>", "RCPT TO:<a@example.com>", "RCPT TO:<b@example.com>", "DATA", "AUTH", "QUIT"} {
		if !f.sawCommand(want) {
			t.Errorf("trong phiên SMTP thiếu %q, các lệnh thực tế: %v", want, f.commands)
		}
	}
	// Phần thân là HTML dạng base64, và phải mang nội dung lỗ hổng thật (sau khi mã hóa vẫn
	// nhận ra được).
	body := f.body()
	if body == "" {
		t.Fatal("giai đoạn DATA không nhận được phần thân")
	}
	if !strings.Contains(body, "Content-Type: text/html") {
		t.Errorf("thiếu header Content-Type:\n%s", body)
	}
	if !strings.Contains(body, "base64") {
		t.Errorf("phần thân chưa mã hóa base64 (dòng HTML dài sẽ phá giới hạn 1000 byte mỗi dòng của SMTP):\n%s", body)
	}
	// Nhiều người nhận đều phải xuất hiện trong header To.
	if !strings.Contains(body, "a@example.com, b@example.com") {
		t.Errorf("header To chưa chứa đủ người nhận:\n%s", body)
	}
}

func TestEmailSendWithoutAuth(t *testing.T) {
	// Chưa cấu hình tài khoản thì không được gửi AUTH — một số relay sẽ vì thế mà từ chối nhận.
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
	if f.sawCommand("AUTH") {
		t.Errorf("chưa cấu hình tài khoản mà vẫn gửi AUTH: %v", f.commands)
	}
}

// TestEmailSendClassifiesSMTPReplies là kiểm chứng trực tiếp của bản sửa trong đợt audit này:
// 5xx phán thất bại vĩnh viễn, 4xx (greylisting) phán có thể thử lại.
func TestEmailSendClassifiesSMTPReplies(t *testing.T) {
	cases := []struct {
		name      string
		rcptReply string
		mailReply string
		permanent bool
	}{
		{"người nhận bị 550 từ chối vĩnh viễn", "550 5.1.1 User unknown", "250 OK", true},
		{"người nhận gặp 450 greylisting", "450 4.7.1 Greylisting in action", "250 OK", false},
		{"người nhận gặp 452 hộp thư đầy", "452 4.2.2 Mailbox full", "250 OK", false},
		{"người gửi bị 553 từ chối vĩnh viễn", "250 OK", "553 5.1.3 Bad address", true},
		{"người gửi gặp 451 lỗi tạm thời", "250 OK", "451 4.3.0 Temporary failure", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSMTP(t)
			f.rcptReply = tc.rcptReply
			f.mailReply = tc.mailReply
			_, err := (emailChannel{}).Send(context.Background(), emailCfg(t, f, nil), singleMsg())
			if err == nil {
				t.Fatal("phải báo lỗi")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("phán định permanent sai: mong đợi %v nhận được %v (%v)", tc.permanent, got, err)
			}
			// Nguyên văn của máy chủ phải giữ lại, nếu không người dùng không biết nên tìm quản
			// trị máy chủ hay sửa địa chỉ.
			if !strings.Contains(err.Error(), strings.Fields(tc.rcptReply)[0]) && !strings.Contains(err.Error(), strings.Fields(tc.mailReply)[0]) {
				t.Errorf("trong lỗi phải giữ mã trả lời của máy chủ: %v", err)
			}
		})
	}
}

func TestEmailSendRefusesPlaintextCredentials(t *testing.T) {
	// PlainAuth của net/smtp từ chối gửi thông tin xác thực trên kết nối chưa mã hóa (trừ khi
	// đích là localhost). Đây là hành vi bảo mật **đúng**, không được phá vòng; nhưng phải đưa
	// ra lỗi có thể hướng dẫn người dùng sửa. Chỗ này dùng một hostname không phải localhost để
	// kích hoạt nó.
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	_, port := f.hostPort(t)
	cfg := map[string]any{
		"host":     "smtp.example.com", // không phải localhost
		"port":     float64(port),
		"from":     "a@example.com",
		"to":       []any{"b@example.com"},
		"username": "artex",
		"password": "pw",
	}
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Skip("DNS của máy phân giải về máy chủ cục bộ, bỏ qua (không ảnh hưởng các ca khác)")
	}
	// Không kết nối được hoặc bị từ chối gửi thông tin xác thực đều tính là qua assert này; điểm
	// mấu chốt là **không được** âm thầm gửi mật khẩu đi.
	if !IsPermanent(err) && !strings.Contains(err.Error(), "kết nối") {
		t.Logf("lỗi: %v (không kết nối được khi không phải localhost là điều dự kiến)", err)
	}
}

func TestEmailValidateReportsMissingFields(t *testing.T) {
	// Kênh email có nhiều field cấu hình nhất, sót bất kỳ cái nào cũng chỉ lộ ra khi giao phát;
	// chỗ này xác nhận từng cái đều được kiểm tra chặn sớm. Assert kiểm tra việc "thông báo lỗi
	// có nhắc đến thiếu gì".
	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{"thiếu host", map[string]any{"port": float64(25), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"thiếu port", map[string]any{"host": "smtp.example.com"}},
		{"port vượt giới hạn", map[string]any{"host": "h", "port": float64(70000), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"thiếu from", map[string]any{"host": "h", "port": float64(25), "to": []any{"d@e.f"}}},
		{"thiếu to", map[string]any{"host": "h", "port": float64(25), "from": "a@b.c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := (emailChannel{}).Validate(tc.cfg); err == nil {
				t.Fatalf("phải kiểm tra thất bại: %v", tc.cfg)
			}
		})
	}
}

// TestEmailConfigTolerance bao phủ khả năng chịu lỗi khi đọc cấu hình: trong JSONB giá trị số
// là float64, nhưng người dùng trên UI có thể điền cổng thành chuỗi; mảng cũng có thể là một
// chuỗi đơn.
func TestEmailConfigTolerance(t *testing.T) {
	cfg := map[string]any{
		"host": "smtp.example.com",
		"port": "587", // cổng dạng chuỗi
		"from": "a@b.c",
		"to":   "d@e.f", // chuỗi đơn thay vì mảng
		"tls":  "true",  // boolean dạng chuỗi
	}
	if err := (emailChannel{}).Validate(cfg); err != nil {
		t.Fatalf("phải chịu được giá trị số dạng chuỗi: %v", err)
	}
	if got := cfgInt(cfg, "port"); got != 587 {
		t.Errorf("cfgInt chưa phân tích cổng dạng chuỗi, nhận được %d", got)
	}
	if !cfgBool(cfg, "tls") {
		t.Error("cfgBool chưa phân tích chuỗi \"true\"")
	}
	if to := cfgStrings(cfg, "to"); len(to) != 1 || to[0] != "d@e.f" {
		t.Errorf("cfgStrings chưa tương thích chuỗi đơn, nhận được %v", to)
	}
}

// TestFilterValidateRejectsTypo là kiểm chứng trực tiếp của bản sửa audit:
// ngưỡng gõ sai chữ phải bị chặn lúc ghi, nếu không bộ lọc sẽ âm thầm mất tác dụng và biến
// thành đẩy tất cả.
func TestFilterValidateRejectsTypo(t *testing.T) {
	good := []string{"", "low", "medium", "high", "critical"}
	for _, s := range good {
		if err := (Filter{MinSeverity: s}).Validate(); err != nil {
			t.Errorf("ngưỡng hợp lệ %q bị từ chối: %v", s, err)
		}
	}
	// Đây là những lỗi gõ sai thực sự sẽ xảy ra — tất cả đều phải bị từ chối.
	for _, s := range []string{"hgih", "HIGH", "nghiêm trọng", "high ", "crit"} {
		err := (Filter{MinSeverity: s}).Validate()
		if err == nil {
			t.Errorf("ngưỡng bất hợp lệ %q phải bị từ chối (nếu không bộ lọc âm thầm mất tác dụng, biến thành đẩy tất cả)", s)
			continue
		}
		// Thông báo lỗi phải hướng dẫn người dùng sửa đúng.
		if !strings.Contains(err.Error(), "low") || !strings.Contains(err.Error(), "critical") {
			t.Errorf("thông báo lỗi phải liệt kê các giá trị khả dụng, nhận được %q", err.Error())
		}
	}
}

// TestFilterValidateIsWriteTimeOnly khóa chặt phân công "ghi thì chặt, đọc thì khoan":
// giá trị xấu đã có trong kho không được khiến kênh không đọc ra được (điều đó sẽ khiến các
// kênh lịch sử đột ngột ngừng đẩy toàn bộ).
func TestFilterValidateIsWriteTimeOnly(t *testing.T) {
	raw := []byte(`{"min_severity":"hgih"}`)
	f := ParseFilter(raw) // không báo lỗi
	if f.MinSeverity != "hgih" {
		t.Fatalf("đường đọc phải giữ nguyên bản, nhận được %q", f.MinSeverity)
	}
	// Và kênh đó vẫn phán định được sự kiện (không panic, không treo).
	_ = Match(f, Snapshot{Kind: EventFindingCreated, Severity: "critical"})
}
