package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel triển khai bot tùy chỉnh DingTalk.
//
// Đặc tính nền tảng (quyết định các lựa chọn triển khai ở đây):
//   - Một bot bị giới hạn 20 tin/phút, gửi vượt sẽ bị âm thầm bỏ rơi (HTTP vẫn có thể 200),
//     nên việc giới hạn tần suất phải làm ở phía client, xem DefaultRatePerMin.
//   - Thiết lập bảo mật chọn một trong ba: ký / từ khóa tùy chỉnh / whitelist IP. Ký là phương
//     án duy nhất không phụ thuộc nội dung tin nhắn, nên chỉ hỗ trợ ký (cũng hỗ trợ webhook
//     trần không bật cả ba).
//   - Thành công/thất bại đều trả về HTTP 200, phân biệt bằng errcode trong body — không kiểm
//     tra errcode sẽ ghi nhận thất bại giao phát thành thành công.
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// Địa chỉ Webhook của DingTalk mang access_token, bản thân nó là thông tin xác thực, nên che toàn bộ.
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// Đích là chính địa chỉ Webhook của DingTalk; đổi địa chỉ phải đồng thời xác lập lại khóa ký cho địa chỉ mới.
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("thiếu địa chỉ Webhook")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("địa chỉ Webhook không hợp lệ: %w", err)
	}
	return nil
}

// Send giao phát một tin nhắn. Khi có liên kết quay lại và là tin đơn lẻ thì dùng ActionCard
// (có nút), nếu không thì dùng markdown.
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook := cfgString(cfg, "webhook")
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return 0, Permanent(err)
	}

	title := markdownTitle(m)
	// Phần thân markdown của DingTalk không có giới hạn byte rõ ràng, nhưng vẫn đặt giới hạn bảo
	// vệ, tránh field bằng chứng phình to bất thường.
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "Xem chi tiết",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	// DingTalk giấu lỗi nghiệp vụ trong response 200.
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("phân tích response của DingTalk thất bại: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 301000 là kiểm tra chữ ký thất bại, 310000 là từ khóa không khớp — đều là lỗi cấu
		// hình, thử lại không tự khỏi.
		return 0, Permanent(fmt.Errorf("DingTalk trả về lỗi %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL bổ sung tham số timestamp và sign cho webhook theo quy tắc ký chính thức.
//
// Quy tắc: chuỗi cần ký = timestamp + "\n" + secret, **khóa của HMAC-SHA256 cũng là secret**,
// kết quả base64 rồi URL-encode. timestamp tính bằng mili giây. Khi secret rỗng thì trả về
// nguyên bản, để hỗ trợ bot chưa bật ký.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// Không truyền err ra ngoài: văn bản lỗi của url.Parse mang địa chỉ đầy đủ (gồm access_token).
		return "", fmt.Errorf("phân tích địa chỉ Webhook thất bại: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL kiểm tra địa chỉ khả dụng, giao thức được hỗ trợ, và phán định nội mạng với
// đích là IP dạng literal.
//
// Hai điểm cần chú ý:
//
//  1. **Thông báo lỗi phải được khử nhạy cảm**. Thứ url.Parse tự trả về là *url.Error, Error()
//     của nó mang **địa chỉ gốc đầy đủ**, mà địa chỉ của mấy nhà trong tính năng này lại nhúng
//     thông tin xác thực (access_token của DingTalk, key của WeCom, bot token của Telegram,
//     hook id của Feishu). Đã từng có thời điểm chỗ này `return err` trực tiếp, thế là lỗi
//     "định dạng địa chỉ bất hợp lệ" mang theo thông tin xác thực chảy ra, đi tới response 400
//     của API test, last_error lưu vào kho mỗi lần giao phát, nhật ký phía server và API lịch
//     sử giao phát.
//
//  2. **IP dạng literal được phán định nội mạng ngay**, tên miền để dành cho giai đoạn quay số
//     phán định (blockInternalDial mới là điểm có hiệu lực cuối cùng, cũng bao phủ được DNS
//     rebinding). Làm một lần ở đây để khi lưu cấu hình đã nhận được gợi ý, thay vì đợi đến
//     lần giao phát đầu tiên thất bại.
//
// Giới hạn giao thức mang tính phòng thủ: file:///gopher:// và tương tự sẽ khiến http.Client
// có hành vi ngoài dự kiến (dù đã bị kiểm tra scheme chặn lại, nhưng không có lý do gì để mở
// rộng mặt này).
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("không thể phân tích địa chỉ (%s)", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("chỉ hỗ trợ http/https, nhận được %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("thiếu tên máy chủ")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("từ chối giao phát tới địa chỉ máy cục bộ/link-local %s (nếu thực sự cần giao phát tới dịch vụ trên máy, đặt %s=1)", ip, AllowLocalTargetsEnv)
	}
	return nil
}
