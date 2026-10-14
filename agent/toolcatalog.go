package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// File này biến "công cụ tích hợp" từ code thuần thành một danh mục có thể liệt kê,
// có thể bị DB ghi đè:
//   - BuiltinToolSeeds(): trải tập công cụ tích hợp của ba agent thực thi thành các bản
//     ghi seed (key + mô tả + schema tham số + agent mặc định gắn kèm), để server lúc
//     khởi động gieo idempotent vào bảng tools.
//   - Hook ToolResolve: khi chạy, dựa trên hàng tools trong DB, áp dụng cho các công cụ
//     đã lắp ráp "lọc theo agent + ghi đè mô tả/schema + chèn giá trị mặc định tham số".
//     key/handler vẫn ở tầng code, DB chỉ đổi "phần văn xuôi và giá trị mặc định".
// handler (hành vi Call) luôn đến từ code — DB không đổi được nó, chỉ đổi được phần
// giải thích và tham số mặc định mà mô hình nhìn thấy.

// ToolSeed là snapshot có thể gieo của một công cụ tích hợp: key chính là
// CoreTool.Name() (gắn chết với handler, UI chỉ đọc), Desc/Schema lấy từ định nghĩa
// công cụ trong code, Agents là code mặc định giao nó cho những agent nào.
type ToolSeed struct {
	Key    string         // = CoreTool.Name(), khóa chính, không được đổi
	Desc   string         // mô tả cấp trên (có thể ghi đè trong UI)
	Schema map[string]any // JSON-Schema tham số (cấu trúc chỉ đọc, description/default có thể sửa trong UI)
	Agents []string       // các agent key gắn mặc định (worker/planner/mainagent)
}

// builtinToolsByAgent dùng một ToolSet "vỏ rỗng chỉ đọc" (nil stores) để dựng tập công
// cụ miền của từng agent thực thi. Hàm khởi tạo công cụ chỉ nhét closure vào Spec, lúc
// khởi tạo không giải tham chiếu store, nên nil an toàn — các công cụ ở đây chỉ dùng để
// đọc Name()/Description()/InputSchema(), tuyệt đối không Call.
//
// Cố ý [không chứa] các công cụ SDK chung actool.DefaultTools() (Read/Write/Edit/
// MultiEdit/LS/Glob/Grep/Bash): agent nào cũng cố định có chúng, không có bài toán
// "gắn cho ai", và phần giải thích đa số nằm trong Prompt() (bảng này chỉ phủ
// Description(), sẽ gây hiểu nhầm phủ một nửa). Không seed → không có hàng DB →
// ToolResolve cho qua nguyên vẹn, không ghi đè, hành vi như trước. Chỉ công cụ miền
// của riêng artex mới vào bảng để quản lý.
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// goals (bộ phân rã mục tiêu) mặc định gắn set_goals + set_constraints: nhờ chúng
		// ghi các mục tiêu đã tách, các ràng buộc thao tác đã rút ra vào kho. Dùng chung công
		// cụ được quản với mainagent, web có thể sửa mô tả/schema, tích chọn theo agent.
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto mặc định gắn công cụ báo cáo lỗ hổng + quản lý tài sản, các công cụ miền
		// khác có thể tích chọn trong UI khi cần. Kho mới ghi vào từ seed ở đây; kho cũ do
		// seedAutoDefaultBindings di chuyển.
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// pentest (agent kiểm thử xâm nhập độc lập) mặc định gắn: tra tài sản / chèn tài
		// sản / báo lỗ hổng / tra lỗ hổng / tra doanh nghiệp. Kho mới ghi vào từ seed ở đây;
		// kho cũ do seedPentestDefaultBindings di chuyển.
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound: các công cụ system này vẫn vào danh mục như thường (web nhìn thấy,
// có thể tích chọn thủ công theo agent), nhưng mặc định [không gắn agent nào] —
// ToolResolve luôn loại bỏ công cụ không gắn ai với mọi agent, phải opt-in tường minh.
// Lý do vẫn giữ nó trong base toolset của một agent nào đó (như goal_met trong
// PlannerTools): một là để seed có thể khởi tạo nó lấy desc/schema, hai là sau khi
// người dùng gắn lại thủ công, lúc chạy base có nó thì ToolResolve mới giữ lại được.
//
// goal_met: bỏ qua từng prove_goal, trực tiếp tuyên bố từ toàn cục [toàn bộ tác vụ đã
// hoàn thành], trọng lượng lớn và có rủi ro phán nhầm, lại trùng với "prove_goal đánh
// dấu mục tiêu cuối cùng → tự động khép lại", nên mặc định không giao cho agent nào,
// cần thì gắn lại thủ công.
var defaultUnbound = map[string]bool{"goal_met": true}

// BuiltinToolSeeds gộp khử trùng các tập công cụ tích hợp của các agent thành danh
// sách seed: công cụ trùng tên (như list_assets nhiều agent đều có) gộp thành một bản
// ghi, Agents lấy hợp; công cụ trong defaultUnbound thì ép gắn rỗng.
func BuiltinToolSeeds() []ToolSeed {
	byAgent := builtinToolsByAgent()
	order := []string{"mainagent", "goals", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool   actool.CoreTool
		agents []string
	}
	m := map[string]*acc{}
	var keys []string
	for _, ak := range order {
		for _, t := range byAgent[ak] {
			a, ok := m[t.Name()]
			if !ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out := make([]ToolSeed, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		agents := a.agents
		if defaultUnbound[k] {
			agents = []string{} // vào danh mục, có thể gắn thủ công, nhưng mặc định không giao cho agent nào (lưu [] chứ không phải null, nhất quán với các công cụ khác)
		}
		out = append(out, ToolSeed{
			Key:    k,
			Desc:   a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// tham số mặc định get injected. Tools with no matching DB row (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (o *overriddenTool) Description() string         { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// null. Structure (names/types/required) is untouched — only giá trị mặc định are merged in.
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs := scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m := map[string]json.RawMessage{}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &m); err != nil {
			return in // non-object input: don't touch it
		}
	}
	changed := false
	for k, dv := range defs {
		if cur, ok := m[k]; !ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if !changed {
		return in
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dv, ok := p["default"]
		if !ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err := json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := string(raw)
	return s == "null" || s == `""`
}
