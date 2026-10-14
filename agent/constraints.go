package agent

import (
	"strings"

	"github.com/Autumn-27/artex/db"
)

// constraintBlock renders this task's operation constraints (task_constraints) as a
// high-priority block appended to the planner/worker system prompt. allow/deny are
// grouped; empty string when there are no constraints (or ts is nil). The framing
// deliberately puts these ABOVE the exploration/expansion heuristics so a declared
// boundary wins the tug-of-war against "chase another entry surface".
func constraintBlock(ts *db.ExplorationStore) string {
	if ts == nil {
		return ""
	}
	rows, err := ts.ListConstraints()
	if err != nil || len(rows) == 0 {
		return ""
	}
	var allow, deny []string
	for _, c := range rows {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		if c.Kind == "allow" {
			allow = append(allow, "- "+text)
		} else {
			deny = append(deny, "- "+text)
		}
	}
	if len(allow) == 0 && len(deny) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【Ràng buộc thao tác (ưu tiên cao nhất, át mọi heuristic thám hiểm/mở rộng bề mặt bên dưới; trước khi tạo mỗi ý đồ, trước khi thực thi mỗi hành động đều phải tự kiểm tra xem có vi phạm không, vi phạm thì không được tiến hành)】:")
	if len(allow) > 0 {
		b.WriteString("\nCác thao tác được phép:\n")
		b.WriteString(strings.Join(allow, "\n"))
	}
	if len(deny) > 0 {
		b.WriteString("\nCác thao tác bị cấm:\n")
		b.WriteString(strings.Join(deny, "\n"))
	}
	b.WriteString("\n(Phát hiện mục tiêu/cổng/host mới ngoài ràng buộc không đồng nghĩa được cấp quyền: trừ khi nó nằm trong phạm vi cho phép ở trên, nếu không hãy ghi là fact out-of-scope và bỏ qua, không được tạo ý đồ hay thực thi hành động cho nó.)")
	return b.String()
}
