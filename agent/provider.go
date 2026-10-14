// Package agent wires real LLM-driven planner and work agents (on top of the
// agent-core SDK) to the dual SQLite graph. See docs/ARTEX-架构设计.md
// §4.3 (planner) and §4.4 (work agent).
//
// Provider configuration is read from the environment so the system runs with
// any Anthropic- or OpenAI-format endpoint. If no key is configured, FromEnv
// returns ok=false and the exploration engine stays idle (an LLM is required).
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/compaction"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/transcript"
)

// Config describes the LLM backend resolved from the environment.
type Config struct {
	Format  llm.Format
	BaseURL string
	APIKey  string
	Model   string
	// Proxy routes all LLM requests through the given proxy URL (http/https/socks5,
	// optionally with user:pass@ credentials). Empty means direct — it does NOT
	// fall back to the standard *_PROXY environment variables.
	Proxy string
	// RatePerSecond / RatePerMinute cap the shared request rate across ALL agents
	// using the provider (0 = that window unlimited).
	RatePerSecond float64
	RatePerMinute float64
	// ContextWindowK is the model's context window in K tokens (user-configured),
	// used to size compaction thresholds. 0 = default; see CompactionWindow.
	ContextWindowK int
	// ThinkingType điều khiển độc lập trường "bật/tắt" suy nghĩ (thinking.type):
	//   "" = không gửi (mặc định, tương thích các mô hình không hỗ trợ trường này); "disabled" = tắt tường minh;
	//   "enabled" = bật. Hoàn toàn tách rời ReasoningEffort — một số API không có trường thinking,
	//   chỉ cần tham số cường độ là kích hoạt được suy nghĩ, nên hai thứ có thể đặt riêng.
	ThinkingType string
	// ReasoningEffort điều khiển độc lập trường "cường độ" suy nghĩ:
	//   "" = không gửi (mặc định); "low"/"medium"/"high"/"xhigh"/"max" = cường độ tương ứng.
	//   OpenAI ánh xạ thành reasoning_effort cấp trên; Anthropic ánh xạ thành output_config.effort.
	ReasoningEffort string
	// Stream điều khiển profile này có dùng API streaming (SSE) hay không. true (mặc định) = streaming;
	// false = không streaming thật (gửi stream:false, nhận JSON đầy đủ một lần, đi qua Provider.Complete).
	// Không streaming có thể né các cài đặt SSE tệ của một số gateway (frame rỗng, mất frame trường suy nghĩ),
	// đánh đổi bằng việc mất tiến độ thời gian thực / đếm token thời gian thực khi đang chạy.
	// Ánh xạ thành agentcore.Options.NonStreaming = !Stream.
	Stream bool
	// MaxTokens là giới hạn đầu ra (token) của một lần trả lời. 0 = không gửi trường này, dùng mặc định
	// phía server (hành vi lịch sử). Khác với ContextWindowK: cái sau là tổng dung lượng của mô hình,
	// chỉ dùng nội bộ để tính ngưỡng nén, không xuất hiện trong request; giá trị này gửi theo mỗi request.
	// Ánh xạ thành agentcore.Options.MaxTokens.
	MaxTokens int
	// MaxTokensField chọn tên trường request cho MaxTokens, chỉ có hiệu lực với format=openai:
	//   "" = max_tokens (mặc định); "max_completion_tokens" = trường mới.
	// Các mô hình suy luận OpenAI (dòng o/GPT-5) chỉ nhận cái sau, nhận max_tokens sẽ báo ngay
	// unsupported_parameter; còn đa số gateway tương thích chỉ nhận cái trước, nên không tự suy đoán,
	// để người dùng chọn theo endpoint.
	MaxTokensField string
	// SessionHeaderKey, khi khác rỗng, khiến mỗi request LLM mang một HTTP header tùy chỉnh,
	// tên header là giá trị này, giá trị header là [session id của phiên hiện tại]
	// (phiên chat=conv-<id>, worker=exp<x>-worker-i<intent>..., xem WorkerSessionID).
	// Dùng cho các gateway làm cache gợi ý/định tuyến dính theo header session-id.
	// Rỗng = không gửi. Giá trị được transcript.WithSessionID gắn lên context của request,
	// RoundTripper đọc và điền vào, nên cùng một provider dùng chung vẫn phát ra header khác nhau theo phiên.
	SessionHeaderKey string
	// Retry là tham số thử lại sau khi cấu hình này được phân giải (profile ghi đè → chính sách toàn cục →
	// mặc định tích hợp, do phía server phân giải). Ý nghĩa ba tầng xem RetryConfig; giá trị zero = hoàn toàn
	// dùng mặc định tích hợp.
	Retry RetryConfig
}

