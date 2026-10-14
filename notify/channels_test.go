package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// singleMsg dựng một tin nhắn đơn lẻ có dấu nháy và xuống dòng. Cố ý dùng tiêu đề/tóm tắt
// chứa `"` và `\n`: đây chính là input dễ sinh ra JSON bất hợp lệ nhất khi template nội suy.
func singleMsg() Message {
	return Message{
		Items: []Item{{
			FindingID: 42,
			Name:      `Cổng đăng nhập có rủi ro "SQL injection"`,
			VulnClass: "SQL injection",
			Severity:  "high",
			Summary:   "tham số id\nkhông lọc dẫn đến injection",
			Assets:    []string{"a.example.com", "b.example.com"},
			DetailURL: "https://artex.local/function/findings/detail?id=42",
		}},
	}
}

// batchMsg dựng một lô tin nhắn tổng hợp.
func batchMsg(n int) Message {
	m := Message{Batch: true, WindowMinutes: 30, HomeURL: "https://artex.local/function/findings"}
	for i := 0; i < n; i++ {
		m.Items = append(m.Items, Item{
			FindingID: int64(i + 1),
			Name:      "Lỗ hổng" + itoa(i+1),
			VulnClass: "XSS",
			Severity:  "medium",
			Summary:   "Cross-site scripting dạng phản xạ",
			Assets:    []string{"target.example.com"},
		})
	}
	return m
}

// capturePost dựng một đầu nhận giả, truyền body yêu cầu và header nhận được về cho hàm assert.
func capturePost(t *testing.T, respBody string, assert func(t *testing.T, body map[string]any, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("body yêu cầu không phải JSON hợp lệ: %v\nnguyên văn: %s", err, raw)
			}
		}
		if assert != nil {
			assert(t, body, r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDingTalkSendsActionCardWhenLinkPresent(t *testing.T) {
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"] != "actionCard" {
			t.Fatalf("khi có liên kết quay lại phải gửi actionCard, nhận được %v", body["msgtype"])
		}
		card, _ := body["actionCard"].(map[string]any)
		if card["singleURL"] != "https://artex.local/function/findings/detail?id=42" {
			t.Errorf("liên kết quay lại bị mất: %v", card["singleURL"])
		}
	})
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
}

func TestDingTalkFallsBackToMarkdownForBatch(t *testing.T) {
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"] != "markdown" {
			t.Fatalf("tin nhắn tổng hợp phải gửi markdown, nhận được %v", body["msgtype"])
		}
		md, _ := body["markdown"].(map[string]any)
		if !strings.Contains(md["text"].(string), "30 phút") {
			t.Errorf("phần thân tổng hợp thiếu cửa sổ thời gian: %v", md["text"])
		}
	})
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, batchMsg(3)); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
}

// TestDingTalkBusinessErrorIsPermanent khóa chặt phán định "HTTP 200 nhưng errcode khác 0".
// Không kiểm tra errcode sẽ ghi nhận thất bại giao phát thành thành công — đây là cái bẫy chung
// của các nền tảng IM.
func TestDingTalkBusinessErrorIsPermanent(t *testing.T) {
	srv := capturePost(t, `{"errcode":310000,"errmsg":"keywords not in content"}`, nil)
	_, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
	if err == nil {
		t.Fatal("errcode khác 0 phải báo lỗi")
	}
	if !IsPermanent(err) {
		t.Fatalf("từ khóa không khớp thuộc lỗi cấu hình, phải đánh dấu là thất bại vĩnh viễn, nhận được %v", err)
	}
	if !strings.Contains(err.Error(), "310000") {
		t.Errorf("thông báo lỗi phải kèm mã lỗi của nền tảng, nhận được %v", err)
	}
}

func TestWeComTruncatesCJKWithinByteLimit(t *testing.T) {
	var contentLen int
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		md, _ := body["markdown"].(map[string]any)
		content, _ := md["content"].(string)
		contentLen = len(content)
		if !utf8.ValidString(content) {
			t.Fatal("sau khi cắt không phải UTF-8 hợp lệ — WeCom sẽ từ chối cả tin")
		}
	})
	// Tạo một lô tổng hợp đủ dài, chắc chắn vượt 4096 byte.
	m := batchMsg(200)
	if _, err := (weComChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, m); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
	if contentLen > weComMarkdownLimit {
		t.Fatalf("phần thân %d byte vượt giới hạn WeCom %d", contentLen, weComMarkdownLimit)
	}
	if contentLen == 0 {
		t.Fatal("phần thân rỗng")
	}
}

