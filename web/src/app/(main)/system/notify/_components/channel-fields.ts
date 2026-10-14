// Bảng trường của kênh và công cụ phân tích giá trị cấu hình.
//
// Tách khỏi trang vì phần này là **dữ liệu** chứ không phải view: nó mô tả mỗi loại
// kênh có những trường nào, dùng điều khiển nào, và chuyển đổi hai chiều giữa văn bản
// biểu mẫu với giá trị cấu hình (JSON).
// Đặt riêng một file để thêm kênh mới chỉ cần sửa ở đây, không phải đổi trang.
// Tên hiển thị và giới thiệu của loại kênh. Đặt ở frontend vì nó chỉ ảnh hưởng đến văn bản, backend không cần biết.
export const KIND_LABEL: Record<string, string> = {
  dingtalk: "DingTalk",
  feishu: "Feishu",
  wecom: "WeCom",
  webhook: "Webhook tổng quát",
  telegram: "Telegram",
  email: "Email",
};

// Định nghĩa trường cấu hình của từng kênh.
//
// Cố ý giữ một bảng trường ở frontend thay vì để backend phát schema: backend chỉ chịu
// trách nhiệm Validate (bắt buộc/định dạng), còn UI cần bố cục và loại điều khiển — hai
// bên quan tâm khác nhau.
// Điểm ghép nối duy nhất là secret_keys — những trường nào render thành ô mật khẩu do
// backend quyết định, vì chỉ bản thân implementation của kênh mới biết giá trị nào là
// thông tin xác thực (toàn bộ Webhook của WeCom là thông tin xác thực, còn của DingTalk
// chỉ là một secret trong đó). Thiếu một mục ở đây khi thêm kênh mới chỉ làm biểu mẫu
// trống, không lỗi ngầm (hasFields bên dưới sẽ gợi ý).
export type FieldKind = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export interface FieldDef {
  key: string;
  label: string;
  kind: FieldKind;
  placeholder?: string;
  help?: string;
  options?: { value: string; label: string }[];
}
export const CHANNEL_FIELDS: Record<string, FieldDef[]> = {
  dingtalk: [
    {
      key: "webhook",
      label: "Địa chỉ Webhook",
      kind: "text",
      placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    },
    {
      key: "secret",
      label: "Khóa ký",
      kind: "password",
      help: "Điền khi cài đặt bảo mật của bot chọn \"Ký\"; chọn \"Từ khóa tùy chỉnh\" hoặc chưa bật cài đặt bảo mật thì để trống",
    },
  ],
  feishu: [
    {
      key: "webhook",
      label: "Địa chỉ Webhook",
      kind: "text",
      placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    },
    { key: "secret", label: "Khóa kiểm tra chữ ký", kind: "password", help: "Điền khi bot bật \"Kiểm tra chữ ký\", nếu không thì để trống" },
  ],
  wecom: [
    {
      key: "webhook",
      label: "Địa chỉ Webhook",
      kind: "text",
      placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    },
  ],
  webhook: [
    { key: "url", label: "URL đích", kind: "text", placeholder: "https://your-endpoint.example.com/hook" },
    {
      key: "method",
      label: "Phương thức request",
      kind: "select",
      options: [
        { value: "POST", label: "POST (kèm body)" },
        { value: "PUT", label: "PUT (kèm body)" },
        { value: "PATCH", label: "PATCH (kèm body)" },
        { value: "GET", label: "GET (không kèm body)" },
      ],
    },
    { key: "headers", label: "Header tùy chỉnh", kind: "kv", help: "Mỗi dòng KEY=VALUE, ví dụ Authorization=Bearer xxx" },
    {
      key: "body_template",
      label: "Mẫu body request",
      kind: "textarea",
      help:
        "Để trống để dùng mẫu mặc định tích hợp. Biến: {{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}, " +
        "và .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel trong range .Items. " +
        "Chèn chuỗi hãy dùng {{json .Xxx}} thay vì {{.Xxx}}, nếu không dấu nháy trong tiêu đề sẽ phá vỡ JSON.",
    },
  ],
  telegram: [
    { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
    { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
    {
      key: "base_url",
      label: "Địa chỉ API",
      kind: "text",
      placeholder: "https://api.telegram.org",
      help: "Để trống dùng địa chỉ chính thức; điền khi tự dựng reverse proxy Bot API",
    },
  ],
  email: [
    { key: "host", label: "Máy chủ SMTP", kind: "text", placeholder: "smtp.example.com" },
    {
      key: "port",
      label: "Cổng",
      kind: "number",
      placeholder: "587",
      help: "587 dùng STARTTLS; 465 hãy bật \"TLS ngầm\"",
    },
    { key: "username", label: "Tài khoản", kind: "text" },
    { key: "password", label: "Mật khẩu / mã ủy quyền", kind: "password" },
    { key: "from", label: "Người gửi", kind: "text", placeholder: "artex@example.com" },
    { key: "to", label: "Người nhận", kind: "list", help: "Nhiều địa chỉ phân tách bằng dấu phẩy" },
    { key: "tls", label: "TLS ngầm", kind: "switch", help: "Bật cho cổng 465; giữ tắt cho 587 (sẽ tự động STARTTLS)" },
  ],
};

export const SEVERITY_OPTIONS = [
  { value: "", label: "Không giới hạn" },
  { value: "low", label: "Thấp trở lên" },
  { value: "medium", label: "Trung bình trở lên" },
  { value: "high", label: "Cao trở lên" },
  { value: "critical", label: "Chỉ nghiêm trọng" },
];

export type ChannelForm = {
  name: string;
  kind: string;
  mode: "realtime" | "digest";
  enabled: boolean;
  ratePerMin: string;
  config: Record<string, unknown>;
  minSeverity: string;
  includeText: string;
  excludeText: string;
  taskIDsText: string;
  assetIDsText: string;
  onStatusChange: boolean;
};

export const emptyForm = (kind: string): ChannelForm => ({
  name: "",
  kind,
  mode: "realtime",
  enabled: true,
  ratePerMin: "",
  config: {},
  minSeverity: "",
  includeText: "",
  excludeText: "",
  taskIDsText: "",
  assetIDsText: "",
  onStatusChange: false,
});

// parseKV phân tích vùng văn bản dạng "mỗi dòng KEY=VALUE".
export function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}
// parseIDs phân tích danh sách id phân tách bằng dấu phẩy/khoảng trắng.
export function parseIDs(text: string): number[] {
  return text
    .split(/[\s,，]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords phân tích danh sách từ khóa phân tách bằng dòng/dấu phẩy (tên loại lỗ hổng có thể chứa khoảng trắng, nên tách theo dòng hoặc dấu phẩy).
export function parseKeywords(text: string): string[] {
  return text
    .split(/[\n,，]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
