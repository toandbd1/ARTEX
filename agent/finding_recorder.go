package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**Bằng chứng lưu lượng của lỗ hổng (tùy chọn)**: khi gọi report_finding để báo cáo lỗ hổng, nếu có HTTP request/response đã xem và xác nhận là hỗ trợ kết luận lỗ hổng, có thể dùng traffic_refs để gắn các ID thật theo thứ tự tái hiện; tên miền và thời gian chỉ dùng để lọc ứng viên, không suy đoán liên quan. Với lỗ hổng không phải HTTP như TCP, chưa thu thập hoặc không có kết quả khớp chắc chắn thì bỏ qua hoặc truyền [], trong evidence giữ lại các bằng chứng kiểm chứng khác như output lệnh, nhật ký, nên ghi rõ lý do không gắn. Không đoán ID, cũng không dò lại chỉ để bổ sung gói tin."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
