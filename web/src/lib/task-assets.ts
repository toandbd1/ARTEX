import type { NewAssetType } from "@/lib/types";

const ASSET_TYPE_LABELS: Record<NewAssetType, string> = {
  app: "Ứng dụng",
  endpoint: "Endpoint",
  ip: "IP",
  root_domain: "Tên miền gốc",
  service: "Dịch vụ",
  subdomain: "Tên miền con",
};

const TASK_ASSET_SOURCE_LABELS: Record<string, string> = {
  agent: "Agent phát hiện",
  anchor: "Neo bảng đen",
  api: "API tài sản",
  company: "Liên kết doanh nghiệp",
  legacy: "Liên kết lịch sử",
  manual: "Thêm thủ công",
  system: "Liên kết hệ thống",
  task: "Khởi tạo tác vụ",
};

export function taskAssetTypeLabel(type: NewAssetType): string {
  return ASSET_TYPE_LABELS[type];
}

export function taskAssetSourceLabel(source: string): string {
  return TASK_ASSET_SOURCE_LABELS[source] ?? source;
}
