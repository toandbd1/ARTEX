package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel là adapter của một kênh thông báo. Triển khai phải **phi trạng thái**: cùng một
// instance sẽ được nhiều cấu hình kênh tái sử dụng đồng thời, thông tin xác thực đều truyền
// qua tham số cfg.
type Channel interface {
	// Kind trả về định danh loại kênh, phải khớp với khóa trong registry.
	Kind() string
	// Validate được gọi khi lưu cấu hình, kiểm tra các field bắt buộc và định dạng.
	// Lỗi trả về sẽ hiển thị trực tiếp cho người cấu hình, nên văn bản phải nói rõ
	// "thiếu field nào" chứ không nói chung chung kiểu "cấu hình không hợp lệ".
	Validate(cfg map[string]any) error
	// Send giao phát một tin nhắn, trả về **số mục thực tế đã gửi đến** và lỗi.
	//
	// Tại sao phải trả về số mục: mọi nền tảng đều có giới hạn độ dài tin nhắn, khi tin
	// nhắn tổng hợp không chứa hết cả lô sẽ bị cắt. Nếu phía gọi vô điều kiện đánh dấu cả
	// lô là đã gửi, các mục bị cắt sẽ biến mất — trong tin nhắn không thấy, trong lịch sử
	// giao phát lại hiển thị thành công, không nơi nào phát hiện ra lỗ hổng chưa từng được
	// gửi đi. Sau khi trả về kept, phía gọi chỉ đánh dấu kept mục đầu, phần còn lại để
	// dành cho lô sau.
	//
	// Trả về lỗi nghĩa là giao phát thất bại, trong đó *PermanentError nghĩa là không nên thử lại.
	// Khi thất bại thì kept vô nghĩa, phía gọi nên bỏ qua nó.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin trả về giới hạn giao phát mỗi phút mà nền tảng chính thức khuyến
	// nghị, làm giá trị giới hạn mặc định khi tạo mới instance kênh. Trả về 0 nghĩa là
	// không có giới hạn đã biết.
	DefaultRatePerMin() int
	// SecretKeys trả về tên các khóa thuộc về thông tin xác thực trong cấu hình kênh.
	// Khi API hiển thị lại thì giá trị các khóa này bị che (mask), khi cập nhật mà nhận
	// được giá trị mask thì giữ nguyên giá trị gốc trong kho. Chỉ bản thân triển khai mới
	// biết field nào tính là thông tin xác thực (toàn bộ địa chỉ Webhook của WeCom chính là
	// thông tin xác thực, còn của DingTalk chỉ là phần secret trong đó), nên tri thức này
	// phải do kênh cung cấp, không thể để tầng trên đoán.
	SecretKeys() []string
	// DestinationKeys trả về tên các khóa quyết định "tin nhắn gửi đi đâu" trong cấu hình kênh.
	//
	// Cũng là thứ liên quan đến bảo mật như SecretKeys: địa chỉ đích và thông tin xác thực
	// là hai bộ field độc lập, nếu cho phép "chỉ đổi địa chỉ, giữ nguyên thông tin xác thực",
	// bất kỳ ai sửa được cấu hình kênh đều có thể khiến thông tin xác thực thật trong kho
	// được gửi đến máy chủ do mình kiểm soát, và việc che cấu hình kênh hoàn toàn mất ý nghĩa.
	// Chi tiết xem PrepareConfigUpdate.
	DestinationKeys() []string
}

// registry là bảng đăng ký kênh. Cố ý dùng literal tường minh thay vì tự đăng ký qua init():
// như vậy "có những kênh nào" nhìn hết được ở một chỗ, và khi thêm kênh mới, chỗ nào bỏ sót
// sẽ bị lộ ra ngay khi biên dịch, thay vì dựa vào side effect lúc chạy.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get lấy triển khai kênh theo loại.
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind báo cáo kind có phải loại kênh được hỗ trợ không.
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds trả về toàn bộ loại kênh được hỗ trợ, sắp xếp theo thứ tự từ điển
// (để dropdown UI hiển thị ổn định).
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError đánh dấu một thất bại giao phát không nên thử lại: sai thông tin xác thực,
// đích từ chối, body yêu cầu bất hợp lệ, v.v.
// Thử lại chỉ có ý nghĩa với lỗi tạm thời (mạng chập chờn, bị giới hạn tần suất, 5xx phía đối
// phương); thử lại lùi bước lặp đi lặp lại với thất bại vĩnh viễn vừa không thành công, vừa
// cuốn mất lỗi thật trong nhật ký thử lại.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent đánh dấu err là thất bại vĩnh viễn. Khi err là nil thì trả về nil,
// tiện viết thành `return Permanent(someCheck())`.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent báo cáo trên chuỗi err có mang dấu thất bại vĩnh viễn không.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- helper đọc cấu hình ----
//
// Cấu hình kênh đến từ cột JSONB của cơ sở dữ liệu, sau khi giải tuần tự bằng
// encoding/json là map[string]any, số luôn là float64, mảng là []any. Các helper dưới đây
// thống nhất lớp chuyển đổi này, đồng thời chịu được sai lệch kiểu do người dùng để trống
// trên UI (như nhập cổng thành chuỗi).

// cfgString lấy mục cấu hình chuỗi, luôn cắt khoảng trắng đầu cuối — sao chép dán từ form
// web rất dễ dính theo.
func cfgString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt lấy mục cấu hình số nguyên, tương thích cả hai nguồn float64 (mặc định JSON) và chuỗi.
func cfgInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool lấy mục cấu hình boolean, tương thích chuỗi "true"/"1".
func cfgBool(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings lấy mục cấu hình mảng chuỗi, tự động cắt khoảng trắng và bỏ chuỗi rỗng.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// Cũng chấp nhận chuỗi đơn, tiện cho việc gửi form khi chỉ có một giá trị.
		if s := cfgString(cfg, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap lấy mục cấu hình map chuỗi (như header HTTP tùy chỉnh), khóa và giá trị đều cắt
// khoảng trắng, bỏ khóa rỗng.
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}
