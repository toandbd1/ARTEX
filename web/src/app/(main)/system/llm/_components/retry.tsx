"use client";

// Component dùng chung cho cấu hình thử lại LLM: "số lần + khoảng cách" của từng tầng trong năm tầng thử lại.
//
// Năm tầng từ trong ra ngoài: kết nối (SDK) → phản hồi rỗng (SDK) → cửa sổ an toàn cùng provider → ngắt mạch vòng quay → chạy lại intent.
// Ba tầng đầu đi theo endpoint, nên mỗi cấu hình mô hình đều có thể ghi đè mặc định toàn cục; hai tầng sau ở cấp tiến trình, chỉ có một bản toàn cục.
//
// Mọi input đều theo cùng ngữ nghĩa "để trống = không cấu hình", khớp với db.RetryRule ở backend:
//   Số lần      trống/0 = dùng mặc định tích hợp | -1 = tắt tầng thử lại này | >0 = dùng số lần này
//   Khoảng cách trống/0 = dùng backoff lũy thừa vốn có của tầng này | >0 = đổi sang khoảng cố định tính bằng mili giây

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** Tầng thử lại này diễn ra ở đâu, do ai thực thi */
  where: string;
  /** Lỗi nào sẽ đi vào tầng này — cụ thể đến mã trạng thái, đừng bắt người dùng đoán */
  trigger: string;
  /** Những lỗi trông giống nhưng 【không】 đi vào tầng này, tránh việc điền xong không thấy tác dụng lại tưởng là bug */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** Giá trị mặc định khi để trống số lần, dùng cho placeholder */
  defAttempts: number;
  /** Chiến lược mặc định khi để trống khoảng cách, dùng cho placeholder */
  defInterval: string;
  /** Ý nghĩa khi điền -1 vào số lần */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "Thử lại kết nối",
    where: "SDK · Trước khi nhận được 200",
    trigger:
      "Không kết nối được hoặc chưa nhận được 200: reset kết nối / timeout đọc ghi / lỗi DNS và các lỗi tầng mạng khác, cùng HTTP 408, 429, 500, 502, 503, 504.",
    skips: "Các mã trạng thái còn lại (400 / 401 / 403 / 404 / 413 / 422 v.v.) đều là từ chối xác định, gửi lại cũng thất bại, ném thẳng ra ngoài.",
    desc: "Gửi lại nguyên vẹn cùng một request. Một khi stream đã bắt đầu (đã nhận 200), đứt giữa chừng không thuộc tầng này nữa.",
    attemptsLabel: "Số lần thử lại",
    defAttempts: 3,
    defInterval: "Lũy thừa 0.5s→1s→2s (tối đa 8s)",
    offHint: "-1 = không thử lại lần nào, thất bại ném ra ngay",
  },
  empty: {
    title: "Thử lại phản hồi rỗng",
    where: "SDK · Chỉ định dạng openai",
    trigger:
      "HTTP 200, finish_reason là stop bình thường, nhưng toàn bộ phản hồi không có một khối nội dung nào — khung rỗng của gateway, mất khung trường thinking, lỗi sampling đều có dạng này.",
    skips: "Không có nội dung do bị cắt bởi max_tokens không tính (cái đó phải tăng giới hạn output, gửi lại chỉ va lại một lần nữa).",
    desc: "Gửi lại toàn bộ prompt, nên khá tốn kém trên ngữ cảnh dài, không nên đặt số lần lớn.",
    attemptsLabel: "Số lần thử lại",
    defAttempts: 2,
    defInterval: "Lũy thừa 0.5s→1s→2s (tối đa 8s)",
    offHint: "-1 = phản hồi rỗng trả ra nguyên trạng",
  },
  stream: {
    title: "Thử lại cửa sổ an toàn cùng provider",
    where: "Dự án này · Trước khi bàn giao output",
    trigger:
      "Lỗi xảy ra sau khi stream đã thiết lập (đã nhận 200): kết nối đứt giữa chừng, provider overloaded, sự kiện lỗi 429 / 5xx trong stream — và chưa một token nào được bàn giao cho phía gọi.",
    skips:
      "Hết quota (402 / insufficient_quota, giao cho vòng quay đổi cấu hình), ngữ cảnh quá dài (413 / context length, giao cho nén), 400 / 401 / 403 / 404 / 422 từ chối xác định, đều không thử lại.",
    desc: "Phát lại cùng một request trên cùng một cấu hình. Vì chưa bàn giao output nào, phát lại sẽ không lặp output của mô hình hay thực thi công cụ.",
    attemptsLabel: "Số lần thử lại",
    defAttempts: 2,
    defInterval: "Lũy thừa 0.5s→1s (tối đa 4s)",
    offHint: "-1 = đứt stream giao thẳng cho tầng chạy lại intent bên ngoài",
  },
  breaker: {
    title: "Ngắt mạch vòng quay",
    where: "Dự án này · Cấp tiến trình, một bản toàn cục",
    trigger:
      "Ngắt mạch khi lỗi tạm thời (429, 5xx, lỗi mạng) tích lũy liên tiếp đến ngưỡng; các lỗi xác định như hết số dư (402), khóa mất hiệu lực (401 / 403), mô hình không tồn tại (404) không xét ngưỡng, ngắt mạch ngay lần đầu.",
    skips: "Thành công một lần là reset về 0, nên cấu hình thỉnh thoảng lỗi sẽ không bị tích dần đến ngắt mạch.",
    desc: "Sau khi ngắt mạch sẽ vào thời gian nguội, trong thời gian nguội vòng quay bỏ qua cấu hình này. Trạng thái được ghi vào DB, khởi động lại không mất.",
    attemptsLabel: "Thất bại liên tiếp mấy lần thì ngắt mạch",
    defAttempts: 3,
    defInterval: "Bậc thang 1min→5min→30min",
    offHint: "-1 = lỗi tạm thời không bao giờ ngắt mạch (lỗi xác định vẫn ngắt mạch)",
  },
  intent: {
    title: "Chạy lại intent",
    where: "Dự án này · Cấp tiến trình, một bản toàn cục",
    trigger:
      "Các tầng trước đều không cứu được: worker kết thúc bằng model_error — đã dùng hết các lần thử lại trong, hoặc stream đã bắt đầu bàn giao output rồi mới đứt (lúc đó phát lại không an toàn, chỉ có thể chạy lại cả chuỗi).",
    skips: "Hết quota đã được vòng quay đổi cấu hình xử lý, không chạy lại ở đây; khi tác vụ bị tạm dừng / kết thúc / vào giai đoạn kết thúc thì nhường ngay, không chiếm thời gian backoff.",
    desc: "Chạy lại toàn bộ intent từ đầu. Đây là tầng ngoài cùng, một lần chạy lại nghĩa là số lần của các tầng bên trong sẽ được nhân thêm một lần nữa.",
    attemptsLabel: "Số lần chạy lại",
    defAttempts: 2,
    defInterval: "Cố định 3s",
    offHint: "-1 = không chạy lại, intent đó bị đánh dấu blocked trực tiếp",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** Dạng dễ đọc của mili giây, chỉ dùng để hiển thị lại cạnh ô nhập, khỏi phải đếm số 0. */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** Input số có kiểm soát: chuỗi rỗng ↔ 0, trạng thái trung gian ("-", "1e") giữ nguyên ở local, không làm phiền component cha. */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // Theo kịp khi component cha đổi cả bộ giá trị (đọc được policy, chuyển cấu hình); khi tự gõ phím sẽ không vào đây,
  // vì lúc đó value đã bằng kết quả parse của văn bản local.
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** Hai núm điều chỉnh của một tầng thử lại. idPrefix dùng để giữ htmlFor của label khi xuất hiện nhiều lần trên cùng một trang. */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = bản gọn trong drawer cấu hình: bỏ phần giải thích mở rộng, chỉ giữ câu "lỗi nào sẽ đi vào tầng này" */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* Lỗi nào sẽ đi vào tầng này, cụ thể đến mã trạng thái — điền núm điều chỉnh mà không thấy hiệu quả, nhiều khả năng là lỗi vốn không rơi vào tầng này. */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">Kích hoạt</span>: {meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">Không qua tầng này</span>: {meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`Mặc định ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            Khoảng cách ms
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="Backoff mặc định"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `Cố định ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">Để trống = dùng mặc định; {meta.offHint}.</p>}
    </div>
  );
}

