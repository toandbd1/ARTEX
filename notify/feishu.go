package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// feishuChannel triển khai bot tùy chỉnh Feishu (gồm Lark), đi qua card tương tác.
//
// Đặc tính nền tảng:
//   - Thuật toán ký **khác** với DingTalk, và cực kỳ dễ viết sai, xem chú thích feishuSign.
//   - Giống DingTalk, nhét lỗi nghiệp vụ vào body của HTTP 200 (code != 0).
//   - Header card hỗ trợ template màu, dùng ánh xạ mức độ để phối màu, giúp nhìn danh sách tin
//     nhắn là nhận ra ngay mức nghiêm trọng.
type feishuChannel struct{}

func (feishuChannel) Kind() string { return KindFeishu }

// Bot tùy chỉnh Feishu khoảng 5 lần/giây, quy đổi 100 lần/phút.
func (feishuChannel) DefaultRatePerMin() int { return 100 }

// Đoạn cuối của địa chỉ Webhook chính là định danh duy nhất của bot, thuộc thông tin xác thực.
func (feishuChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// Tương tự: đổi địa chỉ Webhook phải xác lập lại khóa ký cho địa chỉ mới.
func (feishuChannel) DestinationKeys() []string { return []string{"webhook"} }

func (feishuChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("thiếu địa chỉ Webhook")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("địa chỉ Webhook không hợp lệ: %w", err)
	}
	return nil
}

func (c feishuChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	card, kept := feishuCard(m)
	payload := map[string]any{
		"msg_type": "interactive",
		"card":     card,
	}
	// Tham số ký nằm cùng tầng với tin nhắn, và chỉ xuất hiện khi đã cấu hình secret.
	if secret := cfgString(cfg, "secret"); secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, secret)
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		// Một số phiên bản hook của Feishu dùng bộ tên field này, tương thích luôn.
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("phân tích response của Feishu thất bại: %w (%s)", err, snippet(raw))
	}
	if res.Code != 0 {
		return 0, Permanent(fmt.Errorf("Feishu trả về lỗi %d: %s", res.Code, res.Msg))
	}
	if res.StatusCode != 0 {
		return 0, Permanent(fmt.Errorf("Feishu trả về lỗi %d: %s", res.StatusCode, res.StatusMessage))
	}
	return kept, nil
}

// feishuSign tính chữ ký theo quy tắc chính thức của Feishu.
//
// Chỗ này cực kỳ dễ sập bẫy: ví dụ chính thức là
//
//	hmac.new(string_to_sign.encode(), digestmod=sha256)
//
// nghĩa là **key = timestamp + "\n" + secret, message rỗng**, chứ không phải theo trực giác
// "key=secret, message=stringToSign" — đó đúng là thuật toán của DingTalk. Thuật toán hai bên
// vừa khéo ngược nhau, chép theo triển khai của nhà kia chắc chắn kiểm tra chữ ký thất bại
// (báo 19021).
func feishuSign(timestamp, secret string) string {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// feishuSeverityTemplate ánh xạ mức độ lỗ hổng sang template phối màu header card.
// Mức không xác định dùng grey — không dùng blue, tránh nhầm với low.
func feishuSeverityTemplate(severity string) string {
	switch severity {
	case "critical":
		return "red"
	case "high":
		return "orange"
	case "medium":
		return "yellow"
	case "low":
		return "blue"
	default:
		return "grey"
	}
}

// feishuMaxCardBytes là giới hạn trên thận trọng cho nội dung card. Feishu có giới hạn dung
// lượng cho card, vượt là từ chối cả tin; lấy một giá trị thấp hơn hẳn giới hạn chính thức,
// tính cả chi phí bao bọc JSON vào.
const feishuMaxCardBytes = 24000

// feishuCard dựng card tương tác, trả về card và **số mục thực tế đã ghi vào**.
// Công dụng của kept giống markdownBody: chỉ các mục thực sự vào được card mới nên được đánh
// dấu là đã gửi đến.
func feishuCard(m Message) (map[string]any, int) {
	elements := []any{}
	kept := 0
	if m.Batch {
		// Đóng gói theo từng mục nguyên trước rồi mới ghép phần đầu: phần đầu phải ghi "N mục
		// còn lại sẽ tiếp tục ở tin nhắn tiếp theo", N phải lấy từ số mục thực tế chứa được.
		kept = packItemCount(m.Items, feishuMaxCardBytes, markdownReservedBytes, "", byteSize, func(it Item, idx int) string {
			return feishuBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		elements = append(elements, feishuMarkdownDiv(markdownBatchIntro(m, items, len(m.Items))))
		for i, it := range items {
			elements = append(elements, feishuMarkdownDiv(feishuBatchLine(it, i+1)))
		}
		if m.HomeURL != "" {
			elements = append(elements, feishuButton("Xem tất cả trên nền tảng", m.HomeURL))
		}
	} else if len(m.Items) > 0 {
		kept = 1
		it := m.Items[0]
		elements = append(elements, feishuMarkdownDiv(feishuItemLines(it)))
		if it.DetailURL != "" {
			elements = append(elements, feishuButton("Xem chi tiết", it.DetailURL))
		}
	}

	card := map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"title": map[string]any{"tag": "plain_text", "content": markdownTitle(m)}},
		"elements": elements,
	}
	if len(m.Items) > 0 {
		card["header"].(map[string]any)["template"] = feishuSeverityTemplate(m.Items[0].Severity)
	}
	return card, kept
}

func feishuMarkdownDiv(content string) map[string]any {
	return map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": content}}
}

func feishuButton(label, url string) map[string]any {
	return map[string]any{
		"tag": "action",
		"actions": []any{map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "lark_md", "content": label},
			"url":  url,
			"type": "primary",
		}},
	}
}

// feishuItemLines render phần thân lark_md của một lỗ hổng đơn lẻ.
//
// lark_md và markdown là định dạng văn bản cùng họ, cũng phân tích liên kết và nhấn mạnh, nên
// các field đến từ bên ngoài đều phải qua markdownText (đưa về một dòng + thoát) — nếu không
// một tiêu đề lỗ hổng cũng có thể biến thành liên kết ngoài bấm được trong Feishu.
func feishuItemLines(it Item) string {
	out := fmt.Sprintf("**%s · %s**", SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if it.IsStatusChange() {
		out += fmt.Sprintf("\n**Thay đổi trạng thái**: %s → %s",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		out += fmt.Sprintf("\n**Loại**: %s", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		out += fmt.Sprintf("\n**Tài sản**: %s", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			out += fmt.Sprintf("\n**Tóm tắt**: %s", s)
		}
	}
	return out
}

// feishuBatchLine render một mục trong card tổng hợp.
func feishuBatchLine(it Item, index int) string {
	line := fmt.Sprintf("**%d. %s · %s**", index, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		line += " — " + markdownText(a, 0)
	}
	return line
}
