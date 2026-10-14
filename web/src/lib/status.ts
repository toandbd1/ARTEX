// Centralised status → color/label semantics, reused across the whole app.
// Spec §8.3: ý đồ / độ bao phủ / tác vụ / mức độ nghiêm trọng each have a consistent color set.

export type Tone = "neutral" | "blue" | "green" | "amber" | "red" | "rose" | "violet" | "slate";

export const toneClasses: Record<Tone, string> = {
  neutral: "bg-muted text-muted-foreground border-transparent",
  blue: "bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-500/20",
  green: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20",
  amber: "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20",
  red: "bg-red-500/15 text-red-600 dark:text-red-400 border-red-500/20",
  // rose dùng cho "nghiêm trọng" —— đặc, điểm nhấn cao, nhìn rõ ràng nổi bật hơn viền đỏ mềm của "cao".
  rose: "bg-rose-600 text-white border-rose-600 dark:bg-rose-600 dark:text-white",
  violet: "bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/20",
  slate: "bg-slate-500/15 text-slate-600 dark:text-slate-400 border-slate-500/20",
};

export const toneDot: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  blue: "bg-blue-500",
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  rose: "bg-white",
  violet: "bg-violet-500",
  slate: "bg-slate-500",
};

interface StatusMeta {
  label: string;
  tone: Tone;
}

const intent: Record<string, StatusMeta> = {
  open: { label: "chờ nhận", tone: "slate" },
  running: { label: "đang thực thi", tone: "blue" },
  paused: { label: "đã tạm dừng", tone: "amber" },
  done: { label: "đã hoàn thành", tone: "green" },
  // blocked = lỗi mô hình/API/mạng, đã hết lượt thử lại, ý đồ này về cơ bản chưa thật sự thăm dò được (không phải do mục tiêu chặn).
  blocked: { label: "lỗi thực thi", tone: "red" },
  // exhausted = đạt ngân sách số bước/thời gian nên bị cắt giữa chừng, chỉ ghi lại một phần kết quả (không phải hướng đã thăm dò hết).
  exhausted: { label: "hết ngân sách", tone: "violet" },
  // stopped = trạng thái xóa mềm lịch sử (giữ lại, dữ liệu lịch sử).
  stopped: { label: "đã dừng", tone: "slate" },
  // deleted = người dùng đã xóa giả ý đồ này (giữ lại node và huyết thống, lý do xóa xem ở trường delete_reason).
  deleted: { label: "đã xóa", tone: "slate" },
};

const task: Record<string, StatusMeta> = {
  created: { label: "đã tạo", tone: "slate" },
  queued: { label: "đang xếp hàng", tone: "amber" },
  running: { label: "đang chạy", tone: "blue" },
  paused: { label: "đã tạm dừng", tone: "amber" },
  done: { label: "đã hoàn thành", tone: "green" },
  failed: { label: "thất bại", tone: "red" },
  timeout: { label: "đã quá thời gian", tone: "amber" },
};

const severity: Record<string, StatusMeta> = {
  critical: { label: "nghiêm trọng", tone: "rose" },
  high: { label: "cao", tone: "red" },
  medium: { label: "trung bình", tone: "amber" },
  low: { label: "thấp", tone: "slate" },
};

const finding: Record<string, StatusMeta> = {
  pending: { label: "chờ xử lý", tone: "amber" },
  in_progress: { label: "đang xử lý", tone: "blue" },
  confirmed: { label: "đã xác nhận", tone: "red" },
  resolved: { label: "đã xử lý", tone: "green" },
  fixed: { label: "đã sửa", tone: "green" },
  false_positive: { label: "dương tính giả", tone: "slate" },
  ignored: { label: "bỏ qua", tone: "neutral" },
  duplicate: { label: "trùng lặp", tone: "neutral" },
  risk_accepted: { label: "chấp nhận rủi ro", tone: "violet" },
};

const engine: Record<string, StatusMeta> = {
  exploring: { label: "đang thăm dò", tone: "blue" },
  paused: { label: "đã tạm dừng", tone: "amber" },
  stalled: { label: "đình trệ", tone: "red" },
  idle: { label: "nhàn rỗi", tone: "neutral" },
};

const goal: Record<string, StatusMeta> = {
  open: { label: "đang tiến hành", tone: "blue" },
  met: { label: "đã đạt", tone: "green" },
  abandoned: { label: "đã bỏ", tone: "slate" },
};

const audit: Record<string, StatusMeta> = {
  allow: { label: "cho qua", tone: "green" },
  block: { label: "chặn", tone: "red" },
};

const node: Record<string, StatusMeta> = {
  observed: { label: "quan sát", tone: "slate" },
  confirmed: { label: "xác nhận", tone: "green" },
  tombstoned: { label: "đã loại bỏ", tone: "neutral" },
};

// Trạng thái giao hàng thông báo đẩy. sending dùng blue chứ không phải amber: nó không phải "có vấn đề",
// mà là "đã được nhận, đang gửi", cần phân biệt với ngữ nghĩa chờ đợi của pending.
const delivery: Record<string, StatusMeta> = {
  pending: { label: "chờ gửi", tone: "amber" },
  sending: { label: "đang gửi", tone: "blue" },
  sent: { label: "đã gửi đến", tone: "green" },
  failed: { label: "thất bại", tone: "red" },
  skipped: { label: "đã bỏ qua", tone: "neutral" },
};

const maps = {
  intent,
  task,
  severity,
  finding,
  engine,
  goal,
  audit,
  node,
  delivery,
} as const;

export type StatusDomain = keyof typeof maps;

export function statusMeta(domain: StatusDomain, key: string): StatusMeta {
  return maps[domain][key] ?? { label: key, tone: "neutral" };
}
