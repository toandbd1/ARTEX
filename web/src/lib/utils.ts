import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// Hàm hỗ trợ quyết định đóng của onInteractOutside của ngăn kéo/hộp thoại (Sheet/Dialog).
//
// Bối cảnh: các popover Radix trong ngăn kéo (Select dropdown, DropdownMenu, Popover v.v.)
// sẽ portal ra ngoài ngăn kéo. Khi popover đang mở mà click vào lớp phủ/ngoài ngăn kéo để
// thu nó lại, pointerdown này sẽ được hai DismissableLayer của Select và Sheet xử lý đồng
// thời; Select đóng trước và là sự kiện discrete, React sẽ flush đồng bộ, nên khi tới
// handler của Sheet thì data-state của popover đã chuyển thành closed từ lâu —— việc kiểm
// tra popover có đang mở "ngay lúc đó" vốn không đáng tin (đã xác minh thực tế).
//
// Cách đúng: listener pointerdown của Radix chạy ở giai đoạn bubble; ta ở giai đoạn capture
// (sớm hơn nó) ghi lại trước "lúc này có popover nào đang mở không", onInteractOutside sẽ
// đọc giá trị đã ghi để quyết định có cho đóng hay không.
function isRadixOverlayOpenNow(): boolean {
  if (typeof document === "undefined") return false;
  return !!document.querySelector(
    [
      "[data-slot='select-trigger'][data-state='open']",
      "[data-slot='select-content'][data-state='open']",
      "[role='listbox'][data-state='open']",
      "[data-radix-popper-content-wrapper]",
      "[aria-expanded='true'][data-state='open']",
    ].join(","),
  );
}

let overlayOpenAtLastPointerDown = false;
if (typeof document !== "undefined") {
  document.addEventListener(
    "pointerdown",
    () => {
      overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
    },
    true, // capture: ghi lại trước handler pointerdown ở giai đoạn bubble của Radix
  );
}

// radixOverlayWasOpenAtPointerDown trả về "tại thời điểm pointerdown gần nhất xảy ra, có
// popover Radix nào đang mở không". Ngăn kéo/hộp thoại dựa vào đó: khi popover đang mở mà
// click lớp phủ → chỉ thu popover, không đóng chính nó.
export function radixOverlayWasOpenAtPointerDown(): boolean {
  return overlayOpenAtLastPointerDown;
}

// copyText ghi văn bản vào clipboard, trả về có thành công hay không.
// Bối cảnh: navigator.clipboard chỉ khả dụng trong ngữ cảnh an toàn (HTTPS / localhost);
// khi truy cập qua IP + HTTP nó là undefined, lúc đó hạ cấp xuống execCommand("copy").
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // tiếp tục đi theo phương án hạ cấp
    }
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    textarea.style.top = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}

export const getInitials = (str: string): string => {
  if (typeof str !== "string" || !str.trim()) return "?";

  return (
    str
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .map((word) => word[0])
      .join("")
      .toUpperCase() || "?"
  );
};

export function formatCurrency(
  amount: number,
  opts?: {
    currency?: string;
    locale?: string;
    minimumFractionDigits?: number;
    maximumFractionDigits?: number;
    noDecimals?: boolean;
  },
) {
  const { currency = "USD", locale = "en-US", minimumFractionDigits, maximumFractionDigits, noDecimals } = opts ?? {};

  const formatOptions: Intl.NumberFormatOptions = {
    style: "currency",
    currency,
    minimumFractionDigits: noDecimals ? 0 : minimumFractionDigits,
    maximumFractionDigits: noDecimals ? 0 : maximumFractionDigits,
  };

  return new Intl.NumberFormat(locale, formatOptions).format(amount);
}
