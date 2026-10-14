package db

import (
	"fmt"
	"testing"
	"time"
)

// InsertSettingIfAbsent là lớp chống lưng của auth.password_hash: việc kiểm tra GetSetting của phía gọi có thể
// mất hiệu lực vì cơ sở dữ liệu báo lỗi, cũng có thể bị request đồng thời chen ngang (bcrypt phải chạy vài chục mili giây), nên bảo đảm "chỉ lần đầu
// mới đặt được" phải dựa vào ràng buộc khóa chính, chứ không phải if ở tầng trên.
func TestInsertSettingIfAbsentDoesNotOverwrite(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	// Dùng key riêng của test, tuyệt đối không đụng vào auth.password_hash thật trong DB dev.
	key := fmt.Sprintf("test.insert_if_absent.%d", time.Now().UnixNano())
	defer func() { _, _ = d.Exec(`DELETE FROM settings WHERE key=$1`, key) }()

	inserted, err := d.InsertSettingIfAbsent(key, "first")
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatal("ghi lần đầu phải trả về inserted=true")
	}

	inserted, err = d.InsertSettingIfAbsent(key, "second")
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("khi khóa đã tồn tại phải trả về inserted=false")
	}

	got, ok, err := d.GetSetting(key)
	if err != nil || !ok {
		t.Fatalf("GetSetting: ok=%v err=%v", ok, err)
	}
	if got != "first" {
		t.Fatalf("giá trị bị ghi đè thành %q, phải giữ %q", got, "first")
	}
}
