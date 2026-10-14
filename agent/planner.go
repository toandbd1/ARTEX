package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【Việc cần làm trong quy hoạch của bạn (giữ lại qua các lần đánh thức, do bạn viết ở vòng trước)】:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Tiếp tục theo đó: chỉ phát ý đồ cho bước kế tiếp mà【bước tiền điều kiện đã hoàn thành / fact mà nó phụ thuộc đã tồn tại】; dùng TodoWrite cập nhật danh sách (đánh completed cho các bước đã được fact thỏa mãn). Không phát lại các bước đã pending/in_progress trong danh sách.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = tóm tắt).
//	"goal"    — the human (via set_goals của agent chính) added one OR MORE goals in a
//	            single call (Goals = văn bản mục tiêu thêm lần này, 1+ mục; set_goals hỗ trợ hàng loạt).
//	"goal_deleted" — the human deleted a goal from quản lý mục tiêu ở trang tổng quan (Detail = văn bản mục tiêu bị xóa).
//	"goal_edited"  — the human edited a goal from quản lý mục tiêu ở trang tổng quan (văn bản OldGoal→NewGoal).
//	"cancelled" — the human deleted intent IntentID (Detail = lý do xóa). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // riêng cho Kind=="cancelled": tóm tắt ý đồ chụp trước khi xóa (sau khi xóa thật node không còn, không tra được nữa)
	Goals    []string // riêng cho Kind=="goal": văn bản mục tiêu mà set_goals thêm lần này (1 hoặc nhiều mục)
	OldGoal  string   // riêng cho Kind=="goal_edited": văn bản mục tiêu trước khi sửa
	NewGoal  string   // riêng cho Kind=="goal_edited": văn bản mục tiêu sau khi sửa
	Hints    []string // riêng cho Kind=="hint": văn bản gợi ý mà add_hint thêm lần này (1 hoặc nhiều mục)
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【Các thay đổi thực tế đã kích hoạt vòng này (xem đây trước, rồi mới quyết định có bổ sung hướng không)】:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- Người dùng (agent chính) vừa thêm một mục tiêu: %s — mục tiêu mới cần đạt, hãy bổ sung hướng thám hiểm theo đó (nếu chưa có ý đồ tương ứng).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- Người dùng (agent chính) vừa thêm %d mục tiêu: %s — đều là mục tiêu mới cần đạt, hãy lần lượt bổ sung hướng thám hiểm cho các mục tiêu chưa có ý đồ tương ứng.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- Người dùng (agent chính) vừa thêm một gợi ý chiến lược: %s — đã gắn lên đồ thị thám hiểm, hãy điều chỉnh/bổ sung hướng thám hiểm theo đó (nếu chưa có ý đồ tương ứng).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- Người dùng (agent chính) vừa thêm %d gợi ý chiến lược: %s — tất cả đã gắn lên đồ thị thám hiểm, hãy lần lượt điều chỉnh/bổ sung hướng thám hiểm theo đó.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- Người dùng đã xóa mục tiêu: %s — mục tiêu đó đã bị gỡ, hãy phán đoán lại các mục tiêu/hướng còn lại (không cần phát ý đồ cho nó nữa).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- Người dùng đã sửa mục tiêu, từ「%s」thành「%s」— hãy điều chỉnh hướng thám hiểm theo mục tiêu mới (nếu hướng cũ không còn phù hợp thì ngừng phát).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- Worker của ý đồ #%d (%s) đã báo cáo một finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// Nội dung ý đồ ưu tiên dùng Summary chụp lúc xóa (sau khi xóa thật node không còn, intentSummary không tra được).
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- Ý đồ #%d đã bị người dùng xóa, nội dung ý đồ là: %s, lý do xóa là: %s. Ý đồ này đã bị xóa (không thực thi nữa); hãy quy hoạch lại theo đó.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- Worker của ý đồ #%d (%s) đã kết thúc, kết luận đầu ra: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; id các fact mới mà ý đồ này tạo ra: %s ", fids))
			}
		}
	}
	b.WriteString("\n(Chi tiết đầy đủ có thể tra lại bằng node_detail / get_worker_output / list_findings.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, "、")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(lấy output thất bại)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(work này chưa có bản ghi output)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(đã cắt ngắn, đầy đủ xem get_worker_output)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n【Tình thế vòng này (lấy trước từ graph_overview, tương đương kết quả trả về khi bạn gọi công cụ đó; cần chi tiết thì gọi node_detail/list_facts v.v. tùy nhu cầu)】:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (đoạn [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the quy ước xuất sản
// phẩm trung gian tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `Bạn là "planner" của một hệ thống kiểm thử xâm nhập được ủy quyền trên nền tảng an ninh mạng, được đánh thức thường xuyên (đồ thị vừa đổi là đánh thức). Trách nhiệm: đọc tình thế → phán đoán mục tiêu → **chỉ khi thật sự có hướng mới chưa được phủ** mới bổ sung ý đồ thám hiểm. Bạn là planner, không phải người thực thi: mọi sản phẩm của vòng này chỉ được là【tạo/nói rõ ý đồ】hoặc【phán đoán mục tiêu】, tuyệt đối không làm luôn việc trong plan.

Mục tiêu tác vụ: {{.Goal}}

**Vòng này nên tạo ra mấy ý đồ (nghĩ rõ điều này trước)**:
- **Đáy cứng (ưu tiên cao nhất)**: chỉ cần【mục tiêu chưa đạt】và【hiện không có bất kỳ ý đồ open hoặc running nào】(frontier_open=0 và running_intents rỗng), vòng này【bắt buộc】tạo ra ít nhất một ý đồ tiến về mục tiêu — khi không có work nào đang chạy để chờ, cũng không có hướng nào đang xếp hàng, tạo 0 ý đồ = tác vụ đình trệ; kể cả khi mọi hướng đã biết chỉ nằm trong recent_done, cũng phải dựa vào phán đoán done/exhausted/blocked bên dưới để mở một hướng khác hoặc phát tiếp một hướng.
- Ngoài đáy cứng, **tạo 0 ý đồ là kết quả bình thường, nhưng phải có lý do chính đáng** (không phải mặc định "phát ít cho chắc"): ①**đã phủ** — các hướng bạn nghĩ tới đều đã được ý đồ còn open/running xử lý (đổi cách diễn đạt để tạo lại ý đồ đã tồn tại là lỗi nghiêm trọng); ②**chờ phụ thuộc** — bước kế tiếp phụ thuộc sản phẩm của work đang chạy mà nó chưa ra (lúc này phát cứng sẽ khiến hạ nguồn không có tiền điều kiện mà quay vòng, nên chờ lần đánh thức sau khi đồ thị cập nhật rồi phát).
- Ngược lại: nếu thật sự có hướng mới【chưa được phủ và không phụ thuộc work đang chạy】, hoặc mục tiêu chưa đạt mà trong phạm vi vẫn còn mặt chưa kiểm thử, thì nên phát — đừng coi 0 ý đồ là mặc định lười biếng.

**Quy trình quyết định mỗi lần đánh thức**:

1. **Tình thế đầy đủ đã đính kèm phía dưới prompt này** (chính là kết quả trả về của graph_overview, không cần gọi lại nó): task (tiêu đề gốc + mục tiêu/node gốc), bộ đếm tài sản, goals+trạng thái, ý đồ open/running/recent_done, sites_without_endpoints (site không có endpoint, gợi ý hướng có thể còn phải thám hiểm), facts (số fact thám hiểm, khác loại với lỗ hổng), recent_facts ({id,summary,confidence?}).
   - **Phạm vi**: các node thám hiểm (goals/ý đồ/facts/findings) chỉ chứa tác vụ này; **đồ thị tài sản chia sẻ toàn cục** (nhiều tác vụ dùng chung một bản, bộ đếm tài sản là toàn cục trong phạm vi, không riêng tác vụ này) — gặp tài sản không liên quan tới tác vụ này thì bỏ qua.
   - **Huyết thống**: mỗi ý đồ kèm parents (thượng nguồn: phái sinh từ những fact/ý đồ nào) và yields (hạ nguồn: đã tạo ra những fact/phát hiện nào), mỗi recent_facts kèm from_intent; dựa vào đó hiểu "những fact nào đến từ hướng nào, có tổng hợp ra hướng mới không".
   - **Quan sát phủ định/còn nghi ngờ** (trong recent_facts kiểu "cổng đóng/không inject được") là quan sát của worker, không phải kết luận: trước khi tin hãy node_detail(id) xem evidence — evidence vững, confidence=observed và thủ đoạn đã dùng hết mới coi là hướng đó tạm thời bị khóa; evidence thiếu, chỉ "có vẻ giống/chỉ dò một lần", hoặc confidence=inferred thì xử lý như【chưa thám hiểm rõ】, nếu trong phạm vi và không có ý đồ nào khác phủ, mặc định phát một ý đồ kiểm chứng để xác nhận hoặc bác bỏ (**cùng một hướng phủ định tối đa kiểm chứng một lần**; sau kiểm chứng vẫn phủ định và bằng chứng hợp lý thì tôn trọng kết luận đó, không phát nữa).
   - **Cần chi tiết sâu hơn thì gọi tùy nhu cầu**: list_facts (phân trang, mới nhất trước, mặc định 20, có thể lọc bằng q, lật trang bằng before, kèm total/has_more), list_findings (toàn bộ lỗ hổng), node_detail(id) (bằng chứng/chi tiết đầy đủ; danh sách/recent_facts chỉ cho tóm tắt), list_assets (pull: tìm kiếm q, lọc type/company_id/task_id, phân trang, hoặc lấy thẳng id/ids), asset_neighbors. Tài sản chia sẻ toàn cục, đừng mặc định kéo toàn bộ.

2. **Phán đoán mục tiêu (trách nhiệm cốt lõi)**: trường goals đã chứa mục tiêu và trạng thái; với mục tiêu chưa đạt đã được một phát hiện/fact chứng minh, gọi prove_goal(goal_id, evidence_id, reason) để đánh met. **Khi mục tiêu bạn đánh dấu vừa đúng là mục tiêu chưa hoàn thành cuối cùng, hệ thống tự động xác định toàn bộ tác vụ hoàn thành** — việc khép lại chỉ được dẫn bởi từng prove_goal, không có cách "hoàn thành một chạm" nào khác.
   - ⚠️ **Đối chiếu nghiệm thu định lượng (nghiêm cấm đóng dấu sớm)**: khi mục tiêu chứa điều kiện định lượng (độ bao phủ đạt X%, lấy N flag, đạt quyền nào đó), trước khi prove_goal【bắt buộc】đối chiếu giá trị đo thực tế trong graph_overview phía trên (coverage.pct, bộ đếm findings_total v.v.): chưa đạt chuẩn thì【cấm】prove_goal, đổi sang phát ý đồ bù phần thiếu; không được lấy lý do "đại thể đạt/phần lõi đã chiếm" để đánh met sớm. Ví dụ: yêu cầu độ bao phủ 100% mà thực đo coverage.pct=40% → chưa đạt, tiếp tục phát ý đồ kiểm thử bổ sung.

3. **(Tùy chọn, chỉ lúc mở đầu, cực nhẹ) thăm dò để hiểu**: chỉ khi trong đồ thị gần như chưa có fact (recent_facts cơ bản rỗng, tác vụ mới bắt đầu), chỉ dựa tình thế không nói cụ thể được ý đồ ban đầu, mới dùng Bash v.v. để thăm dò mục tiêu một lượng cực ít, chỉ-đọc (như 1–2 lần curl xem trang chủ/dấu vân tay). **Sản phẩm hợp lệ duy nhất là một câu mô tả ý đồ chính xác hơn** — tuyệt đối không phải phát hiện/xác minh/khai thác lỗ hổng, cũng không phải kết quả liệt kê endpoint/thư mục/tham số (đó là việc của worker, hãy viết thành ý đồ phát xuống). Ba ranh giới cứng:
   - Trong đồ thị đã có fact do worker tạo ra (facts>0 / recent_facts không rỗng) →【cấm】tự thăm dò thêm, mọi phán đoán dựa trên fact đã có, sản phẩm vòng này chỉ có thể là "phát ý đồ mới" hoặc "kết thúc"; muốn đào sâu một manh mối → phát ý đồ để worker đi tra, không phải tự curl.
   - Kể cả lúc mở đầu cũng tối đa dò ≤3 lần là dừng, chỉ để nói rõ ý đồ ban đầu; một khi thấy mình đang "đi sâu kiểm chứng" thay vì "nhanh chóng định hướng" (liệt kê từng endpoint/thư mục, thử từng id, giải mã chuỗi, dò lặp cùng một interface, bất kỳ kiểm thử xác minh injection/vượt quyền/lỗ hổng nào — toàn bộ là việc nặng của worker), lập tức dừng tay viết thành ý đồ.
   - Điều gì phán đoán được từ fact/tình thế hiện có thì không cần thăm dò.

4. **Quyết định bổ sung hướng mới nào**: **"khắc chế" ở đây chỉ nghĩa là【không trùng lặp ý đồ đã tồn tại】, không phải "phát càng ít càng tốt"** — khi mục tiêu chưa đạt, câu hỏi mặc định là "để tiến gần mục tiêu, còn cách đánh nào sâu hơn, mạnh hơn, chưa được phủ", chứ không phải "có thể khép lại chưa". Ý đồ là【hướng thám hiểm mở】(không phải loại/thực đơn cố định), kết hợp fact, tài sản, mục tiêu đã biết để tự phán đoán hướng, lần lượt đối chiếu với open + running + recent_done:
   - Đã có open/running phủ → không tạo lại (đang xử lý).
   - Đã xuất hiện trong recent_done → **trước hết xem state của ý đồ đó (mỗi mục đều kèm) để phân biệt nó dừng thế nào, rồi mới quyết định**:
     · **done (chạy xong bình thường)**: đã phủ → không phát lại nguyên dạng; có phải đường cụt không thì nhìn kết luận fact mà nó yields ra, không nhìn state; chỉ khi xuất hiện【cơ chế mới mang tính vật chất】(fact/tài sản/tham số mới/cách đánh khác rõ rệt) mới phát lại, và summary phải viết rõ khác lần trước ở đâu; đổi cách diễn đạt, "thử lại biết đâu được" không tính, cấm thử lại.
     · **exhausted (hết ngân sách, bị cắt giữa chừng, chỉ ghi lại một phần) / blocked (mô hình hoặc mạng lỗi, cơ bản chưa thám hiểm được)**: đều là giữa đường không kết thúc tử tế, thông tin không đầy đủ — trước hết dùng get_worker_trace / get_worker_output xem nó thực tế làm tới đâu, kẹt ở đâu, rồi chọn trong các phương án sau: sắp đột phá mà bị ngân sách cắt → phát "tiếp tục từ tiến độ lần trước"; thuần lỗi bên ngoài không chạy được (blocked thường là vậy) → phát lại trực tiếp cùng hướng; lần nào cũng kẹt cùng một chỗ → đổi cách đánh/hướng. Căn cứ luôn là tiến độ thật trong trace, không phải bản thân state.
   - Hướng hoàn toàn mới không có ý đồ nào phủ → tạo.
   - Mọi hướng đã biết đều được ý đồ còn open/running phủ → không tạo, kết thúc trực tiếp (có work đang chạy/xếp hàng, chờ chúng đẩy tiến); nhưng nếu chỉ còn recent_done phủ, không còn open/running mà mục tiêu chưa đạt → theo đáy cứng ở đầu phải mở mới hoặc phát tiếp.
   - **Độ sâu ưu tiên hơn độ bao phủ**: coverage là sàn/hạng mục nghiệm thu, không phải mục tiêu thám hiểm bản thân; sau khi phát hiện lối vào giá trị cao (có thể dẫn tới RCE/leo thang đặc quyền/rò rỉ dữ liệu), ưu tiên phát ý đồ【đục thấu đường đó về chiều sâu】, thay vì để kéo phẳng độ bao phủ mà trải rộng, kiểm thử nông từng tài sản.
   - **Giữ tuyến đường đa dạng, đừng hội tụ quá sớm**: khi mục tiêu chưa đạt, nếu các ý đồ hiện có đều dồn vào cùng một tuyến/lối vào, mà tồn tại hướng chưa được phủ【khác bản chất】(mặt lối vào khác/loại tài sản khác/chuỗi khai thác khác), ưu tiên bổ sung hướng phân kỳ đó, thay vì thêm ý đồ đồng nghĩa trên cùng một tuyến (nhìn khác biệt thực chất, không nhìn cách diễn đạt); nếu hướng phân kỳ đó đã được ý đồ hiện có phủ thì vẫn không tạo. Lý tưởng là 2–3 tuyến cơ chế khác nhau cùng tồn tại (như "đánh từ chuỗi upload" và "đánh từ bỏ qua xác thực"), khi một tuyến giao ra bằng chứng【tiến gần mục tiêu】mới tập trung tài nguyên vào đó. **Nhưng tính đa dạng luôn phục tùng【ràng buộc thao tác】ở đầu**: mặt lối vào/cổng/host/thao tác bị ràng buộc loại trừ, dù khác bản chất cũng tuyệt đối không tạo ý đồ.

   **Chuỗi khai thác tuần tự: phát theo bước, đừng tách thành song song.** Chuỗi tuần tự phụ thuộc mạnh (①→②→③, bước sau phụ thuộc sản phẩm thực tế của bước trước): không phát song song một lần (hạ nguồn không lấy được tiền điều kiện chưa tồn tại chỉ lặp lại/quay vòng); dùng TodoWrite ghi cả chuỗi thành việc cần làm (mỗi bước một mục), vòng này chỉ phát bước "tiền điều kiện đã thỏa mãn" (thường là bước một), đợi nó tạo ra fact thì lần đánh thức sau (prompt sẽ kèm danh sách việc cần làm) phát bước kế tiếp và đánh completed các bước đã thỏa mãn. "Cùng một việc" đừng tách hai mục ("xác nhận điểm kích hoạt" và "kích hoạt điểm kích hoạt" là cùng một bước); chỉ các chiều【song song, không phụ thuộc lẫn nhau】(như liệt kê nhiều endpoint không liên quan) mới dùng nhiều ý đồ song song.

5. **Gửi**: dùng【một lần】add_intent để gửi hàng loạt các hướng mới đã sàng lọc (mảng intents, tối đa 4 hướng giá trị cao nhất, không gọi nhiều lần từng mục một):
   - **summary**: một câu ngôn ngữ tự nhiên mô tả hướng đó (địa chỉ đầy đủ của mục tiêu kiểm thử + làm gì + vì sao), không gáy vào phân loại cố định; khử trùng chủ yếu dựa vào việc đối chiếu nó với các ý đồ đã có.
   - **asset_ids**: id tài sản mục tiêu mà hướng này muốn kiểm thử/tấn công (cố gắng truyền, 0/1/nhiều, lấy từ list_assets) — chỉ cần hướng xoay quanh tài sản cụ thể (site/interface/tham số/host) thì nhất định phải truyền, dùng để khử trùng theo phủ, nối vào chuỗi liên kết tài sản, trải nhiều tài sản thì truyền hết; chỉ trinh sát toàn cục không có tài sản cụ thể mới để trống.
   - **parent_ids**: hướng này được tổng hợp từ những node thượng nguồn nào (tùy chọn, 0/1/nhiều) — nhiều fact kết hợp sinh ra một ý đồ thì truyền hết, phái sinh từ ý đồ/phát hiện thượng nguồn nào thì truyền id của nó, hướng hoàn toàn mới ở tầng trên cùng thì để trống.

Không trùng lặp, không gượng ép; nhưng khi mục tiêu chưa đạt và còn cách đánh chưa được phủ và sâu hơn thì nên phát cứ phát. Ngắn gọn, tập trung, hiệu quả.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// Công cụ miền + bộ công cụ mặc định cơ bản (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// Khi tính năng độ bao phủ tài sản tắt thì loại add_task_scope/list_untested_assets (không đưa vào prompt).
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// Tình thế then chốt (ý đồ vừa hoàn thành + đồ thị đầy đủ lấy trước) chuyển sang【user input của vòng
	// này】(xem input bên dưới), system chỉ giữ phần thân quy hoạch tĩnh. Move-out giúp system ổn định
	// qua các vòng, lợi cho cache hơn; cái giá là nếu một vòng dài ra, tình thế có thể bị compaction
	// nén (một vòng planner thường ngắn, rủi ro thấp). situational sẽ được ghép vào input bên dưới.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// Deadline cấp tác vụ / chế độ khép lại cuối (được tiêm qua ctx, xem taskclock.go). Ở vòng cuối,
	// câu khép lại planner khi tác vụ hết giờ được ghép vào user input của vòng này như【chỉ thị thao tác
	// của vòng này】(theo situational), để nó chỉ phán đoán mục tiêu lần cuối, không tạo ý đồ mới.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n【Khép lại cuối của tác vụ (chỉ thị đặc biệt của vòng này, ghi đè quy trình quy hoạch thông thường phía trên)】: " + resolveTaskTimeoutWrapup("planner")
	}
	// Thư mục làm việc của tác vụ này <workDir>/tasks/<taskID>, tạo sẵn trước.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // ràng buộc thao tác (nếu có) tiêm vào system prompt, khoanh ranh giới thám hiểm
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner không có ngân sách đồng hồ riêng; khi có deadline thì kẹp MaxDuration về phần còn lại, để vòng
	// quy hoạch đang chạy vào khép lại khi tác vụ đến hạn (do timeout → dùng prompt tác vụ hết giờ, do số bước → prompt per-run).
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // đi qua proxy ghi lưu để lưu vết; nạp CA của proxy để xác minh chứng chỉ HTTPS được ký lại MITM
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// Tìm kiếm web (tùy chọn). ddgs không cần key; brave-free cần BraveKey; tavily cần TavilyKey.
		// WebSearchProxy là proxy đầu ra độc lập (http/https/socks5), không liên quan tới proxy MITM ghi lưu lượng; để trống thì kết nối trực tiếp.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // lệnh con Bash mặc định đi qua proxy + tin CA
		WorkingDir:            taskDir,                              // thư mục làm việc của tác vụ <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=không giới hạn; khi có deadline = phần còn lại tới deadline
		Compaction:            compactionConfig(p.compactionWindow()),
		// Việc cần làm trong quy hoạch chia sẻ qua các lần đánh thức: giúp chuỗi tuần tự tồn tại qua nhiều vòng (session là mới, store thì không).
		Todos: p.todoFor(ts.ID()),
		// Chạm ngân sách số bước của vòng hiện tại → SDK chạy khép lại: hiện thực hóa các kết luận đã nghĩ rõ
		// trong vòng này (add_intent cần phát, prove_goal có thể chứng minh, chuỗi tuần tự ghi TodoWrite),
		// chứ không phải ngừng quy hoạch — planner vẫn sẽ được đánh thức lặp lại sau đó.
		// Khi bị clamp (kẹp bởi deadline của tác vụ) thì đổi sang PromptByReason (xem wrapupSettlementForTask).
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // khi profile này chọn non-streaming thì đi qua Provider.Complete
		MaxTokens:    p.maxTokens(),    // 0 = không gửi giới hạn, do giá trị mặc định phía server quyết định
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// Tính năng thử nghiệm: khi bật, noa tiếp quản nén ngữ cảnh (bản lưu trữ tập trung tại <workDir>/noa/<SessionID>, bền vững).
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// Tình thế (ý đồ vừa hoàn thành + đồ thị đầy đủ) giờ được ghép vào user input của vòng này (xem input
	// bên dưới). Trong user còn có chỉ thị + việc cần làm qua các lần đánh thức (todo là ghi chú quy hoạch
	// của chính mô hình, tái sinh được, để trong user là đủ).
	// Câu mở đầu chia hai kiểu theo【vòng này có thay đổi cụ thể không】: có thay đổi → trỏ tới khối【thay đổi
	// thực tế】bên dưới; không thay đổi (tuần tra định giờ / hint / khôi phục v.v.) → đừng nói dối "đồ thị đã
	// thay đổi", chuyển sang gợi ý tiện thể rà soát các ý đồ đang chạy.
	lead := "Vừa có thay đổi cụ thể (xem khối【Các thay đổi thực tế đã kích hoạt vòng này】bên dưới), hãy quy hoạch bước kế tiếp theo đó: "
	if len(triggers) == 0 {
		lead = "Vòng này là lần đánh thức **tuần tra định giờ (đến điểm nhịp tim)/không có tín hiệu thay đổi cụ thể** — đồ thị không nhất thiết có thay đổi mới. Tiện thể rà soát các ý đồ đang chạy: cái nào lâu không tiến triển hoặc lệch hướng thì dùng steer_work chỉnh, cái nào sai hẳn hướng thì dùng kill_work cắt lỗ; rồi phán đoán mục tiêu, quyết định có bổ sung hướng không: "
		// Khi đánh thức do nhịp tim/không thay đổi, nếu toàn đồ thị không còn ý đồ open hoặc running nào →
		// thám hiểm đã đình trệ (không worker nào đang chạy, cũng không có hướng xếp hàng). Thông báo rõ
		// cho planner và bắt buộc nó bổ sung hướng mới trong vòng này, đừng chỉ rà soát ý đồ đang chạy
		// rồi quay vòng một lần.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "Vòng này là lần đánh thức **tuần tra định giờ (đến điểm nhịp tim)**, và hiện tại **đã không còn bất kỳ ý đồ open hoặc running nào** — không có worker nào đang chạy, cũng không có hướng nào đang xếp hàng, thám hiểm đã đình trệ. Bạn **bắt buộc** phải tạo trong vòng này một hoặc nhiều ý đồ mới tiến về mục tiêu và **không trùng lặp** với các ý đồ đã có trong đồ thị (không được tạo 0 ý đồ); trước hết dựa vào tình thế bên dưới phán đoán mục tiêu đã đạt chưa, chưa đạt thì bổ sung hướng ngay: "
		}
	}
	input := lead + situational + "\n\nDựa vào tình thế phía trên, phán đoán mục tiêu. Khi mục tiêu đã【thực sự đạt】(đã lấy được kết quả mục tiêu/đã xác nhận lỗ hổng mục tiêu) thì dùng prove_goal đánh dấu từng cái. **Đáy cứng: chỉ cần mục tiêu chưa đạt và hiện không có bất kỳ ý đồ open hoặc running nào (frontier_open=0 và running_intents rỗng), vòng này bắt buộc phải tạo ít nhất một ý đồ tiến về mục tiêu — lúc này không có work nào đang chạy để chờ, cũng không có hướng nào đang xếp hàng, tạo 0 ý đồ = tác vụ đình trệ. Chỉ khi đã có ý đồ open/running đang đẩy tiến, hoặc mục tiêu đã đạt, vòng này mới được phép không tạo ý đồ mới.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration giờ sẽ ngắt công cụ đang chạy khi đến điểm đồng hồ và vào khép lại ngay tại chỗ (trên ctx
	// còn sống), một vòng bị kẹt chết không còn lách qua khép lại, không cần ctx cứng bên ngoài làm đệm.
	// ctx chỉ mang pause / kill / shutdown.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
