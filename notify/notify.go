// Package notify triển khai lớp adapter kênh đẩy thông báo IM / email cho các lỗ hổng được phát hiện.
//
// Phân tầng: package này là **package lá**, chỉ phụ thuộc thư viện chuẩn. Nó không biết
// cơ sở dữ liệu, không biết server. Cấu hình kênh được truyền vào dưới dạng map[string]any
// (tương ứng cột JSONB notification_channels.config), nội dung cần đẩy được truyền vào dưới
// dạng Message. Lợi ích của việc tách này: những chỗ thực sự dễ sai như tính chữ ký, cắt
// UTF-8, khớp bộ lọc có thể unit test độc lập với PostgreSQL, phía host chỉ cần dàn xếp ở tầng server.
//
// Quy ước đồng thời: triển khai của Channel phải **phi trạng thái**. Cùng một instance
// Channel sẽ được nhiều cấu hình kênh (thậm chí nhiều instance bot của cùng một kênh)
// tái sử dụng đồng thời, mọi thông tin xác thực đều truyền qua tham số cfg, không được
// cache những thứ như webhook URL vào field của chính triển khai.
package notify

// Định danh loại kênh. Giá trị đồng thời là tập hợp hợp lệ của notification_channels.kind,
// được kiểm tra whitelist ở tầng server (giống findings.status, không dùng DB CHECK để
// tiện thêm kênh sau này).
const (
	KindDingTalk = "dingtalk" // Bot tùy chỉnh DingTalk
	KindFeishu   = "feishu"   // Bot tùy chỉnh Feishu (gồm Lark)
	KindWeCom    = "wecom"    // Bot nhóm WeCom (WeChat doanh nghiệp)
	KindWebhook  = "webhook"  // Webhook tổng quát: tùy chỉnh method/header/template JSON
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // Email SMTP
)

// Loại sự kiện, tương ứng notification_events.kind.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind là giá trị dự phòng cho kind để trống trong config.
const InitKind = KindDingTalk

// severityRank ánh xạ mức độ lỗ hổng thành số thứ tự có thể so sánh. Mức không xác định
// trả về 0, vì vậy mọi thiết lập min_severity đều sẽ chặn mức không xác định — khi nghi
// ngờ thì không đẩy, tránh spam báo động giả.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank trả về số thứ tự của mức độ; mức không xác định trả về 0.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel trả về tên mức độ tiếng Việt kèm emoji, dùng cho tiêu đề tin nhắn và
// phối màu card. Mức không xác định được hiển thị nguyên bản, không bịa.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 Nghiêm trọng"
	case "high":
		return "🟠 Cao"
	case "medium":
		return "🟡 Trung bình"
	case "low":
		return "🔵 Thấp"
	default:
		return severity
	}
}

// StatusLabel dịch trạng thái xử lý sang tiếng Việt, dùng cho tin nhắn thay đổi trạng thái.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "Chờ xử lý"
	case "in_progress":
		return "Đang xử lý"
	case "confirmed":
		return "Đã xác nhận"
	case "resolved":
		return "Đã xử lý"
	case "fixed":
		return "Đã vá"
	case "false_positive":
		return "Báo động giả"
	case "ignored":
		return "Bỏ qua"
	case "duplicate":
		return "Trùng lặp"
	case "risk_accepted":
		return "Chấp nhận rủi ro"
	default:
		return status
	}
}

// AtLeast kiểm tra severity có đạt ngưỡng min không. min rỗng nghĩa là không đặt ngưỡng, luôn cho qua.
// Lưu ý mức severity không xác định có số thứ tự 0, sẽ bị mọi min khác rỗng từ chối (xem chú thích severityRank).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
