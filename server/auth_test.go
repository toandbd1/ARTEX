package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// GetSetting chỉ khác nhau một error giữa "khóa không tồn tại" và "đọc bị lỗi": cả hai
// trường hợp value đều là chuỗi rỗng. Nếu handler liên quan mật khẩu coi error là "chưa
// đặt mật khẩu", cửa khởi tạo sẽ mở toang trong lúc cơ sở dữ liệu chập chờn — authInit sẽ
// cho một request chưa xác thực đi qua để ghi đè mật khẩu quản trị đã có, còn authStatus
// sẽ đưa frontend thẳng tới /setup để làm đúng việc đó.
//
// Hai test này đóng connection pool của handler để tạo lỗi đọc, khẳng định cả hai chỗ đều fail closed.
func TestAuthStatusFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	// Đóng pool để các GetSetting sau trả về error thay vì sql.ErrNoRows.
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	s.authStatus(w, httptest.NewRequest("GET", "/api/auth/status", nil))

	if w.Code != 503 {
		t.Fatalf("status=%d want 503 (coi lỗi đọc là chưa khởi tạo sẽ đưa người dùng tới /setup ghi đè mật khẩu); body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Initialized *bool `json:"initialized"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err == nil && payload.Initialized != nil {
		t.Fatalf("khi đọc lỗi không được trả về initialized, nhận được %v", *payload.Initialized)
	}
}

func TestAuthInitFailsClosedWhenDataSourceUnavailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"password":"correct horse battery"}`)
	s.authInit(w, httptest.NewRequest("POST", "/api/auth/init", body))

	if w.Code != 503 {
		t.Fatalf("status=%d want 503 (cho qua khi đọc lỗi sẽ để request chưa xác thực ghi đè mật khẩu đã có); body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "token") {
		t.Fatalf("khi đọc lỗi không được cấp token: %s", w.Body.String())
	}
}

func TestValidatePassword(t *testing.T) {
	for _, tc := range []struct {
		name, pw string
		wantErr  bool
	}{
		{"rỗng", "", true},
		{"bảy ký tự", "1234567", true},
		{"tám ký tự", "12345678", false},
		{"tám ký tự Unicode tính theo số ký tự chứ không phải số byte", strings.Repeat("ể", 8), false},
		{"ba ký tự Unicode đủ 9 byte nhưng chỉ có 3 ký tự", strings.Repeat("ể", 3), true},
		{"72 byte", strings.Repeat("a", 72), false},
		{"73 byte vượt giới hạn bcrypt", strings.Repeat("a", 73), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validatePassword(tc.pw); (got != "") != tc.wantErr {
				t.Fatalf("validatePassword(%q)=%q, wantErr=%v", tc.pw, got, tc.wantErr)
			}
		})
	}
}
