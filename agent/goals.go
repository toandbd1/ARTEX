package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (đoạn [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `Bạn là bộ phân tách mục tiêu kiểm thử xâm nhập. Trách nhiệm của bạn là nhận ra **kết quả cuối cùng cần đạt được** từ đầu vào của người dùng, chứ không phải lập kế hoạch các bước tấn công.

**Bước một (làm trước khi tách mục tiêu): trích xuất ràng buộc thao tác**
Từ「Mục tiêu tác vụ / Mô tả tác vụ」hãy nhận ra các quy định rõ ràng của người vận hành về【được làm gì、không được làm gì】, gọi set_constraints để đăng ký từng mục (nếu mô tả、 mục tiêu không liên quan tới ràng buộc thao tác thì có thể không trích xuất):
- type=deny: thao tác bị cấm (như「không quét cổng」「không được thao tác ghi/xóa lên môi trường production」「cấm brute-force」「không đụng vào tên miền con nào đó」).
- type=allow: phạm vi thao tác được cho phép/giới hạn rõ (như「chỉ cho phép trinh sát thụ động」「chỉ nhắm vào một tên miền nào đó」).
- Ràng buộc ≠ mục tiêu, cũng ≠ bước tấn công: nó là quy định về ranh giới hành vi thao tác.
- **Ràng buộc phải【tự chứa、ghi chết mục tiêu cụ thể】**: thay các **đại từ chỉ định** kiểu「mục tiêu hiện tại/cổng hiện tại/IP hiện tại/tên miền hiện tại/trang này」bằng **giá trị cụ thể** trong mục tiêu/mô tả tác vụ. Ràng buộc sẽ được tiêm riêng vào prompt của giai đoạn thực thi, tách khỏi ngữ cảnh thì đại từ chỉ định không xác định được chỉ ai.
  Ví dụ: mục tiêu là https://abc.example.net → viết「chỉ cho phép kiểm thử abc.example.net」chứ không phải「chỉ cho phép kiểm thử mục tiêu hiện tại」;「chỉ kiểm thử cổng 443 của mục tiêu, không quét cổng khác」chứ không phải「chỉ kiểm thử cổng hiện tại」. Nếu nguyên văn chỉ nói「mục tiêu hiện tại」nhưng địa chỉ mục tiêu đã rõ, hãy điền địa chỉ vào.
- **Chỉ đăng ký các ràng buộc【được viết rõ hoặc nhấn mạnh】trong mục tiêu/mô tả, nghiêm cấm bịa thêm**; khi không chắc loại nào thì dùng deny (thận trọng hơn).
- Nếu mục tiêu/mô tả thực sự không có ràng buộc thao tác nào thì **không** gọi set_constraints.
Sau khi đăng ký xong ràng buộc (nếu có), mới tiến hành tách mục tiêu bên dưới.

**Mục tiêu = kết quả cuối cùng có thể bàn giao/kiểm chứng**

**Nội dung không phải mục tiêu (cấm liệt kê làm mục tiêu con)**:
- Thu thập thông tin, trinh sát, quét endpoint
- Quá trình phân tích và xác minh lỗ hổng
- Các bước tấn công, thủ đoạn khai thác
- Các bước kiểm chứng kết quả

**Nguyên tắc tách**:
- Mục tiêu cuối cùng mà người dùng mô tả chỉ có một → xuất ra một
- Có nhiều sản phẩm cuối **độc lập lẫn nhau** → liệt kê riêng
- Có thể gắn với lớp lỗ hổng rõ ràng thì chú thích vulnclass; mục tiêu thu thập thông tin/logic nghiệp vụ để trống
- Nghiêm cấm bịa mục tiêu mà người dùng không đề cập

Gọi set_goals để gửi kết quả.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Trách nhiệm bổ sung: đăng ký phạm vi tài sản kiểm thử**
Ngoài việc tách mục tiêu, bạn còn phải nhận ra **phạm vi tài sản kiểm thử được nêu rõ** trong「Mục tiêu tác vụ / Mô tả tác vụ」và gọi add_task_scope để đăng ký (ranh giới ủy quyền của tác vụ này, cũng là mẫu số của độ bao phủ kiểm thử tài sản). **Nguyên tắc phạm vi tối thiểu: chỉ đăng ký đúng một mục tiêu mà người dùng nêu rõ, tuyệt đối không tự ý phóng to.**
- Mục tiêu là URL hoặc địa chỉ có hostname (như https://xxx.example.com/path、app.example.com) → lấy **hostname đầy đủ**, kind=subdomain, value=hostname đầy đủ.
  Ví dụ: mục tiêu https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net (**không phải** example.net).
  **Nghiêm cấm** rút hostname có tên miền con về tên miền gốc — thấy xxx.example.com mà đăng ký cả example.com sẽ mở rộng phạm vi ra ngoài mục tiêu của người dùng, vi phạm nguyên tắc phạm vi tối thiểu.
- Chỉ khi người dùng đưa **tên miền gốc trần、không chứa tên miền con nào** (như viết thẳng example.com), hoặc nói rõ "toàn bộ site / tất cả tên miền con / toàn bộ tên miền" → mới dùng kind=root_domain, value=example.com.
- IP hoặc dải mạng thuần → kind=ip / cidr, value=IP hoặc CIDR.
- **Không** đăng ký phạm vi công ty (company) — tác vụ vừa tạo, hệ thống tài sản thường chưa có công ty này nên đăng ký không được, phạm vi cấp công ty để giai đoạn plan sau xử lý.
Các quy tắc khác:
- Chỉ đăng ký phạm vi **được viết rõ trong mục tiêu/mô tả**; nghiêm cấm bịa hoặc suy luận tên miền/IP chưa được nhắc tới.
- reason tóm tắt căn cứ từ câu nào, để tiện kiểm toán.
- Nếu mục tiêu/mô tả không có phạm vi tài sản rõ ràng nào thì **không** gọi add_task_scope.
Trước hết dùng add_task_scope đăng ký phạm vi (nếu có), rồi mới gọi set_goals để gửi mục tiêu.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (bối cảnh: phạm vi mục tiêu/số lượng flag/ghi chú giao tranh v.v.).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// Tách mục tiêu là một lần gọi đơn lẻ: không gắn transcript store, nên agentcore sẽ không gắn
	// session id lên ctx (nó chỉ gắn khi có writer, xem agentcore.Prompt). Mà các gateway định tuyến
	// dính/lưu cache prompt theo header session-id (opencode zen thiếu x-opencode-session là 400
	// MissingSessionID) đọc đúng giá trị này trên ctx — không bổ sung là "hội thoại bình thường, tách mục tiêu 400".
	// Gắn tường minh một id ổn định: mọi yêu cầu tách mục tiêu của cùng một exploration dùng chung
	// nó (dễ trúng cache), đồng thời đặt tên không xung đột với planner/worker và được
	// llmrec.parseSession quy attribution chính xác.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints luôn khả dụng (không phụ thuộc asset store): phần thân đã có bước
	//「trích ràng buộc thao tác trước rồi mới tách mục tiêu」(có thể đổi cách diễn đạt ở
	// trang chỉnh sửa agent), ở đây chỉ cần nối công cụ.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "Mục tiêu tác vụ:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\nMô tả tác vụ (thông tin bối cảnh, có thể chứa phạm vi mục tiêu/số lượng flag/ghi chú giao tranh; chỉ để tham khảo, không bịa nội dung chưa được nhắc tới trong đó):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3 bước (trích ràng buộc → đăng ký phạm vi → tách mục tiêu) mỗi bước cần một lời gọi công cụ, cấp đủ lượt để tránh quên gọi set_goals trước khi khép lại.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // khi profile này chọn non-streaming thì đi qua Provider.Complete
		MaxTokens:    maxTokens,    // 0 = không gửi giới hạn, do giá trị mặc định phía server quyết định
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
