package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Render cho nút "Xuất" của trang phát hiện: render một loạt dòng bảng findings thành Markdown tổng hợp, Markdown từng mục,
// hoặc CSV. JSON do tầng server serialize trực tiếp bằng DTO, không nằm ở đây.

// sortFindingsForExport sắp xếp theo mức độ nghiêm trọng giảm dần, rồi theo thời gian mới trước, khớp với nhóm của báo cáo tổng hợp.
func sortFindingsForExport(fs []*db.DBFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		ri, rj := sevRank[fs[i].Severity], sevRank[fs[j].Severity]
		if ri != rj {
			return ri < rj // sevRank càng nhỏ càng nghiêm trọng
		}
		return fs[i].CreatedAt.After(fs[j].CreatedAt)
	})
}

// findingTitle lấy tiêu đề đọc được của lỗ hổng: tên → loại → "Chưa phân loại".
func findingTitle(f *db.DBFinding) string {
	return nz(f.Name, nz(f.VulnClass, "Chưa phân loại"))
}

// FindingsMarkdown gộp một loạt findings thành một báo cáo tổng hợp (tóm tắt + nhóm theo mức độ nghiêm trọng,
// mỗi mục gồm loại/trạng thái/tác vụ sở hữu/bằng chứng/báo cáo chi tiết).
func FindingsMarkdown(fs []*db.DBFinding, generatedAt time.Time) string {
	items := append([]*db.DBFinding(nil), fs...)
	sortFindingsForExport(items)

	var b strings.Builder
	b.WriteString("# Báo cáo tổng hợp phát hiện lỗ hổng\n\n")
	fmt.Fprintf(&b, "- **Thời gian tạo**: %s\n", generatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **Tổng số phát hiện**: %d\n\n", len(items))

	// Tóm tắt: đếm theo từng mức độ nghiêm trọng.
	counts := map[string]int{}
	for _, f := range items {
		counts[f.Severity]++
	}
	b.WriteString("## Tóm tắt\n\n")
	b.WriteString("| Mức độ nghiêm trọng | Số lượng |\n| --- | --- |\n")
	for _, s := range []struct{ key, label string }{
		{"critical", "Nghiêm trọng"}, {"high", "Cao"}, {"medium", "Trung bình"}, {"low", "Thấp"},
	} {
		fmt.Fprintf(&b, "| %s | %d |\n", s.label, counts[s.key])
	}
	b.WriteString("\n")

	if len(items) == 0 {
		b.WriteString("_Không có lỗ hổng phù hợp._\n")
		return b.String()
	}

	b.WriteString("## Chi tiết lỗ hổng\n\n")
	for i, f := range items {
		fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
		if f.VulnClass != "" {
			fmt.Fprintf(&b, "- **Loại**: %s\n", f.VulnClass)
		}
		fmt.Fprintf(&b, "- **Trạng thái**: %s\n", nz(f.Status, "pending"))
		if desc := strings.TrimSpace(f.TaskDescription); desc != "" {
			fmt.Fprintf(&b, "- **Thuộc tác vụ**: %s\n", desc)
		}
		fmt.Fprintf(&b, "- **Thời gian phát hiện**: %s\n\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
		if s := strings.TrimSpace(f.Summary); s != "" {
			fmt.Fprintf(&b, "%s\n\n", s)
		}
		if e := strings.TrimSpace(f.Evidence); e != "" {
			fmt.Fprintf(&b, "**Bằng chứng:**\n\n```\n%s\n```\n\n", e)
		}
		if rep := strings.TrimSpace(f.Report); rep != "" {
			b.WriteString("**Báo cáo chi tiết:**\n\n")
			b.WriteString(rep)
			b.WriteString("\n\n")
		}
		b.WriteString(findingTrafficMarkdown(f, false))
		b.WriteString("---\n\n")
	}
	return b.String()
}

// SingleFindingMarkdown render một lỗ hổng thành một Markdown độc lập (dùng để đóng gói "một lỗ hổng một file").
func SingleFindingMarkdown(f *db.DBFinding, generatedAt time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# [%s] %s\n\n", strings.ToUpper(nz(f.Severity, "info")), findingTitle(f))
	if f.VulnClass != "" {
		fmt.Fprintf(&b, "- **Loại**: %s\n", f.VulnClass)
	}
	fmt.Fprintf(&b, "- **Mức độ nghiêm trọng**: %s\n", nz(f.Severity, "info"))
	fmt.Fprintf(&b, "- **Trạng thái**: %s\n", nz(f.Status, "pending"))
	if desc := strings.TrimSpace(f.TaskDescription); desc != "" {
		fmt.Fprintf(&b, "- **Thuộc tác vụ**: %s\n", desc)
	}
	fmt.Fprintf(&b, "- **Thời gian phát hiện**: %s\n", f.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- **Thời gian tạo**: %s\n\n", generatedAt.Format("2006-01-02 15:04:05"))
	if s := strings.TrimSpace(f.Summary); s != "" {
		fmt.Fprintf(&b, "## Tổng quan\n\n%s\n\n", s)
	}
	if e := strings.TrimSpace(f.Evidence); e != "" {
		fmt.Fprintf(&b, "## Bằng chứng\n\n```\n%s\n```\n\n", e)
	}
	if rep := strings.TrimSpace(f.Report); rep != "" {
		b.WriteString("## Báo cáo chi tiết\n\n")
		b.WriteString(rep)
		b.WriteString("\n")
	}
	b.WriteString(findingTrafficMarkdown(f, true))
	return b.String()
}

var unsafeFilenameChars = regexp.MustCompile(`[^\p{Han}\p{L}\p{N}._-]+`)

// FindingFilename tạo tên file .md an toàn cho "một lỗ hổng một file", dạng
// `critical_SQLi_#123.md`. Loại bỏ dấu phân cách đường dẫn và ký tự điều khiển, tránh đường dẫn không hợp lệ trong zip.
func FindingFilename(f *db.DBFinding) string {
	sev := nz(f.Severity, "info")
	title := findingTitle(f)
	name := fmt.Sprintf("%s_%s_#%d", sev, title, f.ID)
	name = unsafeFilenameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	if name == "" {
		name = fmt.Sprintf("finding_%d", f.ID)
	}
	// Phòng thủ: tách thêm một lớp đường dẫn nữa, loại bỏ hoàn toàn zip slip.
	name = path.Base(name)
	if len(name) > 120 {
		name = name[:120]
	}
	return name + ".md"
}

// FindingsCSV render một loạt findings thành CSV (kèm UTF-8 BOM, giúp Excel nhận đúng tiếng Việt/Unicode).
// Không chứa toàn văn report/evidence dài, chỉ đặt các trường dạng tóm tắt; cần toàn văn hãy dùng xuất Markdown/JSON.
func FindingsCSV(fs []*db.DBFinding) []byte {
	items := append([]*db.DBFinding(nil), fs...)
	sortFindingsForExport(items)

	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // UTF-8 BOM
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "Tên", "Loại", "Mức độ nghiêm trọng", "Trạng thái", "Thuộc tác vụ", "Thời gian phát hiện", "Tổng quan", "Số lượng bằng chứng lưu lượng", "ID bằng chứng lưu lượng"})
	for _, f := range items {
		_ = w.Write([]string{
			fmt.Sprintf("%d", f.ID),
			findingTitle(f),
			f.VulnClass,
			nz(f.Severity, "info"),
			nz(f.Status, "pending"),
			f.TaskDescription,
			f.CreatedAt.Format("2006-01-02 15:04:05"),
			strings.TrimSpace(f.Summary),
			fmt.Sprint(len(f.TrafficBindings)), findingTrafficIDs(f),
		})
	}
	w.Flush()
	return buf.Bytes()
}

func findingTrafficIDs(f *db.DBFinding) string {
	ids := make([]string, 0, len(f.TrafficBindings))
	for _, b := range f.TrafficBindings {
		ids = append(ids, fmt.Sprint(b.ID))
	}
	return strings.Join(ids, ",")
}

func findingTrafficMarkdown(f *db.DBFinding, attachments bool) string {
	stale := f.Report != "" && f.EvidenceVersion != f.ReportEvidenceVersion
	if len(f.TrafficBindings) == 0 && !stale {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n## Bằng chứng lưu lượng liên quan\n\n")
	fmt.Fprintf(&out, "Phiên bản bằng chứng: %d; số lượng liên kết: %d.\n\n", f.EvidenceVersion, len(f.TrafficBindings))
	if stale {
		out.WriteString("Bằng chứng đã thay đổi, báo cáo chi tiết chờ cập nhật.\n\n")
	}
	for i, b := range f.TrafficBindings {
		fmt.Fprintf(&out, "%d. **Bằng chứng #%d · %s** — `%s %s`, mã trạng thái %d\n", i+1, b.ID, b.Role, b.Snapshot.Method, strings.ReplaceAll(b.Snapshot.URL, "`", "%60"), b.Snapshot.Status)
		if b.Note != "" {
			fmt.Fprintf(&out, "   %s\n", strings.ReplaceAll(b.Note, "\n", "\n   "))
		}
		if attachments {
			fmt.Fprintf(&out, "   [Gói tin request](evidence/%d/%d/request.http) · [Gói tin response](evidence/%d/%d/response.http)\n", f.ID, b.ID, f.ID, b.ID)
		}
	}
	out.WriteString("\n")
	return out.String()
}
