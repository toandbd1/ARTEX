"use client";

import * as React from "react";

import { RotateCcwIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { FindingRetest } from "@/lib/types";

interface FindingRetestDialogProps {
  findingId: string;
  findingName?: string;
  onClose: () => void;
  onStarted?: (retest: FindingRetest) => void;
}

// Chỉ mount khi mở, xóa phần mô tả sau khi đóng; danh sách và chi tiết dùng chung khóa gửi và
// xử lý lỗi, sau khi khởi động vẫn ở lại trang hiện tại.
export function FindingRetestDialog({ findingId, findingName, onClose, onStarted }: FindingRetestDialogProps) {
  const notesId = React.useId();
  const [notes, setNotes] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);
  const submitLock = React.useRef(false);

  async function start() {
    if (submitLock.current) return;
    submitLock.current = true;
    setSubmitting(true);
    try {
      const result = await api.startFindingRetest(findingId, notes.trim());
      onStarted?.(result.retest);
      onClose();
      toast.success(result.created ? "Đã khởi động kiểm thử lại, nhấn vào «Đang kiểm thử lại» để xem phiên" : "Lỗ hổng này đang được kiểm thử lại, có thể xem phiên hiện có");
    } catch (e) {
      toast.error(`Khởi động kiểm thử lại thất bại: ${(e as Error).message}`);
    } finally {
      submitLock.current = false;
      setSubmitting(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && !submitLock.current && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Kiểm thử lại lỗ hổng #{findingId}</DialogTitle>
          <DialogDescription className="break-words">
            {findingName ? <span className="mb-2 block">{findingName}</span> : null}
            Agent kiểm thử lại
            sẽ đọc bằng chứng gốc và ràng buộc kiểm thử, thực hiện xác minh có mục tiêu trong một phiên độc lập. Sau khi kiểm thử lại hoàn tất thành công và xác nhận đã vá, trạng thái lỗ hổng tự động đổi thành "Đã vá", các kết luận khác giữ nguyên trạng thái gốc.
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <Field data-disabled={submitting}>
            <FieldLabel htmlFor={notesId}>Ghi chú bổ sung (tùy chọn)</FieldLabel>
            <Textarea
              id={notesId}
              value={notes}
              maxLength={4000}
              rows={4}
              disabled={submitting}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="Ví dụ: dùng tài khoản kiểm thử gốc để xác minh API gốc; phiên bản đã vá là v2."
            />
            <FieldDescription>Có thể bổ sung phiên bản đã vá, điều kiện kiểm thử hoặc giới hạn của lần này.</FieldDescription>
          </Field>
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" disabled={submitting} onClick={onClose}>
            Hủy
          </Button>
          <Button disabled={submitting} onClick={() => void start()}>
            {submitting ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}
            {submitting ? "Đang tạo…" : "Bắt đầu kiểm thử lại"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
