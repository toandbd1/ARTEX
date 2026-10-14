package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// telegramTextLimit là giới hạn trên của field text của Telegram sendMessage (số ký tự).
const telegramTextLimit = 4096

// telegramChannel triển khai Telegram Bot API.
//
// Đặc tính nền tảng:
//   - Xác thực nằm toàn bộ trong URL path (/bot<token>/sendMessage), không cần ký.
//   - Dùng chế độ phân tích HTML thay vì MarkdownV2: MarkdownV2 yêu cầu thoát `_*[]()~`>#+-=|{}.!`
//     tổng cộng 18 ký tự, sót một ký tự là cả tin nhắn bị từ chối; HTML chỉ cần thoát ba ký tự & < >.
//   - Lỗi nghiệp vụ cũng giấu trong HTTP 200, phán định bằng field ok.
type telegramChannel struct{}

func (telegramChannel) Kind() string { return KindTelegram }

// Telegram chat đơn khoảng 1 tin/giây, nhóm 20 tin/phút. Lấy giá trị thận trọng.
func (telegramChannel) DefaultRatePerMin() int { return 20 }

// Bot Token là thông tin xác thực đầy đủ; chat_id chỉ là người nhận, không tính là bí mật (cầm
// được nó mà không có Token cũng không gửi được tin).
func (telegramChannel) SecretKeys() []string { return []string{"bot_token"} }

// base_url quyết định Token được gửi đến API endpoint nào (như reverse proxy tự dựng), đổi nó
// phải xác lập lại Token.
func (telegramChannel) DestinationKeys() []string { return []string{"base_url"} }

func (telegramChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "bot_token") == "" {
		return errors.New("thiếu Bot Token")
	}
	if cfgString(cfg, "chat_id") == "" {
		return errors.New("thiếu Chat ID")
	}
	if base := cfgString(cfg, "base_url"); base != "" {
		if err := validateHTTPURL(base); err != nil {
			return fmt.Errorf("địa chỉ API không hợp lệ: %w", err)
		}
	}
	return nil
}

func (c telegramChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := telegramEndpoint(cfg)
	if err != nil {
		return 0, Permanent(err)
	}
	text, kept := telegramHTML(m)
	payload := map[string]any{
		"chat_id":                  cfgString(cfg, "chat_id"),
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": false,
	}
	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("phân tích response của Telegram thất bại: %w (%s)", err, snippet(raw))
	}
	if res.OK {
		return kept, nil
	}
	// 429 là giới hạn tần suất, thử lại sau khi lùi bước có hiệu quả; còn lại (400 sai tham số,
	// 401 sai token, 403 bị chặn, 404 chat không tồn tại) đều là vấn đề cấu hình, thử lại không
	// tự khỏi.
	if res.ErrorCode == 429 {
		return 0, fmt.Errorf("Telegram giới hạn tần suất: %s", res.Description)
	}
	return 0, Permanent(fmt.Errorf("Telegram trả về lỗi %d: %s", res.ErrorCode, res.Description))
}

