"use client";

import * as React from "react";

import { PlusIcon, Trash2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import type { AssetInterceptKind, AssetInterceptRuleInput } from "@/lib/types";

// Dùng NativeSelect (<select> nguyên bản) thay vì shadcn Select: trình chỉnh sửa này được dùng
// trong ngăn kéo Sheet, dropdown của shadcn Select được portal ra body, khi click ra ngoài sẽ
// kích hoạt nhầm "click ra ngoài để đóng" của ngăn kéo; dropdown nguyên bản không gặp vấn đề này.
export const ASSET_INTERCEPT_KIND_OPTIONS: {
  value: AssetInterceptKind;
  label: string;
  placeholder: string;
}[] = [
  { value: "exact_domain", label: "Tên miền (chính xác)", placeholder: "example.gov.cn" },
  { value: "exact_ip", label: "IP (chính xác)", placeholder: "203.0.113.10" },
  { value: "exact_url", label: "URL (chính xác)", placeholder: "https://example.com/login" },
  { value: "fuzzy_domain", label: "Tên miền (mờ)", placeholder: ".gov.cn" },
  { value: "fuzzy_ip", label: "IP (mờ)", placeholder: "203.0.113." },
  { value: "fuzzy_url", label: "URL (mờ)", placeholder: "/admin" },
  { value: "cidr", label: "Dải CIDR", placeholder: "192.168.0.0/16" },
];

// AssetInterceptRulesEditor là vùng chỉnh sửa nhiều dòng có kiểm soát cho "quy tắc chặn/cho phép"
// (chặn block/cho phép allow + loại + nội dung khớp + ghi chú), không tự lưu trữ — component cha
// quyết định khi nào gửi.
export function AssetInterceptRulesEditor({
  value,
  onChange,
}: {
  value: AssetInterceptRuleInput[];
  onChange: (v: AssetInterceptRuleInput[]) => void;
}) {
  function update(i: number, patch: Partial<AssetInterceptRuleInput>) {
    onChange(value.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  }
  function remove(i: number) {
    onChange(value.filter((_, idx) => idx !== i));
  }
  function add() {
    onChange([...value, { action: "block", kind: "fuzzy_domain", pattern: "", note: "", enabled: true }]);
  }
  return (
    <div className="grid gap-2">
      {value.map((r, i) => {
        const ph = ASSET_INTERCEPT_KIND_OPTIONS.find((o) => o.value === r.kind)?.placeholder ?? "";
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: dòng không có id ổn định, điều khiển theo chỉ số là đủ
          <div key={i} className="flex items-center gap-2">
            <NativeSelect
              size="sm"
              className="w-[84px] shrink-0"
              value={r.action}
              onChange={(e) => update(i, { action: e.target.value as "block" | "allow" })}
            >
              <NativeSelectOption value="block">Chặn</NativeSelectOption>
              <NativeSelectOption value="allow">Cho phép</NativeSelectOption>
            </NativeSelect>
            <NativeSelect
              size="sm"
              className="w-[120px] shrink-0"
              value={r.kind}
              onChange={(e) => update(i, { kind: e.target.value as AssetInterceptKind })}
            >
              {ASSET_INTERCEPT_KIND_OPTIONS.map((o) => (
                <NativeSelectOption key={o.value} value={o.value}>
                  {o.label}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <Input
              className="flex-1"
              placeholder={ph}
              value={r.pattern}
              onChange={(e) => update(i, { pattern: e.target.value })}
            />
            <Input
              className="w-[120px] shrink-0"
              placeholder="Ghi chú (tùy chọn)"
              value={r.note}
              onChange={(e) => update(i, { note: e.target.value })}
            />
            <Button
              type="button"
              size="icon"
              variant="ghost"
              className="text-destructive hover:text-destructive size-8 shrink-0"
              onClick={() => remove(i)}
            >
              <Trash2Icon className="size-4" />
            </Button>
          </div>
        );
      })}
      <Button type="button" size="sm" variant="outline" className="w-fit" onClick={add}>
        <PlusIcon className="size-4" /> Thêm một dòng
      </Button>
    </div>
  );
}
