package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter là hợp đồng của cột JSONB notification_channels.filter: điều kiện lọc của instance kênh.
// Mọi field đều tùy chọn, bỏ trống tức là "không lọc" — đây chính là ngữ nghĩa dự phòng cho
// cấu hình dị dạng, xem ParseFilter.
type Filter struct {
	// MinSeverity là ngưỡng mức độ tối thiểu (low/medium/high/critical), rỗng = không đặt ngưỡng.
	MinSeverity string `json:"min_severity"`
	// TaskIDs / AssetIDs là mảng rỗng nghĩa là không giới hạn; khác rỗng thì yêu cầu sự kiện
	// có giao với nó.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// VulnClassInclude rỗng nghĩa là nhận hết; khác rỗng thì yêu cầu vulnclass khớp một trong
	// các từ khóa. VulnClassExclude khớp bất kỳ từ khóa nào là loại (loại ưu tiên hơn bao gồm).
	// Cách khớp là chuỗi con không phân biệt hoa thường — an toàn hơn regex: người dùng cấu hình
	// sai regex sẽ không khiến kênh âm thầm mất tác dụng.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange quyết định kênh này có nhận sự kiện thay đổi trạng thái lỗ hổng không
	// (chỉ có ý nghĩa ở chế độ realtime).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter phân tích cấu hình lọc của kênh.
//
// **Không bao giờ trả về error.** Đây là lựa chọn thiết kế có chủ đích: khi cấu hình điều
// kiện lọc dị dạng thì luôn suy biến thành Filter zero-value (= không lọc = khớp tất cả),
// vì với một hệ thống thông báo lỗ hổng, **đẩy thừa một tin còn hơn nhiều so với âm thầm
// bỏ sót một tin mức cao**. Biến lỗi phân tích thành "không đẩy" tương đương trao cho người
// dùng một kênh trông có vẻ đã cấu hình xong nhưng thực tế không đẩy gì — đó là kiểu thất
// bại tệ nhất.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// Khi phân tích thất bại, f giữ nguyên zero-value, tức không lọc.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity báo cáo s có phải ngưỡng mức độ hợp lệ không (chuỗi rỗng nghĩa là
// không đặt ngưỡng).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate kiểm tra các field **có giá trị bị ràng buộc** trong cấu hình lọc, dùng khi lưu kênh.
//
// Tại sao phải chặn lúc ghi: phán định của Match với ngưỡng không xác định là `rank >= 0`,
// luôn đúng — nghĩa là min_severity gõ sai một chữ ("hgih") thì bộ lọc sẽ **âm thầm mất tác
// dụng** và biến thành "đẩy tất cả". Điều này nhất quán với định hướng "thà đẩy thừa chứ không
// đẩy thiếu" của package này (sẽ không sót), nhưng hậu quả là người dùng tưởng mình đang đẩy
// theo cấp độ mà thực tế đổ toàn bộ lỗ hổng vào nhóm, và không có dấu hiệu nào báo rằng họ
// cấu hình sai. Kiểu "suy giảm âm thầm" này đúng ra phải bị chặn ở đầu vào.
//
// Lưu ý Validate chỉ dùng cho đường **ghi**. Đường đọc vẫn đi theo ngữ nghĩa khoan dung của
// ParseFilter, để giá trị xấu đã tồn tại trong dữ liệu lịch sử không làm kênh không đọc ra được.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("mức tối thiểu %q không hợp lệ, các giá trị khả dụng: low / medium / high / critical, hoặc để trống nếu không giới hạn", f.MinSeverity)
	}
	return nil
}

// Match phán định một sự kiện có nên được giao phát đến kênh mang điều kiện lọc này không.
//
// **Không bao giờ trả về error**, lý do như ParseFilter: mọi bất thường nội bộ đều xử lý
// theo hướng "khớp". Thứ tự phán định: loại sự kiện → ngưỡng mức độ → phạm vi tác vụ/tài sản
// → từ khóa loại lỗ hổng.
func Match(f Filter, s Snapshot) bool {
	// Sự kiện thay đổi trạng thái chỉ có kênh bật tường minh mới nhận. Mặc định tắt, vì đại đa
	// số người dùng mong "đẩy thông báo" nghĩa là "phát hiện lỗ hổng mới", chứ không phải theo
	// dõi từng lần chuyển trạng thái như nhật ký.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Loại được ưu tiên: khớp bất kỳ từ khóa loại nào là bị loại, kể cả khi đồng thời khớp
	// danh sách bao gồm.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// Tập nhỏ thì quét tuyến tính là đủ; quy mô hai phía đều là "vài chục mục do người tích
	// chọn", chi phí dựng map lớn hơn lợi ích.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold báo cáo s có chứa bất kỳ từ khóa nào trong keywords không
// (không phân biệt hoa thường).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
