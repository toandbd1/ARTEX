package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/harness"
)

// runTrace retains the latest tool call so an interrupted run can identify the
// operation that was still in flight.
type runTrace struct {
	startedAt time.Time
	id        string
	name      string
	input     string
	at        time.Time
	pending   bool
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

var reasonHint = map[harness.TerminalReason]string{
	harness.ReasonCompleted:         "mô hình kết thúc lượt này bình thường, nhưng không để lại tóm tắt dạng chữ; dữ kiện và tài sản lấy theo bản ghi gọi công cụ của lượt này",
	harness.ReasonMaxTurns:          "đạt giới hạn số bước (MaxTurns): SDK đã thực hiện khâu kết thúc và ghi lại dữ kiện và tài sản, ý đồ sẽ được đánh dấu là exhausted, để người lập kế hoạch đổi hướng tiếp tục, chứ không xử lý như thất bại",
	harness.ReasonTimeout:           "đạt ngân sách thời gian thực của một lần chạy (MaxDuration): đến giờ sẽ ngắt công cụ đang chạy và vào khâu kết thúc tại chỗ, ghi lại dữ kiện và tài sản đã nhận diện, ý đồ sẽ được đánh dấu là exhausted",
	harness.ReasonModelError:        "gọi mô hình hoặc API thất bại (mạng, xác thực, giới hạn tốc độ, 5xx của nhà cung cấp...), sau khi hết lượt thử lại ý đồ được đánh dấu là blocked — lỗi tầng truyền tải khiến ý đồ này gần như chưa thực sự thám thính được; xem quá trình thực thi của nó (get_worker_trace) rồi mới quyết định điều lại hay đổi cách",
	harness.ReasonBlockingLimit:     "độ dài ngữ cảnh đạt giới hạn cứng, request bị chặn trước khi phát đi; nên thu hẹp độ mịn của ý đồ hoặc nén kết quả công cụ trả về",
	harness.ReasonPromptTooLong:     "prompt quá dài và đã cạn lượt thử lại nén ngữ cảnh, không thể tiếp tục thực thi",
	harness.ReasonImageError:        "mô hình hiện tại không hỗ trợ nội dung đa phương thức của lượt này; hãy chuyển sang mô hình hỗ trợ thị giác hoặc tránh để công cụ trả về hình ảnh",
	harness.ReasonStopHookPrevented: "hook Stop ngăn lượt này kết thúc, sau đó không tiếp tục được; hãy kiểm tra quy tắc Guard của tác vụ có quá chặt không",
	harness.ReasonHookStopped:       "công cụ hoặc hook chủ động dừng việc thực thi tiếp, ví dụ mục tiêu vượt phạm vi hoặc lệnh bị tắt; hãy kiểm tra giải thích chặn của tool_result cuối cùng",
	harness.ReasonAbortedStreaming:  "lần chạy bị hủy trong giai đoạn mô hình sinh output streaming",
	harness.ReasonAbortedTools:      "lần chạy bị hủy trong giai đoạn thực thi công cụ",
}

// terminalText renders a terminal event with no final text into a compact summary
// and a Markdown detail block.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason := term.Reason
	aborted := reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools
	// Prompt may return ctx.Err directly without a terminal event. Preserve the
	// cancellation cause instead of falling back to an empty/unknown terminal reason.
	if reason == "" && ctx.Err() != nil {
		aborted = true
	}

	var sum string
	if aborted {
		_, short, _, ok := AbortReason(ctx)
		if !ok {
			short = "không lấy được lý do hủy"
		}
		stage := "trong quá trình thực thi"
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = "giai đoạn mô hình xuất kết quả"
		case harness.ReasonAbortedTools:
			stage = "giai đoạn thực thi công cụ"
		}
		sum = "(lần chạy bị gián đoạn: " + short + "; dừng ở " + stage + progressSuffix(term, tr) + ", chưa hoàn thành)"
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = "(đạt giới hạn ngân sách chạy (" + string(reason) + "), đã kết thúc và ghi lại dữ kiện" + progressSuffix(term, tr) + "; lần này không có tóm tắt dạng chữ)"
	} else {
		hint := terminalReasonHint(reason)
		sum = "(không có tóm tắt dạng chữ, trạng thái cuối " + terminalReasonLabel(reason) + ": " + firstLine(hint, 80) + ")"
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	displayReason := terminalReasonLabel(reason)
	fmt.Fprintf(&b, "- **Trạng thái cuối**: `%s` - %s\n", displayReason, terminalReasonHint(reason))
	if aborted {
		code, _, why, ok := AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **Lý do gián đoạn** (`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **Lý do gián đoạn**: không lấy được; bên hủy có thể chưa đính kèm lý do có tên qua context.WithCancelCause\n")
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, "- **Lỗi tầng dưới**: `%v`\n", term.Err)
	}
	if aborted && strings.TrimSpace(term.Text) != "" {
		b.WriteString("- **Phần output đã sinh trước khi hủy**:\n\n")
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **Đã thực thi**: %d lượt hội thoại mô hình\n", term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **Thời gian chạy lần này**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **Token tích lũy**: đầu vào %d / đầu ra %d / đọc cache %d / ghi cache %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString("- **Gọi công cụ**: lần chạy này đã kết thúc trước khi phát ra bất kỳ lời gọi công cụ nào\n")
	} else if tr.pending {
		fmt.Fprintf(&b, "- **Công cụ đang thực thi khi bị gián đoạn**: `%s` (đã chạy %s, **chưa trả về kết quả**)\n\n  ```json\n  %s\n  ```\n",
			tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, "- **Công cụ cuối cùng trước khi gián đoạn**: `%s` (đã trả về bình thường)\n", tr.name)
	}
	return sum, b.String()
}

func terminalReasonLabel(reason harness.TerminalReason) string {
	if reason == "" {
		return "context_canceled"
	}
	return string(reason)
}

func terminalReasonHint(reason harness.TerminalReason) string {
	if hint := reasonHint[reason]; hint != "" {
		return hint
	}
	if reason == "" {
		return "context của lần chạy đã bị hủy, nhưng tầng dưới không sinh sự kiện Terminal"
	}
	return "trạng thái cuối không xác định; harness có thể đã thêm TerminalReason mới, hãy bổ sung reasonHint"
}

func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d lượt", term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", đã chạy " + strings.Join(parts, " / ")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}