// RetryConfig là tham số thử lại đi kèm một cấu hình LLM. Ngữ nghĩa "số lần" của mỗi tầng thống nhất:
// 0 = dùng số lần mặc định tích hợp; số âm = tắt thử lại tầng đó; >0 = dùng giá trị đó. "Khoảng cách"
// của mỗi tầng: 0 = dùng exponential backoff vốn có của tầng đó; >0 = đổi sang khoảng cố định này.
type RetryConfig struct {
	// ConnectAttempts/ConnectInterval: thử lại kết nối của SDK (connection reset/timeout/429/5xx,
	// trước khi stream bắt đầu), ánh xạ trực tiếp thành llm.Config.MaxRetries / RetryInterval.
	// Mặc định 3 lần, mũ bắt đầu 0.5s (trần 8s).
	ConnectAttempts int
	ConnectInterval time.Duration
	// EmptyAttempts/EmptyInterval: thử lại phản hồi rỗng của SDK (hoàn thành nhưng không có content
	// block, chỉ format openai), ánh xạ thành llm.Config.EmptyResponseRetries / EmptyResponseInterval.
	// Mặc định 2 lần, cùng một thang mũ.
	EmptyAttempts int
	EmptyInterval time.Duration
	// StreamAttempts/StreamInterval: thử lại trong cửa sổ an toàn cùng provider — một tầng dự án này
	// bổ sung trên SDK, chỉ phát lại khi "chưa giao bất kỳ output nào cho phía gọi" với đứt stream/quá
	// tải/429 trong stream. SDK không thấy nó, do server/task_llm.go tiêu thụ.
	// Mặc định 2 lần, mũ bắt đầu 0.5s (trần 4s).
	StreamAttempts int
	StreamInterval time.Duration
}

// compaction window resolution bounds (in K tokens). Below the floor the
// threshold math (window − summary reserve − buffer) would go non-positive and
// compaction would fire every turn; above the cap it would never fire.
const (
	defaultWindowK = 200  // unset → assume a 200K window (Claude default)
	minWindowK     = 32   // floor so effectiveWindow stays comfortably positive
	maxWindowK     = 1000 // cap at 1M tokens (user request)
)

// CompactionWindow returns the model context window in TOKENS for compaction
// thresholds, resolved from the user-configured size (ContextWindowK). 0/unset →
// a 200K default; otherwise clamped to [32K, 1M] so compaction stays effective.
func (c Config) CompactionWindow() int {
	k := c.ContextWindowK
	if k <= 0 {
		k = defaultWindowK
	}
	if k < minWindowK {
		k = minWindowK
	}
	if k > maxWindowK {
		k = maxWindowK
	}
	return k * 1000
}

// compactionConfig builds the agent-core compaction config for a context window
// in tokens. agentcore.NewSession wires the summarizer (same provider) when this
// is set on Options.Compaction.
func compactionConfig(windowTokens int) *compaction.Config {
	if windowTokens <= 0 {
		windowTokens = defaultWindowK * 1000
	}
	return &compaction.Config{ContextWindow: windowTokens}
}

