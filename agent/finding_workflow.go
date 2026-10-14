package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**Quy ước mã lỗ hổng**: finding_id là ID bản ghi lỗ hổng độc lập; finding_node_id là ID node thám hiểm. id của list_findings / list_task_findings / node_detail / get_task_node_detail giữ nguyên là ID node thám hiểm, hãy đọc mã độc lập từ finding_id trong cùng kết quả trả về. get_finding_traffic / bind_finding_traffic dùng finding_id độc lập. Tham số finding_id của update_finding_report cũ vẫn truyền finding_node_id. Không dùng con số ở dòng đầu tiên của report_finding cho các công cụ bằng chứng, cũng không đoán con số khác khi gặp lỗi sai mã."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\nMặc định Agent báo cáo sẽ đối chiếu và gắn lưu lượng trước khi viết báo cáo. Người báo cáo giữ trong evidence các lệnh xác minh, output then chốt, các ID lưu lượng thật đã có và công dụng của chúng, để Agent báo cáo đối chiếu với bản ghi thực thi; không cần tra gói tin thêm chỉ để gắn. Tương thích gắn tức thì tường minh: traffic_refs hoặc evidence_hint_id có thể gửi các tham chiếu đã kiểm chứng, cái sau đọc tham chiếu có cấu trúc của hint được chỉ định trong tác vụ này; nếu một trong hai không hợp lệ thì toàn bộ lần báo cáo này thất bại. TCP/không có gói tin thì không cần các tham số tùy chọn này. Trả về finding_id và finding_node_id lần lượt biểu thị bản ghi độc lập và node thám hiểm."
		case "add_hint", "add_task_hint":
			note = "\nKhi bàn giao lỗ hổng đã xác nhận, trong traffic_refs của hint tương ứng hãy giữ ID, công dụng, ghi chú và thứ tự của lưu lượng đã kiểm chứng (một mục đặt ở tầng trên, nhiều mục đặt trong phần tử hints tương ứng), và trong text nêu rõ nó chứng minh lỗ hổng cụ thể nào. Bên gọi không được chỉ bàn giao văn bản mà bỏ mất tham chiếu lưu lượng đã có. Ứng viên chưa kiểm chứng không được truyền làm bằng chứng."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**Bàn giao bằng chứng lưu lượng (tùy chọn)**: việc gắn tự động mặc định do Agent báo cáo hoàn thành sau khi lỗ hổng nhập kho, trước khi viết báo cáo. Người báo cáo nên giữ trong evidence các lệnh xác minh, output then chốt, các ID lưu lượng thật đã có và công dụng, kèm intent_id trong tác vụ để Agent báo cáo truy vết; không cần tra gói tin thêm chỉ để gắn. Khi Auto / Planner báo cáo thay thì không được bỏ mất tham chiếu đã có của người thực thi. add_hint / add_task_hint có thể dùng traffic_refs để bàn giao; gắn tức thì tường minh vẫn tương thích với traffic_refs / evidence_hint_id của report_finding. Khi TCP hoặc không có gói tin thì đăng ký bình thường, không được đoán ID, cũng không dò lại chỉ để bổ sung gói tin."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\nKhi hội thoại nền tảng không có ngữ cảnh tác vụ, không gọi report_finding trực tiếp; hãy bàn giao qua add_task_hint tới tác vụ tương ứng đã có, do Agent của tác vụ đăng ký, và dùng list_task_findings để đối chiếu kết quả."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nTrước khi xác định mục tiêu hoàn thành, hãy hoàn tất việc báo cáo/bàn giao các bằng chứng đã có lần này. Không kết thúc tác vụ, hủy Worker chỉ vì lỗ hổng dạng văn bản đã đăng ký khi việc bàn giao bằng chứng chưa hoàn tất; không có gói tin thì không yêu cầu chờ hay ép bắt gói."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**Tự động liên kết lưu lượng trước khi báo cáo (đã bật)**: bạn chịu trách nhiệm đối chiếu và gắn lưu lượng cho lỗ hổng được kích hoạt lần này, rồi mới viết báo cáo. Trước tiên lấy finding_id và finding_node_id rõ ràng từ JSON trả về của report_finding hoặc get_task_node_detail / list_task_findings. Đọc chi tiết lỗ hổng, bản ghi thực thi của ý đồ tương ứng và danh sách bằng chứng đã có, ưu tiên dùng ID thật mà người báo cáo bàn giao. Nếu lần xác minh này là HTTP và công cụ lưu lượng khả dụng, dùng traffic_search để lọc ứng viên, rồi dùng traffic_get kiểm chứng từng request/response có thực sự hỗ trợ lỗ hổng đó không; tên miền và thời gian chỉ dùng để lọc, không chứng minh quy thuộc. Gắn các bằng chứng đã xác nhận theo thứ tự tái hiện bằng bind_finding_traffic(finding_id, traffic_refs), chọn baseline / proof / verification / supporting và ghi rõ công dụng. Chỉ thao tác trên lỗ hổng lần này, không tạo lại lỗ hổng hay dò lại mục tiêu. Sau khi gắn thành công, gọi lại get_finding_traffic để lấy version mới nhất, đọc phần thân cần thiết, rồi truyền version thực tế đã đọc làm evidence_version cho update_finding_report (tham số finding_id của nó vẫn dùng finding_node_id). Các gắn kết đã có không cần thêm lại. Khi TCP, chưa thu thập, công cụ không khả dụng hoặc không có kết quả khớp chắc chắn, bỏ qua gắn tự động, viết báo cáo bình thường dựa trên bằng chứng văn bản/lệnh và ghi rõ lý do, không được đoán để đủ lưu lượng. Gắn thất bại thì không tuyên bố thành công; giữ lại bằng chứng đã có và nêu trong báo cáo lý do chưa gắn."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Tùy chọn: các tham chiếu lưu lượng đã kiểm chứng và tương ứng với lỗ hổng cụ thể trong hint này, giữ nguyên thứ tự; sau khi bàn giao, report_finding có thể truyền evidence_hint_id để mang theo các tham chiếu này.", "items": obj(map[string]any{"traffic_id": str("ID lưu lượng thật"), "role": str("baseline / proof / verification / supporting"), "note": str("lưu lượng này hỗ trợ kết luận gì")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d phải là node hint của tác vụ này (hint kế thừa không thể dùng trực tiếp để gắn)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
