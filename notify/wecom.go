package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit là giới hạn cứng của markdown content của bot nhóm WeCom (byte, không
// phải ký tự). Đây là giới hạn chặt nhất trong cả sáu kênh, cũng là lý do chính tồn tại
// TruncateBytes.
const weComMarkdownLimit = 4096

// weComChannel triển khai bot nhóm WeCom (WeChat doanh nghiệp).
//
// Đặc tính nền tảng:
//   - Chỉ xác thực qua key trên URL, không hỗ trợ ký — nên bản thân địa chỉ webhook chính là
//     toàn bộ thông tin xác thực.
//   - Giới hạn trên của markdown content là 4096 **byte**, vượt quá thì từ chối cả tin (không
//     phải cắt bớt). Tiếng Trung 3 byte/chữ, nghĩa là phần thân chỉ viết được hơn một nghìn
//     chữ, bắt buộc phải cắt ở phía client.
//   - Giới hạn 20 tin/phút, cũng dựa vào giới hạn tần suất phía client để chống đỡ.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// WeCom chỉ có một chỗ thông tin xác thực là Webhook (key trên URL), và nó không hỗ trợ ký —
// toàn bộ địa chỉ chính là toàn bộ thông tin xác thực, không có field nào khác cần che.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// WeCom chỉ có một field Webhook, nó vừa là đích đến vừa là thông tin xác thực, nên không tồn
// tại chuyện "thông tin xác thực còn sót lại sau khi đổi địa chỉ".
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("thiếu địa chỉ Webhook")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("địa chỉ Webhook không hợp lệ: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// Lô tổng hợp có thể rất dài (50 mục × mỗi mục một dòng + tiền tố), 4096 byte rất dễ vượt.
	// Cắt ở đây thay vì dựa vào nền tảng báo lỗi: bị từ chối nghĩa là mất cả lô, còn cắt thì ít
	// nhất cũng gửi đến được vài mục đầu.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("phân tích response của WeCom thất bại: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 là gọi API vượt giới hạn — cửa sổ giới hạn của nền tảng sẽ trôi đi, thử lại sau
		// khi lùi bước là có hiệu quả, nên tường minh xếp vào loại có thể thử lại. Đi đến đây
		// nghĩa là rate_per_min phía client cấu hình quá liều, thử lại chỉ là chống đỡ, cách sửa
		// thật sự là hạ giá trị giới hạn của kênh đó.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("WeCom giới hạn tần suất %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 là webhook key không hợp lệ — thất bại vĩnh viễn, thử lại không tự khỏi.
		return 0, Permanent(fmt.Errorf("WeCom trả về lỗi %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
