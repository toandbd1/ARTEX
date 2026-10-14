package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa là cơ chế「nén ngữ cảnh do mô hình điều khiển」được giới thiệu ở norma v0.4.0, là tính năng
// thử nghiệm của nền tảng do người dùng bật/tắt trong cài đặt hệ thống. Nó loại trừ lẫn nhau với
// compaction tích hợp: noaadapter.Enable là điểm vào duy nhất, một lần gắn vào là gắn bộ tiếp quản
// ngữ cảnh (Compactor), công cụ Compress và ba đoạn prompt thường trú; không gọi Enable tức là tắt
// (compaction tích hợp vẫn hoạt động bình thường). Công tắc được phân giải bởi noaEnabledFn do mỗi
// agent tiêm vào, đọc một lần mỗi run, nên việc chuyển đổi chỉ ảnh hưởng các run khởi động sau đó,
// không cần dựng lại agent.

// enableNoa nối noa vào opts khi resolver báo đang bật. archiveRoot là thư mục gốc bền vững của
// nguyên văn nén (lấy workDir toàn cục, các agent thống nhất đặt dưới <workDir>/noa, không rải rác
// theo thư mục tác vụ/ý đồ), sessionID đặt tên cho thư mục con lưu trữ bên dưới nó (duy nhất toàn
// cục, nên không xung đột trong cùng một thư mục gốc).
//
// noa là tính năng thử nghiệm: kết nối thất bại không được làm gián đoạn tác vụ thật. Khi có lỗi,
// báo qua onWarn và quay về nén tích hợp. Khi bật thành công thì xóa opts.Compaction để tránh
// agentcore cảnh báo「hai bộ quản lý ngữ cảnh cùng được đặt」.
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("bật nén noa thất bại, quay về nén tích hợp: " + err.Error())
		}
		return
	}
	// Compactor ghi đè Compaction, nhưng khi cả hai cùng tồn tại agentcore sẽ cảnh báo mỗi lần; xóa tường minh.
	opts.Compaction = nil
}
