package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailDialTimeout / emailSessionTimeout lần lượt ràng buộc việc thiết lập kết nối và toàn bộ
// phiên SMTP. Bản thân net/smtp không có bất kỳ cơ chế timeout nào, nếu không đặt hai rào này,
// một đối phương bị kẹt sẽ khiến goroutine giao phát treo vĩnh viễn tại đó — mà dispatcher xử
// lý tuần tự bằng một goroutine, tương đương cả hệ thống thông báo đứng hình.
const (
	emailDialTimeout    = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel triển khai giao phát email SMTP.
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// Email không có giới hạn tần suất của nền tảng, nhưng không nên dùng nó để spam; đặt một giá
// trị mặc định rộng rãi.
func (emailChannel) DefaultRatePerMin() int { return 60 }

// Chỉ che mật khẩu. Máy chủ SMTP, tài khoản, người nhận đều không tính là bí mật, che chúng
// chỉ khiến việc chỉnh sửa trở nên phiền phức.
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port quyết định giao mật khẩu cho máy chủ nào; tls quyết định có mã hóa truyền tải
// không. Thay đổi bất kỳ một trong ba đều yêu cầu xác lập lại mật khẩu — tiện thể khiến bước
// "tắt TLS" phải tường minh mang theo thông tin xác thực, chứ không được sửa tiện tay.
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("thiếu địa chỉ máy chủ SMTP")
	}
	port := cfgInt(cfg, "port")
	if port <= 0 || port > 65535 {
		return errors.New("cổng SMTP không hợp lệ (phải là 1-65535)")
	}
	if cfgString(cfg, "from") == "" {
		return errors.New("thiếu địa chỉ người gửi")
	}
	if len(cfgStrings(cfg, "to")) == 0 {
		return errors.New("cần ít nhất một địa chỉ người nhận")
	}
	return nil
}