func TestWeComRateLimitIsRetryableButKeyErrorIsPermanent(t *testing.T) {
	limited := capturePost(t, `{"errcode":45009,"errmsg":"api freq out of limit"}`, nil)
	_, err := (weComChannel{}).Send(context.Background(), map[string]any{"webhook": limited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("45009 là giới hạn tần suất cửa sổ trôi, phải có thể thử lại, nhận được %v", err)
	}

	badKey := capturePost(t, `{"errcode":93000,"errmsg":"invalid webhook url"}`, nil)
	_, err = (weComChannel{}).Send(context.Background(), map[string]any{"webhook": badKey.URL}, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("93000 là key không hợp lệ, thử lại không tự khỏi, phải là thất bại vĩnh viễn, nhận được %v", err)
	}
}

func TestFeishuCardStructureAndSign(t *testing.T) {
	const secret = "SECtest123"
	srv := capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msg_type"] != "interactive" {
			t.Fatalf("phải gửi card tương tác, nhận được %v", body["msg_type"])
		}
		card, _ := body["card"].(map[string]any)
		header, _ := card["header"].(map[string]any)
		if header["template"] != "orange" {
			t.Errorf("mức high phải có phối màu orange, nhận được %v", header["template"])
		}
		// Đã cấu hình secret thì phải mang tham số ký, nếu không Feishu sẽ từ chối bằng 19021.
		if body["sign"] == nil || body["timestamp"] == nil {
			t.Fatalf("thiếu tham số ký: %v", body)
		}
		// Trong các phần tử card phải có một nút, url của nó trỏ tới chi tiết lỗ hổng.
		elements, _ := card["elements"].([]any)
		foundButton := false
		for _, e := range elements {
			em, _ := e.(map[string]any)
			if em["tag"] != "action" {
				continue
			}
			actions, _ := em["actions"].([]any)
			for _, a := range actions {
				am, _ := a.(map[string]any)
				if am["url"] == "https://artex.local/function/findings/detail?id=42" {
					foundButton = true
				}
			}
		}
		if !foundButton {
			t.Fatal("trong card không có nút trỏ tới trang chi tiết")
		}
	})
	cfg := map[string]any{"webhook": srv.URL, "secret": secret}
	if _, err := (feishuChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
}

func TestFeishuWithoutSecretOmitsSign(t *testing.T) {
	srv := capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["sign"] != nil || body["timestamp"] != nil {
			t.Fatalf("khi chưa cấu hình secret không được mang tham số ký: %v", body)
		}
	})
	if _, err := (feishuChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
}

