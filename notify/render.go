package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes cắt s xuống không quá max byte, bảo đảm kết quả là UTF-8 hợp lệ và không
// cắt đứt ký tự.
//
// Tại sao phải cắt theo ranh giới ký tự: markdown của bot nhóm WeCom có giới hạn cứng 4096
// **byte** (không phải số ký tự), mà một chữ Hán tốn 3 byte. Cắt trực tiếp theo byte sẽ
// chẻ đôi một chữ Hán, sinh ra UTF-8 bất hợp lệ — phía nền tảng hoặc từ chối cả tin, hoặc
// hiển thị thành ô vuông lỗi. Cách làm ở đây là từ vị trí ngân sách lùi về byte bắt đầu rune
// gần nhất (utf8.RuneStart phán định byte tiếp nối 0b10xxxxxx).
//
// max<=0 nghĩa là không giới hạn. Sau khi cắt thì nối thêm dấu ba chấm, trừ khi max nhỏ đến
// mức không chứa nổi dấu ba chấm.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max còn ngắn hơn dấu ba chấm: bỏ dấu ba chấm, cắt thuần, tránh kết quả lại vượt max.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine nén văn bản nhiều dòng thành một dòng: gộp mọi khoảng trắng, rồi cắt theo số ký tự.
// Dùng cho dòng tiêu đề của tin nhắn IM — tóm tắt thường có xuống dòng, nhét thẳng vào
// bảng/tiêu đề sẽ làm vỡ bố cục.
// max<=0 nghĩa là không giới hạn độ dài.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes cắt s xuống không quá max ký tự (chứ không phải byte), khi vượt thì nối thêm
// dấu ba chấm. max<=0 nghĩa là không giới hạn.
//
// Khác biệt với TruncateBytes nằm ở khẩu kính của nền tảng: WeCom giới hạn theo byte,
// Telegram giới hạn theo số ký tự. Dùng sai khẩu kính không báo lỗi, chỉ khiến tin nhắn bị
// cắt ngắn hơn dự kiến rất nhiều (1 chữ Hán = 3 byte, cắt theo byte 4096 chỉ còn khoảng 1365
// chữ), nên cả hai hàm đều phải giữ lại, chọn dùng theo kênh.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML cắt đoạn HTML theo số ký tự, đồng thời bảo đảm không sinh ra thẻ còn nửa.
//
// Cắt ký tự trực tiếp trên HTML sẽ tạo ra thẻ khuyết kiểu `<a href="htt`, bộ phân tích của
// nền tảng hoặc báo lỗi từ chối cả tin, hoặc nuốt phần thân sau đó thành giá trị thuộc tính.
// Cách làm ở đây: cắt theo ký tự trước, rồi kiểm tra phần đuôi có `<` chưa đóng không, có thì
// lùi về trước nó.
//
// Không làm cân bằng thẻ (bổ sung </b> chẳng hạn): bộ phân tích HTML của Telegram sẽ tự đóng
// các thẻ chưa đóng, còn tự triển khai cân bằng thì phải xử lý dấu nháy trong thuộc tính,
// comment, thẻ tự đóng — độ phức tạp không tương xứng với lợi ích.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// Nếu đuôi là mảnh bắt đầu bằng `<` (sau lần xuất hiện cuối của `<` không có `>`), lùi về trước `<`.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// Nếu đuôi là HTML entity bị cắt đứt (như `&amp;` bị cắt thành `&amp`), cũng phải lùi lại.
	// Mảnh entity trong một bộ phân tích chỉ nhận entity có thể khiến **cả tin nhắn** bị từ
	// chối — tin nhắn tổng hợp vượt giới hạn độ dài vốn là chuyện thường, không đáng vì thế mà
	// mất cả thông báo.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount tính xem trong ngân sách có thể chứa **trọn vẹn** bao nhiêu mục, để tin nhắn
// tổng hợp đóng gói theo từng mục nguyên.
//
// Tại sao phải đóng theo mục nguyên thay vì render cả bài rồi cắt: cắt sẽ làm nửa sau của các
// mục biến mất vô tung, trong khi bản ghi giao phát của chúng vẫn bị đánh dấu là đã gửi —
// trong tin nhắn không thấy, trong lịch sử giao phát cũng không thấy, lỗ hổng cứ thế mất.
// Sau khi đóng theo mục nguyên, các mục không chứa được sẽ ở lại trong kho thành lô sau, và
// kept mà phía gọi nhận được chính là số mục tin nhắn này thực sự gửi đến.
//
// Tham số: maxSize<=0 nghĩa là không giới hạn; reserve là lượng dự trữ cho đầu/cuối tin nhắn;
// size chịu trách nhiệm đo (khẩu kính mỗi nền tảng khác nhau: WeCom/DingTalk theo byte,
// Telegram theo số ký tự — dùng sai khẩu kính không báo lỗi, chỉ khiến tin nhắn tiếng Trung
// bị nén nhỏ hơn hẳn giới hạn); render dựng mục thứ idx thành văn bản thực tế của nó — độ dài
// khác nhau tùy nội dung, không thể ước lượng.
//
// Ít nhất trả về 1 (miễn là còn mục). Khi một mục đơn lẻ cực kỳ dài cũng phải gửi mục đó đi,
// để bước cắt cuối cùng của phía gọi chống đỡ, nếu không một lỗ hổng quá dài sẽ làm cả lô
// kẹt vĩnh viễn tại chỗ.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize là hai khẩu kính đo của packItemCount, đặt tên ra để chỗ gọi không xuất
// hiện closure trần func(s string) int, nếu không rất khó nhìn ra ngay đang dùng khẩu kính nào.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine render danh sách tài sản thành một dòng văn bản hiển thị, khi vượt limit cái thì
// lược bớt phần còn lại và ghi chú tổng số.
// Một lỗ hổng có thể neo vào vài chục tài sản, liệt kê hết sẽ làm nứt tin nhắn.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " (tổng " + itoa(len(assets)) + " tài sản)"
}

// itoa là bí danh ngắn của strconv.Itoa, chỉ dùng để ghép văn bản hiển thị, tránh phải import
// strconv khắp nơi.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
