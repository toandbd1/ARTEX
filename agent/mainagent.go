package agent

import (
	"context"
	"fmt"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// MainAgent is the thin human-interface orchestrator (docs §4.2 / §7). The human
// chats with it; it observes (read tools), and steers by injecting hints
// (→planner) or direct high-priority intents (→frontier). It does NOT run the
// autonomous intent-generation loop (that is the planner's job).
type MainAgent struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	tx              *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window          int                                    // context window in tokens (for compaction)
	windowFn        func() int                             // optional dynamic task-chain minimum
	maxTurns        int                                    // max agent turns per run (0 = unlimited)
	proxyAddr       string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert     string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch       WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir         string                                 // shared work dir (surfaced in prompt as artifact-output target)
	steerWork       func(intentID int64, msg string) error // engine callback: steer a running work (nil = off)
	nonStreamingFn  func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn    func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn     func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
}

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (m *MainAgent) SetNoaEnabled(fn func() bool) { m.noaEnabledFn = fn }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (m *MainAgent) SetNonStreaming(fn func() bool) { m.nonStreamingFn = fn }

func (m *MainAgent) nonStreaming() bool { return m.nonStreamingFn != nil && m.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (m *MainAgent) SetMaxTokens(fn func() int) { m.maxTokensFn = fn }

func (m *MainAgent) maxTokens() int {
	if m.maxTokensFn == nil {
		return 0
	}
	return m.maxTokensFn()
}

func NewMainAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *MainAgent {
	return &MainAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns}
}

func (m *MainAgent) SetCompactionWindowResolver(fn func() int) { m.windowFn = fn }

func (m *MainAgent) compactionWindow() int {
	if m.windowFn != nil {
		return m.windowFn()
	}
	return m.window
}

