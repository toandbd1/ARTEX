package server

import (
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// Phân giải chính sách retry phía server, xem tài liệu thiết kế retry LLM trong docs/. Trong năm lớp:
//   - kết nối / phản hồi rỗng / cửa sổ an toàn cùng provider là loại "đi theo endpoint", mỗi cấu hình LLM
//     có thể ghi đè mặc định toàn cục (mục nào của profile để trống thì kế thừa toàn cục, toàn cục cũng
//     không cấu hình thì dùng mặc định tích hợp sẵn);
//   - ngắt mạch (circuit breaker) / chạy lại ý đồ là cấp tiến trình, chỉ có một bản toàn cục.
//
// Chính sách toàn cục đọc một lần một dòng settings từ DB, các điểm gọi đều nằm trên đường tần suất thấp
// (dựng provider, kết thúc work, lưu cấu hình), không đáng thêm một lớp cache; tham số ngắt mạch là ngoại
// lệ — nó phải đọc mỗi lần trên đường xử lý lỗi, nên được applyRetryPolicy đẩy cho Registry lưu giữ.

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// Số lần ở đây giữ nguyên ngữ nghĩa gốc "0 = mặc định / số âm = tắt": MaxRetries /
		// EmptyResponseRetries của SDK tương cấu hoàn toàn với nó, cứ để SDK tự phân giải.
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// Giá trị mặc định của ngắt mạch (cooldown luân chuyển), khớp với giá trị tích hợp sẵn của llmpool — ở đây chỉ ghi đè khi "người dùng đã cấu hình giá trị".
// Giá trị mặc định của chạy lại ý đồ xem modelErrorRetries / modelErrorRetryBackoff trong engine.go.

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob —— "số lần retry
// phản hồi rỗng": cả hai là hai phương thức của cùng một việc. Lớp SDK quản trường hợp
// "không có bất kỳ khối nội dung nào", bằng cách gửi lại nguyên văn cùng một request;
// ở đây quản trường hợp "chỉ có suy nghĩ (thinking), không có nội dung chính cũng không có
// công cụ", bằng cách nối thêm một chỉ thị để mô hình tiếp tục với phần suy nghĩ đã có
// (gửi lại nguyên văn không có ý nghĩa với loại chạy không do hình dạng ngữ cảnh quyết định
// này). Tiêu chuẩn xét "rỗng" khác nhau vì SDK căn cứ vào "đã từng yield sự kiện hay chưa",
// mà bản thân delta suy nghĩ cũng là sự kiện — nhưng khi người dùng cấu hình "retry phản hồi
// rỗng mấy lần", ý họ muốn nói là "mô hình không sinh ra nội dung thực chất thì chạy lại",
// nên hai lớp dùng chung một số lần mới khớp với mental model đó.
//
// Đọc chính sách toàn cục thay vì override của một profile nào đó: giữa một run có thể đổi
// profile do failover, còn đây là van giới hạn tổng lượng của cả ý đồ, không nên đổi theo
// endpoint. Ngữ nghĩa tương cấu với emptyRetries() của SDK:
// 0 = mặc định defaultEmptyTurnNudges; -1 (số âm) = tắt chạy tiếp khi rỗng; >0 = dùng giá trị đó.
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
