"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";

/**
 * Thông báo "có phiên bản mới" trên thanh trên cùng: truy vấn một lần khi tải trang, có bản cập nhật thì hiển thị cạnh số phiên bản,
 * nhấn vào để tới thẳng thẻ "Phiên bản và cập nhật" trong trang cấu hình hệ thống.
 *
 * Backend cache kết quả truy vấn GitHub trong 30 phút, nên việc truy vấn mỗi lần mount ở đây là an toàn
 * ——GitHub API chưa xác thực chỉ cho 60 lần/giờ/IP; nếu không có lớp cache đó, mở nhiều tab
 * sẽ dễ dàng cạn hạn mức, đến khi thật sự muốn cập nhật lại không truy vấn được.
 *
 * Mọi lỗi truy vấn đều im lặng: thanh trên cùng không phải chỗ báo lỗi; người dùng vào trang cài đặt bấm "Kiểm tra cập nhật" sẽ thấy nguyên nhân.
 */
export function UpdateBadge() {
  const [latest, setLatest] = React.useState("");

  React.useEffect(() => {
    let alive = true;
    api
      .checkUpdate()
      .then((r) => {
        // has_update đã bao gồm điều kiện "số phiên bản so sánh được", bản build dev sẽ không hiện thông báo này.
        if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
      })
      .catch(() => {
        // Im lặng: mất mạng / GitHub giới hạn tần suất đều không nên bật lỗi ở thanh trên cùng.
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      title={`Phát hiện phiên bản mới ${latest}, nhấn để cập nhật`}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* Chấm nhấp nhô: thanh trên cùng có nhiều phần tử, chữ thuần dễ bị bỏ qua, hiệu ứng giúp dễ nhận thấy ngay. */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">Phiên bản mới {latest}</span>
      <span className="sm:hidden">Phiên bản mới</span>
    </Link>
  );
}
