package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets quyết định có cho phép giao phát tin nhắn tới địa chỉ loopback /
// link-local hay không.
//
// Mặc định từ chối. Mấy dải địa chỉ này không phải nơi bot IM hay máy chủ email công cộng xuất
// hiện, còn những thứ chúng chạm tới được thì rất nhạy cảm: cổng quản trị của dịch vụ khác trên
// cùng máy, và endpoint metadata của môi trường cloud (169.254.169.254, có thể đọc ra thông tin
// xác thực của instance). Địa chỉ giao phát do quản trị viên cấu hình, nhưng một phiên quản trị
// bị XSS/CSRF mượn tay, hoặc người thứ hai dùng chung một JWT, đều có thể nhờ sửa cấu hình mà
// đọc lại nội dung response — doJSON sẽ ghi 200 byte đầu của body response 4xx/5xx vào
// last_error, và API lịch sử giao phát sẽ hiển thị nó lại, đó chính là một nguyên mẫu đọc bán mù.
//
// Nhưng "SMTP relay trên máy cục bộ" (postfix trên 127.0.0.1:25) là cấu hình phổ biến của email
// tự dựng, chặn một nhát sẽ làm người dùng bị kẹt. Nên để lại một lối thoát tường minh thay vì
// cứng nhắc cấm hết: đặt ARTEX_NOTIFY_ALLOW_LOCAL=1 là cho phép.
//
// Export thành AllowLocalTargetsEnv để test có thể bật nó một cách rõ ràng — các ca test của
// package này và package server dùng rất nhiều đầu nhận giả httptest trên 127.0.0.1, không bật
// thì tất cả đều bị rào bảo vệ chặn lại.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP báo cáo IP đích có thuộc dải địa chỉ "mặc định không cho phép giao phát" không.
//
// Chỉ từ chối loopback, link-local (gồm metadata cloud 169.254.169.254), unspecified và
// multicast. **Không từ chối** mạng riêng RFC1918: tự dựng Mattermost / SMTP relay trong nội
// mạng là cách dùng hợp lệ rất phổ biến, chặn luôn chúng sẽ khiến tính năng trong môi trường
// thật trực tiếp không dùng được. Lựa chọn này là có chủ đích — phòng hộ phải chặn được những
// đích thực sự nhạy cảm, đồng thời không được phá luôn cả triển khai bình thường.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6 (::ffff:127.0.0.1) phải quy về IPv4 rồi mới phán định, nếu không sẽ bị
	// lách qua kiểm tra.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial là hook Control của bộ quay số http.Transport, kiểm tra địa chỉ đích **khi
