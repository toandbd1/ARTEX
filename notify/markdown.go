package notify

import (
	"fmt"
	"strings"
)

// File này là phần render tin nhắn dùng chung cho các kênh "hệ Markdown" (DingTalk, WeCom).
// Feishu dùng card JSON, Telegram dùng HTML, email dùng HTML, mỗi bên render trong adapter của mình.

// maxAssetsShown là số tài sản tối đa được liệt kê trong tin nhắn. Một lỗ hổng có thể neo vào
// vài chục tài sản, liệt kê hết sẽ làm nứt tin nhắn mà không có giá trị thông tin — tên miền
// từ cái thứ 4 trở đi không ai đọc trong IM.
const maxAssetsShown = 3

// maxSummaryRunes là số ký tự mà tóm tắt bị nén xuống. Tin nhắn IM mang tính "gợi ý đi xem
// chi tiết", không phải bản thân báo cáo; nội dung đầy đủ nằm trong nền tảng.
const maxSummaryRunes = 120

// markdownReservedBytes là lượng dự trữ cho đầu tin nhắn (dòng tổng hợp + phân bố mức độ +
// gợi ý cắt bớt nếu có) và phần đuôi (liên kết nền tảng). Khi đóng gói theo từng mục nguyên
// thì trừ phần này ra khỏi ngân sách, bảo đảm đầu đuôi không bị cắt — một khi đầu đuôi bị cắt,
// người đọc ngay cả "đây là lô nào, còn bao nhiêu mục chưa hiển thị" cũng không nhìn ra.
const markdownReservedBytes = 320

// markdownEscape thoát các ký tự meta của markdown.
//
// Tại sao bắt buộc phải làm: tiêu đề lỗ hổng, tóm tắt, loại, tên hiển thị tài sản đều đến từ
// **nguồn không tin cậy** — tiêu đề và tóm tắt xuất thân từ output của mô hình (mô hình đọc
// response của mục tiêu bị kiểm thử), url tài sản thì là URL đầy đủ thu được từ quét (gồm query
// string do mục tiêu kiểm soát). Nếu không thoát, một lỗ hổng có tiêu đề
//
//	Cổng đăng nhập bị SQL injection\n[Khẩn cấp: bấm vào đây xác minh tài khoản](http://attacker.tld)
//
// sẽ render thành **liên kết ngoài có thể bấm** trong DingTalk/Feishu của kỹ sư an ninh; còn
// `![](http://attacker.tld/beacon)` sẽ bị client tải về lúc render, tương đương báo tin "lỗ
// hổng này đã được xem" và rò rỉ IP của người đọc. Kể cả nội dung không ác ý, in đậm hoặc khối
// trích dẫn bị chèn cũng có thể đẩy lỗ hổng nghiêm trọng bên dưới ra khỏi vạch gập.
//
// Tập thoát bao phủ các ký tự loại tiêu đề/liên kết/nhấn mạnh/danh sách/trích dẫn/gạch ngang —
// những ký tự thay đổi cấu trúc hoặc sinh phần tử có thể bấm. `\` phải xử lý đầu tiên, nếu
// không sẽ thoát lại cả các dấu gạch chéo ngược vừa bổ sung sau.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText nén văn bản không tin cậy thành một dòng rồi thoát, dùng cho phần thân markdown.
// Đưa về một dòng là nửa còn lại ngoài việc thoát: bản thân dấu xuống dòng đã có thể giả mạo
// mục danh sách hoặc khối trích dẫn mới, mà ký tự thoát không chặn được nó.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle trả về tiêu đề tin nhắn (thanh tiêu đề của nền tảng IM / tiêu đề card), nội
// dung là **văn bản gốc chưa thoát**.
//
// Chỗ này cố ý không thoát: tiêu đề này được bốn ngữ cảnh render dùng chung — phần thân
// markdown, HTML của Telegram, plain_text của card Feishu, và JSON của Webhook tổng quát cùng
// chủ đề email. Quy tắc thoát của mỗi ngữ cảnh đều khác nhau (thoát markdown nhét vào HTML sẽ
// để lại dấu gạch chéo ngược nhìn thấy được, nhét vào JSON sẽ làm bẩn dữ liệu), nên việc thoát
// phải do đầu ra của từng bên chịu trách nhiệm, xem writeItem / feishuItemLines /
// telegramEscape. Đã từng thêm thoát markdown vào hàm dùng chung, kết quả trong tin nhắn
// Telegram xuất hiện kiểu `\(1\)` — dấu gạch chéo ngược nhìn thấy được.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Tổng hợp lỗ hổng · tổng %d mục", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Thông báo lỗ hổng"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody render phần thân tin nhắn, trả về phần thân và **số mục thực tế đã ghi vào**.
//
// Giá trị trả về kept là số mục lần giao phát này thực sự gửi đến, phía gọi căn cứ vào đó chỉ
// đánh dấu kept mục đầu là đã gửi — các mục bị giới hạn độ dài của kênh chặn ngoài phải để
// dành cho lô sau, chứ không được đánh dấu thành công theo. Đây chính là nguồn gốc của "mất
// âm thầm": tin nhắn bị cắt nhưng bản ghi giao phát hiển thị đã gửi hết, không nơi nào nhìn ra
// nửa sau chưa từng được gửi đi.
//
// maxBytes<=0 nghĩa là không giới hạn.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// Tin nhắn đơn lẻ dù quá dài cũng vẫn gửi (để bước cắt cuối cùng chống đỡ): một phần
		// thông tin của lỗ hổng vẫn hơn là không gửi gì.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[Xem tất cả trên nền tảng](%s)\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro render phần mở đầu của tin nhắn tổng hợp: cửa sổ thời gian, số mục và
