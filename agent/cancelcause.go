package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "người dùng đã tạm dừng tác vụ",
		"Người dùng đã tạm dừng tác vụ qua giao diện điều khiển tác vụ (POST /api/tasks/{id}/control, action=pause). Lần chạy Planner/Worker này bị chủ động hủy; các ý đồ đang chạy sẽ quay về frontier(open), sau khi khôi phục tác vụ sẽ được nhận lại và thực thi từ đầu")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "Agent điều phối đã tạm dừng tác vụ",
		"Agent điều phối đã gọi công cụ pause_task để tạm dừng tác vụ này. Lần chạy Planner/Worker này bị chủ động hủy; các ý đồ đang chạy sẽ quay về frontier(open), sau khi khôi phục sẽ thực thi lại")
	AbortTaskDeleted = cause("task_deleted", "tác vụ đã bị xóa",
		"Tác vụ đang bị xóa (DELETE /api/tasks/{id}), rào chắn xóa đã hủy Planner, Worker và Agent chính đang chạy của tác vụ này; kết quả của lần chạy này sẽ không còn được sử dụng")
	AbortPausedOnReload = cause("paused_on_reload", "backend đã khôi phục trạng thái tạm dừng của tác vụ",
		"Khi khởi động, backend đã khôi phục trạng thái tạm dừng của tác vụ dựa trên trạng thái lưu trong cơ sở dữ liệu. Lần chạy này bị hủy; trong điều kiện bình thường giai đoạn khôi phục không có Agent nào đang chạy")
	AbortGoalMet = cause("goal_met", "planner đã xác định mục tiêu tác vụ đạt được",
		"Planner xác định mục tiêu tác vụ đã đạt được và chuyển tác vụ sang done, sau đó hủy các Worker còn đang chạy; các ý đồ này sẽ được đánh dấu stopped chứ không phải thất bại")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "đã hết thời gian chờ khép lại khi tác vụ quá thời gian",
		"Sau khi tác vụ đạt timeout, hệ thống chờ các Worker đang chạy khép lại êm đẹp, nhưng 90 giây gia hạn drain vẫn không đủ nên thực hiện hủy cứng; các ý đồ sẽ được đánh dấu exhausted, các fact và tài sản đã ghi trong giai đoạn khép lại vẫn được giữ lại")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "planner đã chấm dứt ý đồ này",
		"Planner đã gọi kill_work để chủ động chấm dứt ý đồ này, thường nghĩa là hướng đi đã lệch hoặc không còn giá trị tiếp tục; ý đồ sẽ được đánh dấu stopped và không tự động được nhận lại")
	AbortWorkPausedByUser = cause("work_paused_by_user", "người dùng đã tạm dừng ý đồ Worker này",
		"Người dùng đã tạm dừng Worker đang chạy. Lần gọi này bị hủy, ý đồ chuyển sang paused; các ý đồ, fact, lỗ hổng và bản ghi hoạt động đã đăng ký đều được giữ lại, sau khi khôi phục sẽ thực thi lại từ đầu")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "người dùng đã xóa ý đồ Worker này",
		"Người dùng đã xóa Worker đang chạy. Lần gọi này bị hủy; sau khi Worker rời vùng ghi, server xử lý ý đồ theo chế độ xóa mà người dùng chọn — xóa giả chỉ đánh dấu là đã xóa và giữ toàn bộ sản phẩm, xóa thật sẽ xóa tầng theo ý đồ này cùng các node hạ nguồn chỉ do nó chống đỡ")
	AbortWorkFinished = cause("work_finished", "Worker đã kết thúc bình thường và giải phóng context",
		"Worker đã kết thúc bình thường, engine giải phóng tài nguyên context của nó trong detachWork. Đây không phải gián đoạn chạy; nếu nó xuất hiện trong thông báo gián đoạn, nghĩa là sự kiện hủy và sự kiện kết thúc đã xảy ra tranh chấp")
	AbortPausedRaceGuard = cause("paused_race_guard", "từ chối khởi động lần chạy mới trong thời gian tác vụ tạm dừng",
		"Khi tác vụ đang ở trạng thái tạm dừng, engine từ chối cấp context thực thi mới, dùng để tránh tranh chấp giữa claim và tạm dừng khiến Worker tiếp tục khởi động; các ý đồ đã nhận sẽ quay về frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "người dùng đã dừng lượt hội thoại này",
		"Người dùng đã nhấn dừng, chủ động kết thúc lần chạy Agent chính hoặc Agent phiên này. Các bản ghi hoạt động đã tạo sẽ được giữ lại, có thể tiếp tục gửi tin nhắn tiếp theo")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "tác vụ tạm dừng và kết thúc hội thoại của Agent chính",
		"Khi người dùng tạm dừng tác vụ, hội thoại Agent chính đang chạy cũng bị hủy đồng bộ. Các bản ghi hoạt động đã tạo sẽ được giữ lại; sau khi khôi phục tác vụ, tin nhắn lượt này sẽ không tự động phát lại")
	AbortChatTurnFinished = cause("chat_turn_finished", "lượt hội thoại này đã kết thúc bình thường và giải phóng context",
		"Lượt hội thoại này đã kết thúc bình thường, server đang giải phóng tài nguyên context của lượt đó. Đây không phải gián đoạn chạy; nếu nó xuất hiện trong thông báo gián đoạn, nghĩa là sự kiện hủy và sự kiện kết thúc đã xảy ra tranh chấp")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "tiến trình backend đang tắt",
		"Tiến trình backend nhận SIGINT hoặc SIGTERM, đang khởi động lại, cập nhật hoặc tắt. Tất cả Agent đang chạy sẽ bị hủy; sau khi khởi động lại, các ý đồ running còn sót lại sẽ được đặt lại về open và thực thi lại")
	AbortRunHardTimeout = cause("run_hard_timeout", "bộ đệm timeout cứng của một lần chạy đã kích hoạt",
		"Một lần chạy vượt quá ngân sách đồng hồ mềm cộng thêm gia hạn, nghĩa là yêu cầu mô hình hoặc một công cụ nào đó lâu không trả về, khiến việc khép lại ở ranh giới lượt bình thường không thể thực thi. Hãy kiểm tra kỹ lời gọi công cụ cuối cùng chưa trả về trước khi gián đoạn")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "context thượng nguồn đã đến deadline",
			"Context thượng nguồn đã đến deadline, nhưng bên đặt không đính kèm nguyên nhân cụ thể qua WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "bên hủy không đính kèm nguyên nhân cụ thể",
			"Context thượng nguồn đã bị hủy, nhưng bên hủy không đính kèm nguyên nhân cụ thể qua context.WithCancelCause; hãy đăng ký nguyên nhân tại agent/cancelcause.go và nối vào điểm hủy đó", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
