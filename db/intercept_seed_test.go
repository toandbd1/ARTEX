package db

import (
	"regexp"
	"testing"
)

// Rule tích hợp sẵn "đường dẫn API dạng xóa" khớp với toàn bộ chuỗi JSON tool_input,
// vì vậy các ca kiểm thử được đưa ra trực tiếp ở dạng JSON, giống với subject mà
// Interceptor thực sự nhận được.
func TestDeleteEndpointPathPattern(t *testing.T) {
	re := regexp.MustCompile(deleteEndpointPathPattern)

	hit := []string{
		`{"command":"curl -s 'http://t.com/api/user/delete?id=1'"}`,    // GET vào API xóa
		`{"command":"curl -X POST http://t.com/admin/delete -d id=1"}`, // POST vào API xóa
		`{"command":"curl 'http://t.com/api/deleteAll'"}`,
		`{"command":"curl 'http://t.com/api/delete_user?id=1'"}`,
		`{"command":"curl 'http://t.com/api/delete-user?id=1'"}`,
		`{"url":"http://t.com/api/remove?id=1"}`,
		`{"command":"curl http://t.com/files/unlink/3"}`,
		`{"command":"curl http://t.com/api/del?id=2"}`,
		`{"command":"curl -X POST http://t/v1/erase"}`,
		`{"command":"curl http://t/admin/destroyAll"}`, // rule đường dẫn ở v1 không cho phép hậu tố, bổ sung ở đây
	}
	for _, s := range hit {
		if !re.MatchString(s) {
			t.Errorf("đáng lẽ phải khớp nhưng lại được phép qua: %s", s)
		}
	}

	// Sau động từ bắt buộc phải có dấu phân cách, để tránh chặn nhầm các đường dẫn chỉ đọc như /delivery, /details.
	miss := []string{
		`{"command":"curl 'http://t.com/api/delivery?id=1'"}`,
		`{"command":"curl 'http://t.com/order/details'"}`,
		`{"command":"curl 'http://t.com/api/delta/sync'"}`,
		`{"command":"curl 'http://t.com/user/delegate'"}`,
		`{"command":"curl 'http://delete.example.com/'"}`, // động từ xóa xuất hiện trong tên miền chứ không phải đường dẫn
		`{"command":"curl 'http://t.com/remote/status'"}`,
		`{"command":"nmap -p80 10.0.0.1"}`,
	}
	for _, s := range miss {
		if re.MatchString(s) {
			t.Errorf("chặn nhầm: %s", s)
		}
	}
}