// phân bố mức độ. Có những thứ này, người nhận tổng hợp không cần bấm vào nền tảng cũng phán
// đoán được lô này có cần xử lý ngay không.
//
// items là các mục **thực tế chứa được**, total là tổng số đáng lẽ của lô này. Khi hai giá trị
// khác nhau phải nói rõ "còn bao nhiêu mục ở tin nhắn tiếp theo" — nếu không người đọc sẽ tưởng
// con số ghi ở đầu tin nhắn là tất cả, còn những mục chưa từng được gửi phía sau hoàn toàn
// không tồn tại trên giao diện.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**%d lỗ hổng mới trong %d phút gần đây**", total, m.WindowMinutes)
	} else {
		fmt.Fprintf(&b, "**%d lỗ hổng mới**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, "(bản tin này hiển thị %d mục đầu, %d mục còn lại sẽ tiếp tục ở tin nhắn tiếp theo)", len(items), extra)
	}
	// Đưa ra phân bố theo mức độ, để người đọc nhìn một cái là thấy có mục nghiêm trọng không.
	// Chỉ thống kê các mục **thực tế có trong bản tin này**, bảo đảm "Nghiêm trọng 3" khớp với
	// số mục đếm được bên dưới.
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem render một mục lỗ hổng đơn lẻ.
//
// prefix dùng cho số thứ tự trong danh sách tổng hợp; single=true thì render bản đầy đủ (gồm
// tóm tắt và liên kết quay lại), trong danh sách tổng hợp chỉ render một dòng tóm gọn — nếu
// không 50 mục tổng hợp sẽ thành một tài liệu dài.
//
// Mọi nội dung đến từ bên ngoài (tiêu đề/loại/tài sản/tóm tắt) đều đi qua markdownText: đưa về
// một dòng + thoát. Liên kết quay lại được ghép từ public_base_url do quản trị viên cấu hình,
// không phải nội dung không tin cậy, và phải là liên kết bấm được, nên xuất nguyên bản.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// Chế độ tổng hợp: hiển thị một dòng, tài sản và tóm tắt nén lại đi theo phía sau.
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**Thay đổi trạng thái**: %s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**Loại**: %s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**Tài sản**: %s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**Tóm tắt**: %s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[Xem chi tiết](%s)\n", it.DetailURL)
	}
}