// thiết lập kết nối**.
//
// Tại sao đặt ở giai đoạn quay số thay vì chỉ kiểm tra lúc lưu cấu hình: đây mới là điểm có
// hiệu lực cuối cùng. Nó đồng thời bao phủ hai tình huống lách qua kiểm tra cấu hình — DNS
// rebinding (lúc kiểm tra phân giải ra IP công cộng, lúc kết nối thật phân giải ra nội mạng) và
// chuyển hướng (dù chúng ta đã từ chối nhảy qua host khác, nhưng nhảy cùng host vẫn có thể trỏ
// path sang chỗ khác).
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("không thể phân giải địa chỉ đích %q", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("từ chối giao phát tới địa chỉ máy cục bộ/link-local %s (nếu thực sự cần giao phát tới dịch vụ trên máy, đặt %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport chỉ thêm một rào bảo vệ quay số trên nền Transport mặc định.
// Dùng Clone để giữ lại toàn bộ tinh chỉnh mặc định (connection pool, HTTP/2, timeout, proxy,
// v.v.), tránh vì thêm một kiểm tra mà thay đổi các hành vi khác.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient là client dùng chung cho việc giao phát của tất cả các kênh.
//
// Cố ý **không** tái sử dụng proxy ra ngoài toàn cục của dự án (GlobalProxy phía server): proxy
// đó dành cho lưu lượng mục tiêu pentest, thường là đường hầm không ổn định, còn tính khả dụng
// của thông báo không nên bị dao động của mạng mục tiêu bắt cóc. Đẩy IM kết nối trực tiếp là
// được. Timeout đặt 15 giây — đối phương chậm hơn thế thực tế đã là trạng thái sự cố.
//
// Từ chối chuyển hướng qua host khác: địa chỉ giao phát của tính năng này đều có dạng "một
// endpoint cố định", bình thường sẽ không chuyển hướng sang host khác; mà thông tin xác thực của
// mấy nhà này (access_token của DingTalk, key của WeCom, bot token của Telegram) **nằm ngay
// trong URL**, đi theo nhảy qua host khác tương đương giao thông tin xác thực cho đích chuyển
// hướng. Nhảy cùng host (như bổ sung dấu gạch chéo cuối) vẫn được cho phép.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("quá nhiều lần chuyển hướng")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("từ chối chuyển hướng qua host khác (%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit giới hạn kích thước đọc body response. Khi đối phương bất thường có thể nhả lại
// nội dung cực lớn, mà chúng ta chỉ cần mã lỗi và một đoạn mô tả lỗi ngắn để hiển thị trong lịch
// sử giao phát.
const respBodyLimit = 8 << 10

// doJSON gửi một yêu cầu và trả về body response (đã giới hạn độ dài).
//
// Khi payload là nil thì gửi body rỗng (dùng cho GET hoặc tình huống nền tảng không yêu cầu body).
// Các cặp khóa-giá trị trong headers được đính kèm nguyên bản, dùng cho header tùy chỉnh của
// Webhook tổng quát.
//
// Phân loại lỗi là trách nhiệm cốt lõi của hàm này: thất bại tầng mạng và 5xx/408/429 xếp vào
// "có thể thử lại", còn lại 4xx xếp vào "thất bại vĩnh viễn" — thử lại một 403 chỉ là in cùng
// một lỗi 3 lần vào nhật ký.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// Tuần tự hóa thất bại là bug cục bộ (kiểu field cấu hình không đúng), thử lại cũng
			// không tốt lên.
			return nil, Permanent(fmt.Errorf("dựng body yêu cầu thất bại: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// URL bất hợp lệ — nhiều khả năng người dùng điền sai địa chỉ, thuộc thất bại vĩnh viễn.
		// Chỗ này cũng không được truyền err ra ngoài: văn bản lỗi của url.Parse chứa địa chỉ đầy đủ.
		return nil, Permanent(fmt.Errorf("địa chỉ yêu cầu bất hợp lệ: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// Kết nối bị từ chối, DNS thất bại, timeout — phần lớn là lỗi tạm thời, giao cho thử lại
		// lùi bước.
		//
		// Văn bản lỗi phải khử nhạy cảm rồi mới truyền ra ngoài. Lý do: http.Client.Do trả về
		// *url.Error, Error() của nó là `Op "URL đầy đủ": lỗi nền`, mà thông tin xác thực của mấy
		// nhà trong tính năng này **nằm ngay trong URL** (access_token của DingTalk, key của WeCom,
		// hook id của Feishu, /bot<token>/ của Telegram). Không khử nhạy cảm thì thông tin xác thực
		// sẽ chảy theo chuỗi lỗi này tới bốn nơi: last_error của notification_deliveries (lưu vào
		// kho dưới dạng văn bản thuần), response của API lịch sử giao phát (**lách qua việc che của
		// cấu hình kênh**), nhật ký phía server, và văn bản 502 mà API gửi thử trả về frontend.
		return nil, fmt.Errorf("yêu cầu thất bại: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("đọc response thất bại: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429 (giới hạn tần suất) và 408 (timeout) đáng để thử lại; còn lại 4xx là vấn đề cấu hình
	// hoặc quyền hạn, thử lại vô nghĩa.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("đối phương giới hạn tần suất hoặc timeout (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("dịch vụ đối phương bất thường (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("đối phương từ chối yêu cầu (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet nén body response thành một dòng văn bản ngắn, dùng cho thông báo lỗi. Response có thể
// mang xuống dòng và nhiều khoảng trắng, nhét thẳng vào last_error sẽ làm vỡ bố cục trang lịch sử
// giao phát.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget nén địa chỉ giao phát thành "scheme://host/…", dùng cho thông báo lỗi.
//
// Đây là khẩu kính khử nhạy cảm địa chỉ duy nhất của package này, cố ý làm **đủ thô bạo**: ngoài
// scheme và host, phần còn lại đều vứt bỏ. Lý do là không có cách nào "tổng quát và an toàn" để
// phán định đoạn nào của URL là thông tin xác thực:
//
//	DingTalk  thông tin xác thực ở query        /robot/send?access_token=xxx
//	WeCom     thông tin xác thực ở query        /cgi-bin/webhook/send?key=xxx
//	Feishu    thông tin xác thực ở **đoạn cuối path**  /open-apis/bot/v2/hook/<hook_id>
//	Telegram  thông tin xác thực ở **đoạn giữa path**  /bot<token>/sendMessage
//
// Muốn "chỉ giữ phần hữu dụng" thì phải vá theo từng kênh, mà sót bất kỳ nhà nào cũng là một lần
// rò rỉ thông tin xác thực. Giữ lại host đã đủ để điều tra (DNS không phân giải được, không kết
// nối được, chứng chỉ sai đều định vị được), còn cụ thể là bot nào thì nhờ gợi ý đuôi số bị che
// trong cấu hình kênh mà nhận ra.
//
// Khi phân tích thất bại trả về placeholder cố định — tuyệt đối không hiển thị lại chuỗi gốc.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(không thể phân tích địa chỉ)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError bóc địa chỉ ra khỏi lỗi tầng truyền tải, chỉ giữ lại nguyên nhân nền.
//
// Cấu trúc của *url.Error là {Op, URL, Err}, Error() sẽ in cả URL ra. Chỗ này tường minh lấy
// field Err, né Error() của nó — đáng tin hơn việc thay thế chuỗi sau đó, vì thay thế phải ứng
// phó đúng các biến thể sau khi URL encode/thoát, rất dễ sót.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: lỗi không xác định", uerr.Op, host)
	}
	// Lỗi không phải *url.Error (như lỗi do chính sách chuyển hướng trả về) cũng có thể mang địa
	// chỉ, thống nhất đi qua khử nhạy cảm.
	return redactURLsInText(err.Error())
}

// redactURLsInText thay các địa chỉ http(s) xuất hiện trong một đoạn văn bản thành dạng đã khử
// nhạy cảm.
//
// Dùng để chống đỡ những lỗi không lấy được field có cấu trúc (lỗi chính sách chuyển hướng, lỗi
// tùy chỉnh của thư viện bên thứ ba). Chỉ nhận diện tiền tố http/https, cắt theo khoảng trắng và
// dấu nháy — địa chỉ không chứa hai loại ký tự này.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