// telegramEndpoint ghép ra địa chỉ sendMessage. Khi base_url để trống thì dùng API chính thức,
// khác rỗng thì dùng cho reverse proxy Bot API tự dựng (nhu cầu phổ biến khi mạng nội địa khó
// truy cập trực tiếp).
func telegramEndpoint(cfg map[string]any) (string, error) {
	base := cfgString(cfg, "base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimSuffix(base, "/")
	token := cfgString(cfg, "bot_token")
	raw := base + "/bot" + token + "/sendMessage"
	u, err := url.Parse(raw)
	if err != nil {
		// Không truyền err ra ngoài: địa chỉ chứa Bot Token, và lúc này ngay cả addr cũng không
		// nên hiển thị lại.
		return "", fmt.Errorf("ghép địa chỉ API thất bại (địa chỉ API: %s)", redactRequestTarget(base))
	}
	return u.String(), nil
}

// telegramHTML render phần thân HTML, trả về phần thân và số mục thực tế đã ghi vào
// (xem Channel.Send).
func telegramHTML(m Message) (string, int) {
	var b strings.Builder
	b.WriteString("<b>" + telegramEscape(markdownTitle(m)) + "</b>\n")
	if m.Batch {
		// Giới hạn trên của Telegram là **số ký tự**, nên đóng gói cũng đo theo ký tự (runeSize).
		footer := ""
		if m.HomeURL != "" {
			footer = fmt.Sprintf("\n\n<a href=\"%s\">Xem tất cả trên nền tảng</a>", telegramEscapeAttr(m.HomeURL))
		}
		kept := packItemCount(m.Items, telegramTextLimit, telegramReservedRunes, footer, runeSize, func(it Item, idx int) string {
			return telegramBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		b.Reset()
		b.WriteString("<b>" + telegramEscape(telegramBatchTitle(m, items, len(m.Items))) + "</b>")
		for i, it := range items {
			b.WriteString("\n" + telegramEscape(telegramBatchLine(it, i+1)))
		}
		b.WriteString(footer)
		return TruncateHTML(b.String(), telegramTextLimit), kept
	}
	if len(m.Items) == 0 {
		return b.String(), 0
	}
	it := m.Items[0]
	if it.IsStatusChange() {
		b.WriteString(fmt.Sprintf("\n<b>Thay đổi trạng thái</b>: %s → %s",
			telegramEscape(StatusLabel(it.FromStatus)), telegramEscape(StatusLabel(it.ToStatus))))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		b.WriteString("\n<b>Loại</b>: " + telegramEscape(it.VulnClass))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		b.WriteString("\n<b>Tài sản</b>: " + telegramEscape(a))
	}
	if s := OneLine(it.Summary, maxSummaryRunes); s != "" {
		b.WriteString("\n<b>Tóm tắt</b>: " + telegramEscape(s))
	}
	if it.DetailURL != "" {
		b.WriteString(fmt.Sprintf("\n\n<a href=\"%s\">Xem chi tiết</a>", telegramEscapeAttr(it.DetailURL)))
	}
	return TruncateHTML(b.String(), telegramTextLimit), 1
}

// telegramReservedRunes là lượng dự trữ cho tiêu đề tin nhắn và gợi ý cắt bớt có thể xuất hiện
// (tính theo ký tự).
const telegramReservedRunes = 160

// telegramBatchLine render một mục trong tin tổng hợp (chưa thoát, phía gọi thoát thống nhất).
func telegramBatchLine(it Item, idx int) string {
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		return fmt.Sprintf("%d. %s · %s — %s", idx, SeverityLabel(it.Severity), it.Title(), a)
	}
	return fmt.Sprintf("%d. %s · %s", idx, SeverityLabel(it.Severity), it.Title())
}

// telegramBatchTitle render dòng tiêu đề của tin nhắn tổng hợp. Số mục dùng là số mục **thực
// tế có trong bản tin này**, chứ không phải tổng số của lô — nếu không người đọc sẽ tưởng con
// số ghi ở đầu tin nhắn là tất cả.
func telegramBatchTitle(m Message, items []Item, total int) string {
	title := fmt.Sprintf("Tổng hợp lỗ hổng · tổng %d mục", total)
	if extra := total - len(items); extra > 0 {
		title += fmt.Sprintf("(hiển thị %d mục đầu, %d mục còn lại tiếp tục ở tin nhắn tiếp theo)", len(items), extra)
	}
	if m.WindowMinutes > 0 {
		title = fmt.Sprintf("%d phút gần đây · %s", m.WindowMinutes, title)
	}
	return title
}

// telegramEscape thoát nội dung văn bản HTML.
// Telegram chỉ nhận ba loại entity này, sau khi thoát các entity có sẵn như &amp; sẽ bị thoát
// lần hai — đây đúng là hành vi mong đợi: thứ chúng ta muốn hiển thị là ký tự gốc, chứ không
// phải để người dùng chèn HTML.
func telegramEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramEscapeAttr thoát giá trị thuộc tính HTML. Ngoài việc thoát văn bản còn phải xử lý dấu
// nháy — URL mang dấu nháy sẽ đóng thuộc tính href sớm, biến nội dung phía sau thành điểm chèn.
func telegramEscapeAttr(s string) string {
	s = telegramEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
