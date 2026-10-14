"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// Phím gửi/xuống dòng của ô nhập phiên chat. Thuần tùy chọn phía frontend: chỉ lưu vào
// localStorage, không ghi vào CSDL, không đồng bộ theo tài khoản, nên đổi trình duyệt
// phải thiết lập lại. Xem issue #39 — bản 0.3.2 đã đổi gửi bằng Ctrl+Enter thành gửi
// bằng Enter, ở đây trả lại phím cũ như một tùy chọn.
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

export const CHAT_SEND_MODE_OPTIONS: { value: ChatSendMode; label: string }[] = [
  { value: "enter", label: "Enter để gửi, Shift+Enter để xuống dòng" },
  { value: "ctrl-enter", label: "Ctrl+Enter để gửi, Enter để xuống dòng" },
];

function parseMode(raw: string | null): ChatSendMode {
  return raw === "ctrl-enter" || raw === "enter" ? raw : DEFAULT_CHAT_SEND_MODE;
}

// Tập subscriber trong cùng một tab. Sự kiện storage của localStorage chỉ kích hoạt ở
// các tab "khác"; sau khi trang này đổi trong cài đặt phải dùng emit để báo cho ô nhập
// của cùng trang, nếu không phải làm mới mới có hiệu lực.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// Trả về chuỗi literal, Object.is so sánh theo giá trị, không khiến useSyncExternalStore bị lặp vô hạn.
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// Server không có localStorage, render giá trị mặc định trước, sau khi hydrate getSnapshot sẽ sửa lại.
function getServerSnapshot(): ChatSendMode {
  return DEFAULT_CHAT_SEND_MODE;
}

export function useChatSendMode(): ChatSendMode {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function setChatSendMode(mode: ChatSendMode) {
  setLocalStorageValue(CHAT_SEND_MODE_KEY, mode);
  for (const listener of listeners) listener();
}

// shouldSubmitOnKey quyết định một lần bấm phím có nên gửi hay không.
// isComposing / keyCode 229 là khi bộ gõ (IME như tiếng Trung...) đang chọn từ, phải bỏ qua,
// nếu không Enter chọn từ sẽ bị gửi nhầm.
// Chế độ enter chỉ loại trừ Shift, giữ nguyên y hệt hành vi của 0.3.2 — người dùng
// không đổi cài đặt thì cảm giác gõ phím không đổi.
// Chế độ ctrl-enter chấp nhận cả Ctrl lẫn Cmd (macOS).
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
