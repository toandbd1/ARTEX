package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel là adapter Webhook tổng quát: người dùng tự định nghĩa URL, method, header
// yêu cầu và template JSON. Sự tồn tại của nó khiến tính năng này không cần viết riêng một
// triển khai cho Slack / Mattermost / Discord / hệ thống tự dựng — những nền tảng đó đều được
// bao phủ bởi một template có thể cấu hình.
type webhookChannel struct{}

func (webhookChannel) Kind() string { return KindWebhook }

// Webhook tổng quát không có giới hạn chính thức, trả về 0 nghĩa là mặc định không giới hạn
// tần suất, để người dùng tự đặt theo năng lực phía đối diện.
func (webhookChannel) DefaultRatePerMin() int { return 0 }

// Che url và headers: bản thân địa chỉ đích thường mang token, header tùy chỉnh thường chứa
// thông tin xác thực, cả hai đều xuất hiện trong phần hiển thị lại của API nên đều phải chặn.
// Cái giá phải trả là khi chỉnh sửa muốn đổi một header thì phải điền lại cả nhóm header (giá
// trị mask sẽ được hiểu là "giữ nguyên giá trị gốc") — lựa chọn này là có chủ đích: thà điền
// thêm một lần, cũng không hiển thị thông tin xác thực ra trình duyệt.
func (webhookChannel) SecretKeys() []string { return []string{"url", "headers"} }

// Đích đến là url. Khi đổi url phải xác lập lại headers — nếu không header Authorization gốc
// sẽ bị gửi nguyên bản đến địa chỉ mới, đây chính là đường lách chính của việc vượt qua che.
func (webhookChannel) DestinationKeys() []string { return []string{"url"} }

// webhookDefaultTemplate là body yêu cầu dự phòng khi chưa điền template: một cấu trúc JSON
// thẳng thắn, bao phủ đại đa số đầu nhận tự dựng kiểu "nhận một JSON lưu vào kho".
const webhookDefaultTemplate = `{
  "title": {{json .Title}},
  "batch": {{.Batch}},
  "count": {{.Count}},
  "items": [
{{- range $i, $it := .Items}}
{{- if $i}},{{end}}
    {
      "finding_id": {{$it.FindingID}},
      "name": {{json $it.Name}},
      "vulnclass": {{json $it.VulnClass}},
      "severity": {{json $it.Severity}},
      "summary": {{json $it.Summary}},
      "assets": {{json $it.Assets}},
      "detail_url": {{json $it.DetailURL}}
    }
{{- end}}
  ]
}`

// webhookTemplateData là ngữ cảnh được lộ ra cho template của người dùng.
type webhookTemplateData struct {
	Title   string
	Batch   bool
	Count   int
	Items   []webhookItem
	HomeURL string
	// SentAt là thời điểm giao phát lần này (RFC3339), để đầu nhận ghi lại.
	SentAt string
}

type webhookItem struct {
	FindingID     int64
	Name          string
	VulnClass     string
	Severity      string
	SeverityLabel string
	Summary       string
	Assets        []string
	DetailURL     string
	FromStatus    string
	ToStatus      string
	// StatusLabel là mô tả dễ đọc của thay đổi trạng thái, như "Chờ xử lý → Đã vá"; khi không
	// phải thay đổi trạng thái thì rỗng.
	StatusLabel string
}

func (webhookChannel) Validate(cfg map[string]any) error {
	raw := cfgString(cfg, "url")
	if raw == "" {
		return errors.New("thiếu URL đích")
	}
	if err := validateHTTPURL(raw); err != nil {
		return fmt.Errorf("URL đích không hợp lệ: %w", err)
	}
	if m := strings.ToUpper(cfgString(cfg, "method")); m != "" && m != http.MethodGet && m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch {
		return fmt.Errorf("method không được hỗ trợ %s (khả dụng GET/POST/PUT/PATCH)", m)
	}
	if tpl := cfgString(cfg, "body_template"); tpl != "" {
		if _, err := parseWebhookTemplate(tpl); err != nil {
			return fmt.Errorf("lỗi cú pháp template body yêu cầu: %w", err)
		}
	}
	return nil
}

