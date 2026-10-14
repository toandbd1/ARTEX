package notify

// Snapshot là hợp đồng của cột JSONB notification_events.snapshot. Phía ghi là giao dịch
// lưu lỗ hổng của tầng db, phía đọc là engine giao phát và khớp bộ lọc của tầng server.
// Định nghĩa đặt ở package này vì nó là payload của "miền thông báo": db chỉ chịu trách
// nhiệm tuần tự hóa, không hiểu ý nghĩa các field.
//
// Tại sao lưu dư thừa các field lỗ hổng thay vì tra lại khi render: lỗ hổng về sau có thể
// bị đổi tên, đổi mức, đổi trạng thái, còn nội dung đẩy phải phản ánh kết luận **tại thời
// điểm xảy ra** — tra lại sẽ nhận được sự dẫn dắt nguy hiểm kiểu "về sau bị đổi thành low".
// Ngoài ra fan-out và render nhờ đó không cần JOIN ba bảng findings/tasks/assets.
type Snapshot struct {
	// Loại sự kiện: finding_created / finding_status_changed
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// Chỉ khác rỗng khi kind=finding_status_changed.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item là một lỗ hổng chờ đẩy, dùng cho kênh render.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets là tên hiển thị tài sản đã phân giải (như tên miền/IP). Do tầng server
	// điền — package này không đụng cơ sở dữ liệu nên không lấy được tên.
	Assets []string
	// DetailURL là liên kết quay lại chi tiết lỗ hổng; rỗng nghĩa là chưa cấu hình
	// public_base_url, render sẽ bỏ qua.
	DetailURL string
	// Chỉ dùng cho sự kiện thay đổi trạng thái; khi cả hai đều khác rỗng sẽ render
	// thành "Chờ xử lý → Đã vá".
	FromStatus string
	ToStatus   string
}

// IsStatusChange báo cáo mục này có phải sự kiện thay đổi trạng thái không.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title trả về tiêu đề hiển thị của mục: ưu tiên name do người đặt, lui về loại lỗ hổng
// vulnclass, khi cả hai đều rỗng thì dùng một placeholder — tuyệt đối không xuất tiêu đề rỗng.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(lỗ hổng chưa đặt tên)"
}

// Message là nội dung đầy đủ của một lần gửi qua kênh.
type Message struct {
	// Khi đẩy đơn lẻ thì độ dài là 1; khi đẩy tổng hợp (digest) thì là cả một lô.
	// Slice rỗng là bất hợp lệ, phía gọi phải bảo đảm có ít nhất một mục.
	Items []Item
	// Batch=true thì render theo tin nhắn tổng hợp (đổi tiêu đề, kèm cửa sổ thời gian và số mục).
	Batch bool
	// WindowMinutes là chu kỳ tổng hợp (phút), chỉ dùng khi Batch=true cho văn bản
	// "N phút gần đây". Cố ý truyền vào tường minh từ cấu hình thay vì tính time.Since
	// lúc render: giữ render mang tính xác định, mới dễ test.
	WindowMinutes int
	// HomeURL là địa chỉ bảng điều khiển của nền tảng (public_base_url toàn cục);
	// rỗng thì không kèm cổng vào bảng điều khiển.
	HomeURL string
}
