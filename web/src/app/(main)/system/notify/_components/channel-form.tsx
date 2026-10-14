"use client";

import { CheckIcon } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { NotificationFilter } from "@/lib/types";

// asText / inputType là các hàm phụ trợ lấy giá trị trong file này (liên quan chặt đến việc render điều khiển), không đặt trong channel-fields.
import { type FieldDef, type FieldKind, SEVERITY_OPTIONS } from "./channel-fields";

// asText hiển thị giá trị cấu hình bất kỳ thành chuỗi dùng được trong ô nhập.
// config đến từ JSON, giá trị có thể là string / number / boolean / array / null,
// ở đây chỉ quan tâm "có nhét được vào ô văn bản không", việc serialize cụ thể do buildConfig đảm nhiệm.
function asText(v: unknown): string {
  if (typeof v === "string") return v;
  if (v === null || v === undefined) return "";
  return String(v);
}

// inputType ánh xạ loại trường sang thuộc tính type của input.
function inputType(kind: FieldKind): "text" | "password" | "number" {
  if (kind === "password") return "password";
  if (kind === "number") return "number";
  return "text";
}

// ConfigField render điều khiển tương ứng theo định nghĩa trường.
//
// Xử lý trường che giấu là điểm cần lưu ý duy nhất ở đây: ô nhập **không hiển thị**
// giá trị che giấu, chỉ hiển thị một dòng gợi ý "đã lưu". Như vậy giao diện chỉ có một
// quy tắc — ô có chữ là do người dùng nhập, ô trống là giá trị rỗng. Nếu nhét
// "__masked__:…abc123" vào ô nhập, người dùng sẽ tưởng đó là văn bản placeholder
// cần tự xóa, càng dễ xóa nhầm thông tin xác thực.
export function ConfigField({
  def,
  value,
  isSecret,
  onChange,
}: {
  def: FieldDef;
  value: unknown;
  isSecret: boolean;
  onChange: (v: unknown) => void;
}) {
  const id = `n-cfg-${def.key}`;
  const raw = asText(value);
  // Giá trị che giấu do backend trả về: dạng "__masked__:…abc123", phần đuôi là đoạn nhận diện được của giá trị gốc.
  const masked = isSecret && raw.startsWith("__masked__");
  const maskedTail = masked ? (raw.split("…")[1] ?? "") : "";

  if (def.kind === "switch") {
    return (
      <div className="flex items-center gap-2 text-sm">
        <Switch checked={value === true} onCheckedChange={onChange} aria-label={def.label} />
        {def.label}
        {def.help && <span className="text-muted-foreground">({def.help})</span>}
      </div>
    );
  }

  if (def.kind === "select") {
    return (
      <div className="grid gap-2">
        <Label>{def.label}</Label>
        <Select value={raw || def.options?.[0]?.value} onValueChange={onChange}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(def.options ?? []).map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    );
  }

  // Điều khiển được phân phối theo loại trường. Dùng chuỗi if thay vì toán tử ba ngôi
  // lồng nhau, vì ở đây cần phân biệt bốn loại điều khiển, ba tầng ba ngôi đọc đến đó
  // đã phải dừng lại đếm ngoặc.
  function control() {
    if (def.kind === "textarea" || def.kind === "kv") {
      return (
        <Textarea
          id={id}
          className="font-mono"
          placeholder={def.placeholder}
          value={masked ? "" : raw}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    }
    if (def.kind === "list") {
      return (
        <Input
          id={id}
          value={Array.isArray(value) ? (value as string[]).join(", ") : raw}
          onChange={(e) => onChange(e.target.value)}
          placeholder={def.placeholder}
        />
      );
    }
    return (
      <Input
        id={id}
        className={def.kind === "text" ? "font-mono" : ""}
        type={inputType(def.kind)}
        placeholder={def.placeholder}
        value={masked ? "" : raw}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  const hint = masked ? (
    <p className="text-muted-foreground flex items-center gap-1 text-xs">
      <CheckIcon className="size-3" />
      Đã lưu{maskedTail ? ` (đuôi ${maskedTail})` : ""} · Nhập giá trị mới để ghi đè, xóa trắng để xóa mục này
    </p>
  ) : (
    def.help && <p className="text-muted-foreground text-xs">{def.help}</p>
  );

  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{def.label}</Label>
      {control()}
      {hint}
    </div>
  );
}

// FilterSummary tóm tắt điều kiện lọc thành một dòng, để không cần mở rộng thẻ cũng biết kênh này đẩy thông báo gì.
export function FilterSummary({ filter }: { filter: NotificationFilter }) {
  const parts: string[] = [];
  if (filter.min_severity) {
    parts.push(SEVERITY_OPTIONS.find((o) => o.value === filter.min_severity)?.label ?? filter.min_severity);
  }
  if (filter.vulnclass_include?.length) parts.push(`Loại chứa ${filter.vulnclass_include.length} từ`);
  if (filter.vulnclass_exclude?.length) parts.push(`Loại trừ ${filter.vulnclass_exclude.length} từ`);
  if (filter.task_ids?.length) parts.push(`${filter.task_ids.length} tác vụ`);
  if (filter.asset_ids?.length) parts.push(`${filter.asset_ids.length} tài sản`);
  if (filter.on_status_change) parts.push("Kèm thay đổi trạng thái");
  if (parts.length === 0) {
    return <p className="text-muted-foreground text-sm">Tất cả lỗ hổng</p>;
  }
  return <p className="text-muted-foreground text-sm">{parts.join(" · ")}</p>;
}