func (c webhookChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	method := strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET không mang body yêu cầu: nhét nội dung vào query vượt quá năng lực của template, cũng
	// không hợp ngữ nghĩa GET, nên GET chỉ phù hợp với kiểu đầu nhận "khớp là kích hoạt hook".
	var payload any
	if method != http.MethodGet {
		body, err := renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err != nil {
			return 0, Permanent(err)
		}
		// Thứ template render ra là JSON dạng chuỗi, chỗ này chuyển thành json.RawMessage gửi đi
		// nguyên bản, tránh thoát lần hai gói cấu trúc người dùng khéo léo dựng vào một chuỗi JSON.
		if !json.Valid([]byte(body)) {
			return 0, Permanent(errors.New("kết quả render của template body yêu cầu không phải JSON hợp lệ"))
		}
		payload = json.RawMessage(body)
	}

	headers := cfgMap(cfg, "headers")
	if ct := cfgString(cfg, "content_type"); ct != "" {
		// Cho phép ghi đè, nhưng áp dụng sau headers, bảo đảm cấu hình tường minh được ưu tiên.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	if _, err := doJSON(ctx, method, cfgString(cfg, "url"), headers, payload); err != nil {
		return 0, err
	}
	// Webhook tổng quát không cắt phần thân (đầu nhận là dịch vụ của chính người dùng, dung
	// lượng do body_template quyết định), nên cả lô đều tính là đã gửi đến.
	return len(m.Items), nil
}

// renderWebhookBody render body yêu cầu bằng template của người dùng (hoặc template mặc định).
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err := parseWebhookTemplate(tpl)
	if err != nil {
		return "", fmt.Errorf("lỗi cú pháp template body yêu cầu: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, newWebhookTemplateData(m)); err != nil {
		return "", fmt.Errorf("render template body yêu cầu thất bại: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate phân tích template.
//
// missingkey=zero khiến khóa map thiếu render thành zero-value thay vì báo lỗi — nhưng ngữ cảnh
// của file này là struct, tác dụng chính là để range không báo lỗi khi .Items rỗng. Thứ thực sự
// cần phòng là .Items là nil.
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs là các hàm phụ trợ được lộ ra cho template.
var webhookTemplateFuncs = template.FuncMap{
	// json tuần tự hóa giá trị bất kỳ thành JSON.
	//
	// Hàm này không phải thêm cho đẹp mà là bắt buộc: bỏ nó đi, người dùng chỉ viết được
	// {{.Title}} nội suy trực tiếp, mà chỉ cần trong tiêu đề lỗ hổng có dấu nháy hoặc xuống dòng
	// thì cả đoạn body yêu cầu không còn là JSON hợp lệ — đầu nhận sẽ từ chối, và thông báo lỗi
	// trỏ về "phân tích JSON thất bại", hoàn toàn không liên tưởng được đến việc trong tiêu đề có
	// một dấu nháy.
	"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons dùng để nhét một mảnh JSON vào bên trong giá trị chuỗi JSON khác (làm một lớp thoát
	// chuỗi).
	"jsons": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		quoted, err := json.Marshal(string(raw))
		if err != nil {
			return "", err
		}
		// Bỏ dấu nháy ngoài cùng: phía gọi tự quyết định có thêm dấu nháy hay không.
		return string(quoted[1 : len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d := webhookTemplateData{
		Title:   markdownTitle(m),
		Batch:   m.Batch,
		Count:   len(m.Items),
		HomeURL: m.HomeURL,
		SentAt:  time.Now().Format(time.RFC3339),
		Items:   make([]webhookItem, 0, len(m.Items)),
	}
	for _, it := range m.Items {
		wi := webhookItem{
			FindingID:     it.FindingID,
			Name:          it.Name,
			VulnClass:     it.VulnClass,
			Severity:      it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary:       it.Summary,
			Assets:        append([]string{}, it.Assets...),
			DetailURL:     it.DetailURL,
			FromStatus:    it.FromStatus,
			ToStatus:      it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