// SetProxy points the main agent's WebFetch at the recording proxy plus the CA
// cert it trusts to verify HTTPS through it (empty addr = direct).
func (m *MainAgent) SetProxy(addr, caCert string) { m.proxyAddr, m.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the main agent (off by default).
func (m *MainAgent) SetWebSearch(o WebSearchOpts) { m.webSearch = o }

// SetSteerWork wires the engine callback that lets the main agent's steer_work
// tool inject a mid-run course-correction into a running work (nil = tool off).
func (m *MainAgent) SetSteerWork(fn func(intentID int64, msg string) error) { m.steerWork = fn }

// mainAgentDefaultTmpl is the built-in EDITABLE body (đoạn [A]) of the main agent
// prompt, seeded into agent_prompts. Goal is a {{.Goal}} template var; the quy ước
// xuất sản phẩm trung gian tail is code-owned (artifactSpec), appended after rendering.
const mainAgentDefaultTmpl = `Bạn là "agent chính" của một hệ thống kiểm thử xâm nhập được ủy quyền, là giao tiếp với người vận hành. Bạn không tự mình thám hiểm, cũng không tự chủ sinh ý đồ liên tiếp (đó là việc của planner). Trách nhiệm của bạn:

1. Quan sát: dùng graph_overview / list_findings / list_facts / list_assets / get_worker_output để trả lời câu hỏi của người dùng về tiến độ hiện tại.
2. Điều khiển (đưa ý đồ của con người vào hệ thống):
   - Người dùng muốn "đổi hướng/nhấn mạnh một loại lỗ hổng/tập trung một khu vực" → dùng add_hint để viết gợi ý (planner sẽ đọc ở lần sau).
   - Người dùng muốn "kiểm thử ngay một mục tiêu cụ thể" → dùng add_intent để tiêm trực tiếp một ý đồ ưu tiên cao (priority 8-10). Hệ thống sẽ tự động kéo tác vụ đã hoàn thành về trạng thái chạy, để worker nhận ý đồ này thực thi, xong lại quay về trạng thái đã hoàn thành.
     **Khi mục tiêu tác vụ đã đạt được toàn bộ** (trong graph_overview các goals đều met): trước khi phát lệnh hãy phán đoán xem sau ý đồ này có hàm ý một "kết quả mới cần đạt" không. Nếu có, dùng một câu tóm lại mục tiêu bạn đoán cho người dùng, và **hỏi lại có muốn đăng ký thành mục tiêu chính thức không** — người dùng đồng ý → dùng set_goals đăng ký (tác vụ sau đó vào quy hoạch thông thường, planner sẽ tự chủ tiếp tục đẩy tiến); người dùng không / chỉ muốn thăm dò tạm thời → chỉ add_intent phát một ý đồ này, worker chạy xong tác vụ quay về trạng thái đã hoàn thành (không tự tiếp tục). Nếu ý đồ này rõ ràng chỉ là tra cứu một lần, không hàm ý mục tiêu mới, cứ add_intent trực tiếp, không cần hỏi mỗi lần.
   - Người dùng muốn "chỉnh hướng thời gian thực cho một ý đồ (work) đang chạy (đừng đi X nữa, tập trung Y)" → dùng steer_work (không gián đoạn, không mất tiến độ đã có, có hiệu lực trước hành động kế tiếp của worker); trước đó dùng get_worker_output xem nó đang làm gì. Nếu hướng sai hẳn thì đổi sang add_intent để phát ý đồ mới.
   - Người dùng muốn "thêm một mục tiêu cuối cần đạt" → dùng set_goals để bổ sung mục tiêu. Hệ thống sẽ ghi mục tiêu đó vào đồ thị tác vụ và **tự động kéo tác vụ đã hoàn thành/tạm dừng về trạng thái chạy để chạy tiếp** (planner sau đó sẽ phán đoán lại đã đạt chưa), không cần người dùng bấm khôi phục.
   - Người dùng muốn "thêm/sửa ràng buộc kiểm thử (cho phép/cấm một loại thao tác, như『chỉ kiểm thử cổng hiện tại』『cấm brute-force』『chỉ trinh sát thụ động』)" → dùng set_constraints để đăng ký (type=allow cho phép / type=deny cấm). Ràng buộc sẽ được tiêm vào prompt của planner/worker ở vòng quy hoạch kế tiếp để khoanh ranh giới thám hiểm; cũng có thể thêm/xóa/sửa trong「Quản lý ràng buộc」ở trang tổng quan.
3. Trả lời ngắn gọn bằng ngôn ngữ tự nhiên, nêu rõ bạn đã làm gì.

Mục tiêu tác vụ hiện tại: {{.Goal}}

Không bịa phát hiện; chỉ trả lời dựa trên dữ liệu thật mà công cụ trả về.`

func mainAgentSystem(goal, dataDir, workDir string) string {
	body := renderSystem("mainagent", mainAgentDefaultTmpl, MainVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Chat handles one human message and returns the assistant reply. emit, if
// non-nil, receives each execution step (thinking / tool_use / tool_result /
// text / result) so the main-agent session shows its work — exactly like the
// worker/planner sessions — not just the final answer.
func (m *MainAgent) Chat(ctx context.Context, taskID int64, mainSeg int, as *db.AssetStore, ts *db.ExplorationStore, goal, message string, emit func(db.Activity), notify, resume func(), notifyGoal, notifyHint func([]string)) (string, error) {
	tsx := NewToolSet(ts, "human")
	tsx.SetFindingRecorder(m.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.SetNotify(notify)         // đánh thức chung (các thao tác ghi không có callback riêng đi qua nó, debounced)
	tsx.SetResumeTask(resume)     // set_goals thêm mục tiêu → kéo tác vụ đã hoàn thành/tạm dừng về running
	tsx.SetNotifyGoal(notifyGoal) // set_goals thêm mục tiêu → ghi cho planner một trigger「người dùng vừa thêm mục tiêu: …」
	tsx.SetNotifyHint(notifyHint) // add_hint thêm gợi ý → ghi cho planner một trigger「người dùng vừa thêm N gợi ý chiến lược: …」
	tsx.steerWork = m.steerWork   // enable steer_work tool (nil = unavailable)
	// Công cụ miền + bộ công cụ mặc định cơ bản (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// Khi tính năng độ bao phủ tài sản tắt thì loại add_task_scope/list_untested_assets (không đưa vào prompt).
	base := append(tsx.DropCoverageTools(tsx.MainAgentTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "mainagent", base)
	defer cleanup()
	// Thư mục làm việc của tác vụ này <workDir>/tasks/<taskID>, tạo sẵn trước.
	mainDir := ensureRunDir(m.workDir, taskID, 0)
	ctx = intercept.WithReviewWorkingDirectory(ctx, mainDir)
	system, boundary := deferredSystem(mainAgentSystem(goal, m.workDir, mainDir), def)
	opts := agentcore.Options{
		Provider:        m.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // đi qua proxy ghi lưu để lưu vết; nạp CA của proxy để xác minh chứng chỉ HTTPS được ký lại MITM
		WebFetchProxy:   m.proxyAddr,
		WebFetchCACert:  m.proxyCACert,
		// Tìm kiếm web (tùy chọn). ddgs không cần key; brave-free cần BraveKey; tavily cần TavilyKey.
		// WebSearchProxy là proxy đầu ra độc lập (http/https/socks5), không liên quan tới proxy MITM ghi lưu lượng; để trống thì kết nối trực tiếp.
		EnableWebSearch:       m.webSearch.Enabled,
		WebSearchBackend:      m.webSearch.Backend,
		BraveSearchAPIKey:     m.webSearch.BraveKey,
		TavilySearchAPIKey:    m.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: m.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  m.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   m.webSearch.DeepSeekModel,
		WebSearchProxy:        m.webSearch.Proxy,
		BashEnv:               proxyEnv(m.proxyAddr, m.proxyCACert), // lệnh con Bash mặc định đi qua proxy + tin CA
		WorkingDir:            mainDir,                              // thư mục làm việc của tác vụ <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(mainDir),
		MaxTurns:              m.maxTurns,                             // 0 = unlimited (configurable in agent management)
		Compaction:            compactionConfig(m.compactionWindow()), // long chats stay within the window
		Todos:                 actool.NewTodoStore(),                  // việc cần làm tạm thời cấp phiên (TodoWrite), thuần lập kế hoạch, mất khi thoát
		// Chạm ngân sách (số bước) → SDK chạy khép lại: xuất cho người dùng một câu tổng kết tiến độ. Prompt và số lượt khép lại chỉnh được trong trang quản trị (mặc định 10 lượt).
		Settlement:   wrapupSettlement("mainagent", nil),
		NonStreaming: m.nonStreaming(), // khi profile này chọn non-streaming thì đi qua Provider.Complete
		MaxTokens:    m.maxTokens(),    // 0 = không gửi giới hạn, do giá trị mặc định phía server quyết định
	}
	if m.tx != nil { // persist raw human↔AI conversation; one accumulating file per segment
		opts.Transcript = m.tx
		// Segment 0 keeps the legacy "exp%d-main" name so existing transcripts still
		// load; each new session (seg>=1) gets its own file for a clean context.
		opts.SessionID = fmt.Sprintf("exp%d-main", ts.ID())
		if mainSeg > 0 {
			opts.SessionID = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
		}
	}
	// Tính năng thử nghiệm: khi bật, noa tiếp quản nén ngữ cảnh (bản lưu trữ tập trung tại <workDir>/noa/<SessionID>, bền vững).
	// session id theo cùng quy tắc với transcript (nhận biết phân đoạn), để lưu trữ và khôi phục khớp nhau.
	noaSession := fmt.Sprintf("exp%d-main", ts.ID())
	if mainSeg > 0 {
		noaSession = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
	}
	enableNoa(&opts, m.noaEnabledFn, m.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload the prior conversation from the transcript so the agent has context
	// across turns (each Chat is a fresh session; without this it can't see earlier
	// messages). First turn: no file yet → Resume loads nothing and proceeds.
	if m.tx != nil {
		_ = s.Resume(opts.SessionID)
	}
	// C2: this session is fresh each turn; re-unlock skill-gated MCPs from prior
	// Skill() calls in the reloaded history so revealed tools stay callable.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = "mainagent"
			emit(r)
		}
	})
	return text, err
}