/** Ghi đè ba tầng trong drawer cấu hình mô hình (ba tầng đi theo endpoint). */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">Ghi đè thử lại</Label>
        <p className="text-muted-foreground text-xs">
          Chỉ có hiệu lực với cấu hình này, ghi đè mặc định toàn cục trong "Thử lại & backoff". Mỗi ô để trống = theo toàn cục;
          số lần điền -1 = tắt tầng thử lại này; điền khoảng cách thì dùng khoảng cố định thay backoff lũy thừa.
          Ngắt mạch và chạy lại intent ở cấp tiến trình, chỉ có thể chỉnh ở trang toàn cục.
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** Tab "Thử lại & backoff": giá trị mặc định toàn cục của năm tầng. */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`Đọc chính sách thử lại thất bại: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // Backend sẽ kẹp giá trị vượt biên về khoảng và trả lại, dùng trực tiếp giá trị trả về để làm mới, thấy gì là lưu đó.
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("Đã lưu, có hiệu lực ngay (lượt gọi đang chạy vẫn dùng tham số cũ)");
    } catch (e) {
      toast.error(`Lưu thất bại: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> Đang đọc chính sách thử lại…
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        Một lỗi của lượt gọi mô hình sẽ lần lượt đi qua năm tầng thử lại, từ trong ra ngoài:
        <span className="text-foreground"> kết nối → phản hồi rỗng → cửa sổ an toàn cùng provider → ngắt mạch vòng quay → chạy lại intent</span>
        . Tầng trong dùng hết mới đến tầng ngoài, nên số lần được
        <span className="text-foreground">nhân</span>
        với nhau — nếu kéo mọi tầng lên tối đa, một lần giật mạng có thể đốt hàng chục request.
        Để trống tất cả chính là giá trị mặc định hiện tại, hoàn toàn giống hành vi khi chưa có trang này. Ba tầng đầu có thể ghi đè riêng trong từng cấu hình mô hình.
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          Lưu
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          Khôi phục tất cả về mặc định
        </Button>
      </div>
    </div>
  );
}
