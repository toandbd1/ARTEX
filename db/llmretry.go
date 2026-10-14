package db

import (
	"encoding/json"
	"time"
)

// Chiến lược retry LLM: cấu hình toàn cục "số lần + khoảng cách" cho năm tầng retry, xem docs/LLM重试设计.md.
// Lưu trong một giá trị JSON của bảng settings — đây là tham số vận hành dùng chung cho cả máy, không đáng tạo một bảng riêng;
// việc đọc có giá trị mặc định tích hợp sẵn để dự phòng, nên khi khóa không tồn tại (cơ sở dữ liệu mới/chưa từng cấu hình) thì hành vi hoàn toàn giống thời điểm còn hardcode hằng số.

const settingLLMRetryPolicy = "llm_retry_policy"

// RetryRule is one layer's knob pair. The zero value means "unset":
//
//	Attempts   0 = dùng số lần mặc định tích hợp sẵn; -1 = tắt retry ở tầng này; >0 = dùng giá trị đó
//	IntervalMS 0 = dùng chiến lược khoảng cách vốn có của tầng đó (thường là exponential backoff); >0 = đổi sang khoảng cách cố định tính bằng mili giây
//
// -1 nghĩa là "tắt một cách rõ ràng" chứ không phải "0 lần", vì 0 đã được dùng cho "chưa cấu hình".
type RetryRule struct {
	Attempts   int `json:"attempts"`
	IntervalMS int `json:"interval_ms"`
}

// Interval returns the configured fixed interval, or 0 when unset (caller keeps
// its own default ladder).
func (r RetryRule) Interval() time.Duration {
	if r.IntervalMS <= 0 {
		return 0
	}
	return time.Duration(r.IntervalMS) * time.Millisecond
}

// Or returns the rule with each unset field filled in from fallback. Used to
// layer a profile override on top of the global policy field by field, so a
// profile that only pins the interval still inherits the global count.
func (r RetryRule) Or(fallback RetryRule) RetryRule {
	if r.Attempts == 0 {
		r.Attempts = fallback.Attempts
	}
	if r.IntervalMS == 0 {
		r.IntervalMS = fallback.IntervalMS
	}
	return r
}

// retry knob bounds. A count above the cap turns a blip into a token bonfire;
// an interval above an hour outlives any transient failure worth waiting out.
const (
	maxRetryAttempts   = 20
	maxRetryIntervalMS = 3600_000 // 1h
)

// Clamped returns the rule with out-of-range values pulled back into the sane
// band (attempts within [-1, 20], interval within [0, 1h]).
func (r RetryRule) Clamped() RetryRule {
	if r.Attempts < -1 {
		r.Attempts = -1
	}
	if r.Attempts > maxRetryAttempts {
		r.Attempts = maxRetryAttempts
	}
	if r.IntervalMS < 0 {
		r.IntervalMS = 0
	}
	if r.IntervalMS > maxRetryIntervalMS {
		r.IntervalMS = maxRetryIntervalMS
	}
	return r
}

// Clamped bounds a profile's override the same way the global policy is bounded,
// so a hand-crafted API payload can't land a value the CHECK constraint rejects.
func (o RetryOverride) Clamped() RetryOverride {
	o.Connect, o.Empty, o.Stream = o.Connect.Clamped(), o.Empty.Clamped(), o.Stream.Clamped()
	return o
}

// LLMRetryPolicy holds the five-layer retry configuration. Connect/Empty/Stream are the
// per-request layers (a profile may override them, see LLMProfile.Retry);
// Breaker and Intent are process-wide by nature and live only here.
type LLMRetryPolicy struct {
	// Connect: retry thiết lập kết nối của SDK (reset kết nối/timeout/429/5xx, trước khi stream bắt đầu). Mặc định 3 lần, exponential backoff.
	Connect RetryRule `json:"connect"`
	// Empty: retry khi SDK trả phản hồi rỗng (hoàn tất nhưng không có content block, chỉ ở định dạng openai). Mặc định 2 lần, exponential backoff.
	Empty RetryRule `json:"empty"`
	// Stream: retry trong cửa sổ an toàn của cùng provider (phát lại khi stream đứt trước khi giao output). Mặc định 2 lần, backoff mũ bắt đầu từ 0.5s (trần 4s).
	Stream RetryRule `json:"stream"`
	// Breaker: ngắt mạch dạng vòng quay. Attempts=số lần lỗi tức thời liên tiếp để kích hoạt ngắt mạch (mặc định 3, -1=lỗi tức thời không ngắt mạch,
	// lỗi cứng như hết số dư/khóa không hợp lệ vẫn ngắt mạch ngay lập tức); IntervalMS=thời gian làm nguội cố định (0=thang mặc định 1/5/30min).
	Breaker RetryRule `json:"breaker"`
	// Intent: chạy lại toàn bộ ý đồ sau khi worker kết thúc với model_error. Mặc định 2 lần, cố định 3s.
	Intent RetryRule `json:"intent"`
}

// Clamped returns the policy with every rule clamped.
func (p LLMRetryPolicy) Clamped() LLMRetryPolicy {
	p.Connect, p.Empty, p.Stream = p.Connect.Clamped(), p.Empty.Clamped(), p.Stream.Clamped()
	p.Breaker, p.Intent = p.Breaker.Clamped(), p.Intent.Clamped()
	return p
}

// LLMRetryPolicy reads the global retry policy. A missing or unparseable value
// yields the zero policy — i.e. every layer on its built-in default.
func (d *DB) LLMRetryPolicy() LLMRetryPolicy {
	var p LLMRetryPolicy
	if d == nil {
		return p
	}
	raw, ok, err := d.GetSetting(settingLLMRetryPolicy)
	if err != nil || !ok || raw == "" {
		return p
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return LLMRetryPolicy{}
	}
	return p.Clamped()
}

// SetLLMRetryPolicy persists the global retry policy (values are clamped first).
func (d *DB) SetLLMRetryPolicy(p LLMRetryPolicy) error {
	raw, err := json.Marshal(p.Clamped())
	if err != nil {
		return err
	}
	return d.SetSetting(settingLLMRetryPolicy, string(raw))
}