func TestTelegramEscapesHTMLInUntrustedContent(t *testing.T) {
	var text string
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		text, _ = body["text"].(string)
		if body["parse_mode"] != "HTML" {
			t.Fatalf("phải dùng chế độ phân tích HTML, nhận được %v", body["parse_mode"])
		}
	})
	m := Message{Items: []Item{{
		Severity: "high",
		// Tiêu đề và tóm tắt đến từ mục tiêu bị kiểm thử/output của mô hình, là nội dung không
		// tin cậy.
		Name:    `<script>alert(1)</script>`,
		Summary: "a & b < c",
	}}}
	if _, err := (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": srv.URL}, m); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
	if strings.Contains(text, "<script>") {
		t.Fatalf("chưa thoát HTML, tồn tại chèn mã: %q", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("mong đợi entity đã thoát, nhận được %q", text)
	}
	if !strings.Contains(text, "a &amp; b") {
		t.Fatalf("& chưa thoát, nhận được %q", text)
	}
}

func TestTelegramErrorClassification(t *testing.T) {
	rateLimited := capturePost(t, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`, nil)
	_, err := (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": rateLimited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("429 phải có thể thử lại, nhận được %v", err)
	}

	forbidden := capturePost(t, `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`, nil)
	_, err = (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": forbidden.URL}, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("403 là vấn đề cấu hình, phải là thất bại vĩnh viễn, nhận được %v", err)
	}
}

func TestWebhookDefaultTemplateProducesValidJSON(t *testing.T) {
	// Ca này chính là ý nghĩa tồn tại của template mặc định: khi trong tiêu đề có dấu nháy và
	// xuống dòng, mọi cách viết ngây thơ kiểu `"title": "{{.Title}}"` đều sinh ra JSON bất hợp
	// lệ. {{json .}} thì không.
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["title"] != `[🟠 Cao] Cổng đăng nhập có rủi ro "SQL injection"` {
			t.Errorf("tiêu đề chưa được khôi phục đúng: %v", body["title"])
		}
		items, _ := body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("số lượng items phải là 1, nhận được %d", len(items))
		}
		it, _ := items[0].(map[string]any)
		if it["summary"] != "tham số id\nkhông lọc dẫn đến injection" {
			t.Errorf("tóm tắt chưa được khôi phục đúng: %v", it["summary"])
		}
		// Giá trị số phải là JSON number chứ không phải chuỗi (cách viết kiểu
		// json:"...,string" sẽ sập cái bẫy này).
		if _, ok := it["finding_id"].(float64); !ok {
			t.Errorf("finding_id phải là số, nhận được %T", it["finding_id"])
		}
	})
	if _, err := (webhookChannel{}).Send(context.Background(), map[string]any{"url": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
}

func TestWebhookCustomTemplateAndHeaders(t *testing.T) {
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, r *http.Request) {
		if r.Header.Get("X-Token") != "s3cret" {
			t.Errorf("header tùy chỉnh bị mất: %v", r.Header)
		}
		if body["msg"] != "3 mục" {
			t.Errorf("template tùy chỉnh render có vấn đề: %v", body["msg"])
		}
		if body["first"] != "Lỗ hổng1" {
			t.Errorf("trích xuất range có vấn đề: %v", body["first"])
		}
	})
	cfg := map[string]any{
		"url":           srv.URL,
		"headers":       map[string]any{"X-Token": "s3cret"},
		"body_template": `{"msg": {{json (printf "%d mục" .Count)}}, "first": {{json (index .Items 0).Name}}}`,
	}
	if _, err := (webhookChannel{}).Send(context.Background(), cfg, batchMsg(3)); err != nil {
		t.Fatalf("giao phát thất bại: %v", err)
	}
}

func TestWebhookRejectsNonJSONRenderResult(t *testing.T) {
	cfg := map[string]any{"url": "https://example.com/hook", "body_template": `not json at all`}
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("kết quả render không phải JSON phải là thất bại vĩnh viễn (template viết sai, thử lại vô dụng), nhận được %v", err)
	}
}

func TestWebhookValidateCatchesBadConfigEarly(t *testing.T) {
	bad := []map[string]any{
		{},
		{"url": "file:///etc/passwd"},
		{"url": "https://example.com", "method": "DELETE"},
		{"url": "https://example.com", "body_template": `{{.Items.`},
	}
	for i, cfg := range bad {
		if err := (webhookChannel{}).Validate(cfg); err == nil {
			t.Errorf("nhóm cấu hình thứ %d phải bị từ chối: %v", i, cfg)
		}
	}
}

func TestEmailMessageIsWellFormed(t *testing.T) {
	msg, err := buildEmailMessage("artex@example.com", []string{"a@example.com", "b@example.com"}, singleMsg())
	if err != nil {
		t.Fatalf("lắp ráp email thất bại: %v", err)
	}
	if !strings.HasPrefix(msg, "From: artex@example.com\r\n") {
		t.Fatalf("header From có vấn đề:\n%s", msg)
	}
	if !strings.Contains(msg, "To: a@example.com, b@example.com\r\n") {
		t.Fatalf("header To có vấn đề:\n%s", msg)
	}
	// Chủ đề không phải ASCII phải mã hóa RFC 2047, nếu không client sẽ hiển thị thành mã loạn.
	if !strings.Contains(msg, "Subject: =?utf-8?") {
		t.Fatalf("chủ đề chưa được mã hóa RFC 2047:\n%s", msg)
	}
	if dec, err := new(mime.WordDecoder).DecodeHeader(mustExtractHeader(t, msg, "Subject")); err != nil {
		t.Fatalf("không thể giải mã chủ đề: %v", err)
	} else if !strings.Contains(dec, "SQL injection") {
		t.Fatalf("nội dung chủ đề sau giải mã có vấn đề: %q", dec)
	}

	// Phần thân là base64, giải ra phải là HTML hợp lệ.
	parts := strings.SplitN(msg, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatal("email thiếu phân cách header/body")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(parts[1]), "\r\n", ""))
	if err != nil {
		t.Fatalf("giải mã base64 phần thân thất bại: %v", err)
	}
	html := string(decoded)
	if !strings.HasPrefix(html, "<div") {
		t.Fatalf("phần thân không phải HTML: %.80s", html)
	}
	// Dấu nháy trong tiêu đề xuất hiện nguyên bản ở vị trí văn bản: dấu nháy kép trong nội dung
	// văn bản HTML là ký tự hợp lệ, không cần thoát. Assert "giữ nguyên bản" ở đây là để ngăn về
	// sau có người lỡ thêm một lớp thoát dấu nháy, khiến dấu nháy hiển thị thành &quot;.
	if !strings.Contains(html, `"SQL injection"`) {
		t.Fatalf("dấu nháy trong tiêu đề ở vị trí văn bản phải giữ nguyên bản: %.200s", html)
	}
}

// TestEmailEscapesStructuralInjection bao phủ kiểu chèn mà phần thân email thực sự phải phòng:
// tiêu đề và tóm tắt lỗ hổng đến từ mục tiêu bị kiểm thử và output của mô hình, là nội dung
// không tin cậy. Vị trí văn bản phải thoát & < > (nếu không có thể chèn thẻ), vị trí thuộc tính
// còn phải thoát dấu nháy (nếu không có thể đóng href).
func TestEmailEscapesStructuralInjection(t *testing.T) {
	m := Message{
		Items: []Item{{
			Severity:  "high",
			Name:      `<script>alert(1)</script>`,
			Summary:   "a & b > c",
			DetailURL: `https://artex.local/x?a="onmouseover=alert(1)`,
		}},
	}
	html := htmlBody(m, 0)
	if strings.Contains(html, "<script>") {
		t.Fatalf("tiêu đề chưa thoát, có thể chèn thẻ: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("mong đợi entity đã thoát: %s", html)
	}
	if !strings.Contains(html, "a &amp; b &gt; c") {
		t.Fatalf("& và > chưa thoát: %s", html)
	}
	// Liên kết quay lại là public_base_url do quản trị viên cấu hình, độ tin cậy tương đối cao,
	// nhưng vị trí thuộc tính vẫn phải thoát dấu nháy — nếu không một địa chỉ mang dấu nháy sẽ
	// đóng href và chèn event handler.
	if strings.Contains(html, `onmouseover=alert(1)">`) {
		t.Fatalf("thuộc tính href chưa được thoát đúng: %s", html)
	}
	if !strings.Contains(html, "&quot;") {
		t.Fatalf("dấu nháy ở vị trí thuộc tính phải được thoát: %s", html)
	}
}

func mustExtractHeader(t *testing.T, msg, name string) string {
	t.Helper()
	for _, line := range strings.Split(msg, "\r\n") {
		if strings.HasPrefix(line, name+": ") {
			return strings.TrimPrefix(line, name+": ")
		}
	}
	t.Fatalf("không tìm thấy header %s", name)
	return ""
}

func TestChannelValidateReportsMissingFields(t *testing.T) {
	// Lỗi kiểm tra sẽ hiển thị trực tiếp cho người cấu hình, phải nói rõ thiếu gì, chứ không
	// nói chung chung "cấu hình không hợp lệ".
	cases := []struct {
		kind   string
		cfg    map[string]any
		substr string
	}{
		{KindDingTalk, map[string]any{}, "Webhook"},
		{KindFeishu, map[string]any{}, "Webhook"},
		{KindWeCom, map[string]any{}, "Webhook"},
		{KindTelegram, map[string]any{}, "Bot Token"},
		{KindTelegram, map[string]any{"bot_token": "t"}, "Chat ID"},
		{KindEmail, map[string]any{}, "SMTP"},
		{KindEmail, map[string]any{"host": "h"}, "cổng"},
		{KindEmail, map[string]any{"host": "h", "port": 587, "from": "f"}, "người nhận"},
	}
	for _, tc := range cases {
		ch, ok := Get(tc.kind)
		if !ok {
			t.Fatalf("kênh %s chưa đăng ký", tc.kind)
		}
		err := ch.Validate(tc.cfg)
		if err == nil {
			t.Errorf("cấu hình %s %v phải kiểm tra thất bại", tc.kind, tc.cfg)
			continue
		}
		if !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("thông báo lỗi của %s phải nhắc đến %q, nhận được %q", tc.kind, tc.substr, err.Error())
		}
	}
}

// TestEmailSMTPErrorClassification khóa chặt sự phân biệt ngữ nghĩa SMTP 4xx/5xx.
// Nếu 4xx cũng bị phán thành thất bại vĩnh viễn, một máy chủ email bật greylisting sẽ khiến mỗi
// tin đẩy đều rơi vào failed ngay sau lần thử đầu tiên — mà greylisting chính là tình huống thử
// lại tự động nên phát huy tác dụng nhất.
func TestEmailSMTPErrorClassification(t *testing.T) {
	cases := []struct {
		reply     string
		permanent bool
	}{
		{"450 4.7.1 Greylisting in action, please come back later", false},
		{"451 4.3.0 Temporary system failure", false},
		{"452 4.2.2 Mailbox full", false},
		{"550 5.1.1 User unknown", true},
		{"553 5.1.3 Bad address syntax", true},
		{"554 5.7.1 Relay access denied", true},
		// Khi không lấy được mã trả lời thì xử lý theo hướng "có thể thử lại": thà thử thêm một
		// lần, cũng không phán chết một lỗi có thể chỉ là tạm thời.
		{"unexpected EOF", false},
		{"", false},
	}
	for _, tc := range cases {
		err := smtpStageError("người nhận bị từ chối", errors.New(tc.reply))
		if got := IsPermanent(err); got != tc.permanent {
			t.Errorf("câu trả lời %q: mong đợi permanent=%v nhận được %v", tc.reply, tc.permanent, got)
		}
		// Dù phân loại thế nào, nguyên văn cũng phải giữ lại cho người dùng điều tra.
		if tc.reply != "" && !strings.Contains(err.Error(), tc.reply) {
			t.Errorf("nguyên văn của câu trả lời %q bị vứt bỏ: %v", tc.reply, err)
		}
	}
}

func TestRegistryCoversAllKinds(t *testing.T) {
	// Sáu kênh thiếu một cũng không được — thiếu một cái sẽ âm thầm biến mất khỏi dropdown UI.
	want := []string{KindDingTalk, KindEmail, KindFeishu, KindTelegram, KindWebhook, KindWeCom}
	got := Kinds()
	if len(got) != len(want) {
		t.Fatalf("số lượng kênh phải là %d, nhận được %d: %v", len(want), len(got), got)
	}
	for _, k := range want {
		if !ValidKind(k) {
			t.Errorf("kênh %s chưa đăng ký", k)
		}
		if ch, ok := Get(k); !ok || ch.Kind() != k {
			t.Errorf("Kind() của kênh %s không khớp với khóa đăng ký", k)
		}
	}
	if ValidKind("nope") {
		t.Error("loại chưa đăng ký không được qua kiểm tra")
	}
}

func TestPermanentErrorUnwrap(t *testing.T) {
	base := &permanentSentinel{}
	err := Permanent(base)
	if !IsPermanent(err) {
		t.Fatal("phải nhận diện là thất bại vĩnh viễn")
	}
	if !strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("thông báo lỗi phải truyền qua tầng dưới: %v", err)
	}
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) phải trả về nil")
	}
	if IsPermanent(nil) {
		t.Fatal("nil không phải thất bại vĩnh viễn")
	}
}

type permanentSentinel struct{}

func (*permanentSentinel) Error() string { return "sentinel" }