// FromEnv resolves the LLM provider config:
//
//	ARTEX_LLM_PROVIDER = anthropic|openai (default: inferred from keys)
//	ARTEX_LLM_MODEL    = model id        (default: per provider)
//	ARTEX_LLM_BASE_URL = endpoint        (optional)
//	ARTEX_LLM_PROXY    = proxy URL        (optional; http/https/socks5)
//	ANTHROPIC_API_KEY / OPENAI_API_KEY         = credentials
func FromEnv() (Config, bool) {
	prov := os.Getenv("ARTEX_LLM_PROVIDER")
	anthKey := os.Getenv("ANTHROPIC_API_KEY")
	oaiKey := os.Getenv("OPENAI_API_KEY")

	if prov == "" {
		switch {
		case anthKey != "":
			prov = "anthropic"
		case oaiKey != "":
			prov = "openai"
		default:
			return Config{}, false
		}
	}

	c := Config{
		BaseURL: os.Getenv("ARTEX_LLM_BASE_URL"),
		Model:   os.Getenv("ARTEX_LLM_MODEL"),
		Proxy:   strings.TrimSpace(os.Getenv("ARTEX_LLM_PROXY")),
		// Mặc định streaming; ARTEX_LLM_STREAM=false/0/off tắt tường minh để đi không streaming.
		Stream: !isFalsy(os.Getenv("ARTEX_LLM_STREAM")),
	}
	switch prov {
	case "openai":
		c.Format = llm.FormatOpenAI
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		c.APIKey = oaiKey
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		c.APIKey = anthKey
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	if c.APIKey == "" {
		return Config{}, false
	}
	return c, true
}

// ConfigFrom builds a Config from UI-provided strings (provider defaults to
// anthropic; model defaults per provider). Inputs are trimmed and the base URL
// is normalized to the API base the provider expects (the provider appends the
// endpoint path itself), so a full endpoint URL is tolerated.
func ConfigFrom(provider, model, baseURL, apiKey, proxy string) Config {
	c := Config{
		Model:   strings.TrimSpace(model),
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  strings.TrimSpace(apiKey),
		Proxy:   strings.TrimSpace(proxy),
		Stream:  true, // mặc định streaming; phía gọi ghi đè theo profile
	}
	switch strings.TrimSpace(provider) {
	case "openai":
		c.Format = llm.FormatOpenAI
		// provider appends "/chat/completions"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/chat/completions"), "/")
		if c.Model == "" {
			c.Model = "gpt-4o"
		}
	case "openai-responses":
		c.Format = llm.FormatOpenAIResponses
		// provider appends "/responses"; tolerate a full endpoint URL.
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/responses"), "/")
		if c.Model == "" {
			c.Model = "gpt-5"
		}
	default:
		c.Format = llm.FormatAnthropic
		// provider appends "/v1/messages".
		c.BaseURL = strings.TrimRight(strings.TrimSuffix(c.BaseURL, "/v1/messages"), "/")
		if c.Model == "" {
			c.Model = "claude-opus-4-8"
		}
	}
	return c
}

// isFalsy reports whether an env-var string explicitly requests "off". Empty or
// unrecognized → false (so an unset var keeps the streaming default).
func isFalsy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

// Provider returns the short provider name ("anthropic"/"openai").
func (c Config) Provider() string {
	switch c.Format {
	case llm.FormatOpenAI:
		return "openai"
	case llm.FormatOpenAIResponses:
		return "openai-responses"
	}
	return "anthropic"
}

// NewProvider builds an llm.Provider from the config. When a rate is set, the
// limiter lives on the single provider instance — so planner + all workers +
// main agent (which share this provider) are bounded by one shared rate limit.
func (c Config) NewProvider() (llm.Provider, error) {
	client, err := quotaAwareHTTPClient(c.Proxy, c.SessionHeaderKey)
	if err != nil {
		return nil, err
	}
	lc := llm.Config{
		Format:     c.Format,
		BaseURL:    c.BaseURL,
		APIKey:     c.APIKey,
		Model:      c.Model,
		HTTPClient: client,
	}
	// Hai trường bật/tắt suy nghĩ và cường độ được truyền qua riêng (rỗng = không gửi trường đó).
	// Hai thứ tách rời: có thể chỉ gửi thinking.type, chỉ gửi effort, gửi cả hai, hoặc không gửi gì.
	lc.ThinkingType = c.ThinkingType
	lc.ReasoningEffort = c.ReasoningEffort
	// Chọn tên trường cho giới hạn đầu ra (rỗng = dùng max_tokens). "Giá trị" giới hạn không nằm ở đây:
	// nó đi theo từng lượt qua agentcore.Options.MaxTokens, provider chỉ quyết định nhét nó vào key nào.
	lc.MaxTokensField = c.MaxTokensField
	// Tham số thử lại cùng ngữ nghĩa với SDK (số lần 0=mặc định/âm=tắt, khoảng 0=exponential backoff/>0=cố
	// định), truyền qua nguyên vẹn.
	lc.MaxRetries = c.Retry.ConnectAttempts
	lc.RetryInterval = c.Retry.ConnectInterval
	lc.EmptyResponseRetries = c.Retry.EmptyAttempts
	lc.EmptyResponseInterval = c.Retry.EmptyInterval
	if c.RatePerSecond > 0 || c.RatePerMinute > 0 {
		lc.RateLimit = &llm.RateLimit{PerSecond: c.RatePerSecond, PerMinute: c.RatePerMinute}
	}
	return llm.NewProvider(lc)
}

// IsQuotaExhaustedMessage deliberately recognizes only explicit balance,
// billing, credit, or quota-exhaustion signals. Generic 429/rate-limit text,
// authentication failures, network errors, and server failures are excluded.
var nonFailoverHTTPStatus = regexp.MustCompile(`(?:status(?:\s+code)?|http(?:\s+status)?)\s*[=:]?\s*(?:401|403|5\d\d)\b`)
var transientQuotaLimit = regexp.MustCompile(`(?i)(?:\b(?:rpm|tpm|rpd|qps)\b|quota[_\s-]*metric|rate[_\s-]*limit|too many requests|(?:requests?|tokens?)\s+(?:per|/)\s*(?:second|minute)|(?:per|/)\s*(?:second|minute)\s+(?:requests?|tokens?)|generate[_\s-]*requests[_\s-]*per[_\s-]*(?:minute|second)|tokens?[_\s-]*per[_\s-]*(?:minute|second))`)

func IsQuotaExhaustedMessage(message string) bool {
	message = strings.ToLower(message)
	// Authentication/authorization and provider-side 5xx failures never rotate,
	// even when a gateway happens to echo a quota-looking phrase in the body.
	if nonFailoverHTTPStatus.MatchString(message) {
		return false
	}
	// Provider APIs frequently describe an ordinary rate limit as "quota
	// exceeded", especially Google-style responses containing a quota metric.
	// These limits recover with time and must stay on the current provider.
	if transientQuotaLimit.MatchString(message) {
		return false
	}
	markers := []string{
		"insufficient_quota", "quota_exceeded", "quota exceeded", "quota exhausted",
		"exceeded your current quota", "billing_hard_limit_reached",
		"billing hard limit", "billing_not_active", "credit balance", "insufficient credit",
		"insufficient balance", "balance is too low", "payment required", "status 402",
		"余额不足", "额度不足", "额度已用尽", "欠费",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	// gRPC RESOURCE_EXHAUSTED is overloaded for both account quota and ordinary
	// request-rate limiting. Preserve it as an explicit exhaustion signal only
	// when the same error does not identify a transient rate limit.
	return strings.Contains(message, "resource_exhausted") &&
		!strings.Contains(message, "rate limit") &&
		!strings.Contains(message, "too many requests")
}

// quotaAwareTransport preserves Norma's normal retry behavior except for a 429
// whose body explicitly says the account quota/balance is exhausted. Norma's
// retry loop treats every 429 as transient; normalizing only that response to
// 402 lets a task router fail over immediately while retaining the original
// response body for provider-specific classification and audit logs.
type quotaAwareTransport struct {
	base http.RoundTripper
	// sessionHeaderKey, when non-empty, is the HTTP header name each request
	// carries; its value is the session id read from the request context. Empty
	// disables it. See Config.SessionHeaderKey.
	sessionHeaderKey string
}

func (t quotaAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Custom session-id header: name is user-configured, value is THIS run's
	// session id (norma stashes it on the context via transcript.WithSessionID).
	// Stable across a session's turns and distinct across sessions — exactly what
	// a session-keyed prompt cache wants. Skipped when no session id is present.
	if t.sessionHeaderKey != "" {
		if sid := transcript.SessionIDFrom(req.Context()); sid != "" {
			req.Header.Set(t.sessionHeaderKey, sid)
		}
	}
	// When LLM recording is on, the Recorder puts a Capture on the context so the
	// raw wire bodies can be persisted. This is the only layer that still sees
	// them: norma builds the request body internally and decodes the SSE response
	// before either reaches the recorder.
	capt := llmrec.CaptureFrom(req.Context())
	capt.SetRequest(requestBodySnapshot(req))

	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	// Tee rather than read: a 200 is an SSE stream that must keep streaming. The
	// 429 branch below reads through this wrapper, so its body lands in the
	// capture before being replaced.
	resp.Body = capt.TeeResponse(resp.StatusCode, resp.Body)

	if resp.StatusCode != http.StatusTooManyRequests {
		return resp, nil
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	if readErr != nil {
		return resp, nil
	}
	if IsQuotaExhaustedMessage(string(body)) {
		resp.StatusCode = http.StatusPaymentRequired
		resp.Status = "402 Payment Required"
	}
	return resp, nil
}

// requestBodySnapshot copies an outgoing request body without consuming it.
// norma builds every model request from a *bytes.Reader, so net/http populates
// GetBody and the copy has no effect on what gets sent.
func requestBodySnapshot(req *http.Request) string {
	if req.GetBody == nil {
		return ""
	}
	rc, err := req.GetBody()
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	return string(b)
}

func quotaAwareHTTPClient(proxy, sessionHeaderKey string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		transport.Proxy = nil // để trống = kết nối trực tiếp, không fallback HTTP_PROXY/HTTPS_PROXY
	} else {
		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("llm: invalid proxy %q: %w", proxy, err)
		}
		switch proxyURL.Scheme {
		case "http", "https", "socks5":
		case "":
			return nil, fmt.Errorf("llm: proxy %q missing scheme (use http://, https:// or socks5://)", proxy)
		default:
			return nil, fmt.Errorf("llm: unsupported proxy scheme %q (use http, https or socks5)", proxyURL.Scheme)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: quotaAwareTransport{base: transport, sessionHeaderKey: strings.TrimSpace(sessionHeaderKey)}}, nil
}

// logTestConnection prints the raw HTTP status code(s) and response body of a
// connection test to the server log, so "bấm kiểm tra" leaves a diagnosable trail of
// exactly what the gateway returned — 401 bodies, quota text, empty frames — not
// just the collapsed ok/err the UI shows. Bodies are clipped to keep a chatty
// SSE stream from flooding the log.
func logTestConnection(c Config, capt *llmrec.Capture) {
	attempts := capt.Attempts()
	if len(attempts) == 0 {
		log.Printf("[llm-test] %s / %s @ %s — chưa phát bất kỳ HTTP request nào (phân giải cấu hình hoặc tạo kết nối đã thất bại)",
			c.Provider(), c.Model, c.BaseURL)
		return
	}
	for i, a := range attempts {
		log.Printf("[llm-test] %s / %s @ %s — lần thử %d/%d HTTP %d\nThân phản hồi: %s",
			c.Provider(), c.Model, c.BaseURL, i+1, len(attempts), a.Status, clipBody(a.Body))
	}
}

// clipBody trims a wire body for logging. 4K is plenty to show an error JSON or
// the head of an SSE stream while bounding a runaway response.
func clipBody(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(rỗng)"
	}
	const max = 4096
	if len(s) > max {
		return s[:max] + fmt.Sprintf("…(đã cắt, tổng %d byte)", len(s))
	}
	return s
}

// TestConnection makes a minimal real completion to verify the provider/model/
// endpoint/key actually work. Returns the round-trip latency and the model's
// reply text.
func TestConnection(ctx context.Context, c Config) (time.Duration, string, error) {
	prov, err := c.NewProvider()
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Bắt gói wire thô: điều kiểm tra kết nối cần thấy nhất là gateway đã trả về gì (status code + thân
	// phản hồi), mà sau khi norma giải mã phản hồi thành StreamEvent thì những thứ này mất hết.
	// quotaAwareTransport sẽ tìm Capture này trong context và điền status code cùng body của mỗi lần thử HTTP.
	ctx, capt := llmrec.NewCapture(ctx)
	defer logTestConnection(c, capt)
	// Kiểm tra kết nối là đường phát đơn, không qua vòng lặp phiên của agentcore, nên không ai gắn
	// session id lên context. Với endpoint có cấu hình SessionHeaderKey (như opencode zen bắt buộc header
	// x-opencode-session, thiếu là 400 MissingSessionID ngay), điều này gây ra độ lệch "hội thoại bình
	// thường, bấm kiểm tra lại 400". Ở đây gắn bổ sung một session id ngẫu nhiên một lần, để việc kiểm tra
	// và hội thoại thật đi cùng một logic gắn header; endpoint không cấu hình SessionHeaderKey không đọc
	// nó, không có tác dụng phụ.
	ctx = transcript.WithSessionID(ctx, "conntest-"+transcript.NewSessionID())
	start := time.Now()
	// MaxTokens phải cho đủ: mô hình suy luận (như deepseek-v4-pro) trước khi đưa ra câu trả lời sẽ tạo
	// một đoạn suy nghĩ dài (thực đo với một câu "ping" cũng đốt ~2900 token). Nếu chỉ cho 32, mô hình
	// sẽ mãi kẹt ở "giai đoạn suy nghĩ" rồi đụng giới hạn đầu ra (finish=length), bị cắt; kiểm tra kết nối
	// tuy vẫn tính là qua (err=nil) nhưng hiển thị thành "đã gián đoạn/length/resume" rối tung. Cho đủ
	// ngân sách để nó nhả OK gọn gàng (finish=stop).
	// Giữ EscalateMaxTokens = false: không tăng hạn mức để thử lại khi bị cắt, tránh vòng resume đốt phí vô ích.
	reply, err := agentcore.Run(ctx, agentcore.Options{
		Provider:       prov,
		SystemPrompt:   []string{"Bạn là bài kiểm tra kết nối. Chỉ cần xuất trực tiếp hai ký tự OK, không suy nghĩ, không giải thích, không gì khác."},
		PermissionMode: acperm.ModeBypass,
		MaxTurns:       1,
		MaxTokens:      8192,
		NonStreaming:   !c.Stream, // kiểm tra kết nối bằng chế độ thu/phát thật của profile này
	}, "ping")
	lat := time.Since(start)
	if err != nil {
		return lat, "", err
	}
	// err==nil chưa đủ: tình huống request thông nhưng mô hình không nhả một chữ nào là có thật (suy nghĩ
	// đốt hết ngân sách, phần thân bị chính sách an toàn nuốt, tầng tương thích làm mất content). Cấu hình
	// kiểu đó trong phiên chính là "không trả lời", nhưng kiểm tra lại báo thành công — đúng độ lệch mà hạng
	// mục này cần loại bỏ. Không có phần thân nhìn thấy được thì một luật phán thất bại.
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return lat, "", fmt.Errorf("mô hình không có nội dung trả lời (request đã thông, nhưng không trả về văn bản nào)")
	}
	return lat, reply, nil
}