func (c emailChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	host := cfgString(cfg, "host")
	port := cfgInt(cfg, "port")
	from := cfgString(cfg, "from")
	to := cfgStrings(cfg, "to")
	username := cfgString(cfg, "username")
	password := cfgString(cfg, "password")
	implicitTLS := cfgBool(cfg, "tls")

	msg, err := buildEmailMessage(from, to, m)
	if err != nil {
		return 0, Permanent(err)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	client, err := emailDial(ctx, addr, host, implicitTLS)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	// STARTTLS: đối phương hỗ trợ thì nâng cấp. Không được gửi thông tin xác thực trong phiên
	// văn bản thuần (xem phần giải thích auth bên dưới).
	if !implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return 0, fmt.Errorf("STARTTLS thất bại: %w", err)
			}
		}
	}
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			// smtp.PlainAuth sẽ từ chối gửi thông tin xác thực trên kết nối chưa mã hóa (trừ khi
			// đích là localhost). Đây là hành vi bảo mật **đúng**, không được phá vòng, nhưng cần
			// dịch rõ nguyên nhân — nếu không người dùng chỉ nhìn thấy "unencrypted connection" mà
			// không biết phải làm gì.
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("từ chối gửi thông tin xác thực: kết nối chưa mã hóa. Hãy bật TLS, hoặc đổi sang cổng 465 (TLS ngầm định), hoặc tích vào \"Bật TLS\" (%w)", err))
			}
			return 0, Permanent(fmt.Errorf("xác thực SMTP thất bại: %w", err))
		}
	}
	if err := client.Mail(from); err != nil {
		return 0, smtpStageError(fmt.Sprintf("người gửi %s bị từ chối", from), err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return 0, smtpStageError(fmt.Sprintf("người nhận %s bị từ chối", rcpt), err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return 0, fmt.Errorf("SMTP DATA thất bại: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return 0, fmt.Errorf("ghi phần thân email thất bại: %w", err)
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("gửi email thất bại: %w", err)
	}
	// Quit thất bại không ảnh hưởng đến thực tế "email đã được máy chủ tiếp nhận", nên bỏ qua
	// lỗi của nó.
	_ = client.Quit()
	// Email không bị cắt độ dài (phần thân HTML gửi toàn bộ), cả lô đều tính là đã gửi đến.
	return len(m.Items), nil
}

// emailDial thiết lập kết nối SMTP.
//
// implicitTLS=true đi theo kiểu "vừa kết nối là TLS" như cổng 465; false thì kết nối văn bản
// thuần qua 25/587 rồi STARTTLS sau. Hai kiểu không thể trộn: gửi greeting văn bản thuần vào
// cổng 465 sẽ bị ngắt ngay.
//
// Thời hạn phiên được đặt ngay **tại chỗ thiết lập kết nối** (chứ không phải bổ sung sau đó),
// vì Client của net/smtp giấu kết nối bên dưới trong field không export, bên ngoài không lấy
// được nó; kết nối một khi đã giao đi thì chỉ có thể dựa vào deadline đặt trước để chống đỡ.
// Điều này cũng tiện thể bao phủ cả việc nghẽn trong giai đoạn bắt tay.
// Control gắn blockInternalDial dùng chung một rào bảo vệ với các kênh hệ HTTP. Không gắn thì
// SMTP chính là lỗ hở của toàn bộ hệ thống phòng SSRF: host điền 169.254.169.254 hoặc
// 127.0.0.1 là kết nối được trực tiếp, mà khi smtp.NewClient bắt tay thất bại sẽ gói dòng đối
// phương trả về vào lỗi, qua last_error được API lịch sử giao phát hiển thị lại, tạo thành
// nguyên mẫu đọc bán mù; chênh lệch thời gian giữa "kết nối bị từ chối và timeout" còn có thể
// dùng để dò cổng. Giai đoạn quay số là điểm có hiệu lực cuối cùng, cũng bao phủ DNS rebinding.
func emailDial(ctx context.Context, addr, host string, implicitTLS bool) (*smtp.Client, error) {
	d := &net.Dialer{Timeout: emailDialTimeout, Control: blockInternalDial}
	var conn net.Conn
	var err error
	if implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("kết nối máy chủ SMTP thất bại: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(emailSessionTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("bắt tay SMTP thất bại: %w", err)
	}
	return client, nil
}

// smtpStageError phân loại thất bại của một giai đoạn thành "có thể thử lại" và "thất bại vĩnh
// viễn" theo mã trả lời SMTP.
//
// Tại sao bắt buộc phải phân biệt: ngữ nghĩa 4xx và 5xx của SMTP hoàn toàn khác nhau —
//   - 4xx (450 greylisting, 451 lỗi cục bộ, 452 thiếu dung lượng lưu trữ) là từ chối **tạm
//     thời**, cách làm đúng chuẩn là thử lại sau; đặc biệt greylisting, gần như lần giao phát
//     đầu tiên nào cũng gặp.
//   - 5xx (550 người dùng không tồn tại, 553 địa chỉ bất hợp lệ) là từ chối vĩnh viễn, thử lại
//     không có ý nghĩa.
//
// Nếu nhất loạt phán thất bại vĩnh viễn, một máy chủ email bật greylisting sẽ khiến **mỗi** tin
// đẩy đều rơi vào failed ngay sau lần thử đầu tiên — mà kiểu thất bại này chính là tình huống
// thử lại tự động nên phát huy tác dụng nhất.
// Mã trả lời lấy ba chữ số đầu của văn bản lỗi; khi không lấy được mã thì xử lý theo hướng có
// thể thử lại (thà thử thêm một lần, còn hơn vì không phân tích được mà phán chết một lỗi có
// thể chỉ là tạm thời).
func smtpStageError(what string, err error) error {
	code := smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode lấy mã trả lời ba chữ số đứng đầu từ văn bản lỗi SMTP, không lấy được trả về 0.
// net/smtp không export field mã lỗi, chỉ có thể lấy từ văn bản; định dạng là "450 4.7.1 ...".
func smtpReplyCode(text string) int {
	if len(text) < 3 {
		return 0
	}
	n, err := strconv.Atoi(text[:3])
	if err != nil {
		return 0
	}
	return n
}

// buildEmailMessage lắp ráp email RFC 5322 hoàn chỉnh.
//
// Phần thân mã hóa base64 vì hai lý do: một là SMTP quy định một dòng không quá 1000 byte, mà
// phần thân HTML (đặc biệt email tổng hợp) rất dễ xuất hiện dòng quá dài; hai là base64 vốn
// không sinh ra dòng bắt đầu bằng ".", khỏi phải bận tâm việc thoát dấu chấm của SMTP.
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// Chủ đề không phải ASCII phải mã hóa RFC 2047, nếu không sẽ bị client hiển thị thành mã loạn.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// Email không có giới hạn độ dài cứng, nên không cắt phần thân.
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
	// base64 gãy dòng theo 76 ký tự, phù hợp RFC 2045.
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}
