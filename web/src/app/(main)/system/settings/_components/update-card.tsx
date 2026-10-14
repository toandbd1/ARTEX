"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { api, sseUrl } from "@/lib/api";
import type { UpdateCheck, UpdateProgress } from "@/lib/types";

/** Thời gian tối đa chờ phiên bản mới khởi động. Một lần nâng cấp trải qua ba lần khởi động tiến trình (staging → thay thế → phiên bản mới),
 *  mỗi lần chỉ tính bằng giây, ba phút đủ để bao phủ đĩa chậm và rebuild container Docker. */
const RESTART_TIMEOUT_MS = 180_000;

function humanSize(n?: number): string {
  if (!n || n <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function UpdateCard() {
  const [info, setInfo] = React.useState<UpdateCheck | null>(null);
  const [checking, setChecking] = React.useState(true);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  // Tách khỏi progress: sau khi staging xong tiến trình biến mất, SSE sẽ ngắt, lúc này phải chuyển sang poll /api/health.
  const [restarting, setRestarting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  // quiet đồng thời quyết định có bỏ qua cache backend không: kiểm tra tự động khi vào trang dùng cache (thanh trên vừa kiểm tra),
  // người dùng bấm "Kiểm tra cập nhật" thủ công thì bắt buộc truy vấn gốc, nếu không phiên bản vừa phát hành phải chờ cache hết hạn mới thấy.
  const check = React.useCallback((quiet = false) => {
    setChecking(true);
    api
      .checkUpdate(!quiet)
      .then((r) => {
        setInfo(r);
        if (!quiet) {
          if (r.error) toast.error("Kiểm tra cập nhật thất bại: " + r.error);
          else if (r.has_update) toast.success(`Phát hiện phiên bản mới ${r.latest}`);
          else if (r.comparable) toast.success("Hiện tại đã là phiên bản mới nhất");
        }
      })
      .catch((e) => {
        if (!quiet) toast.error("Kiểm tra cập nhật thất bại: " + (e as Error).message);
      })
      .finally(() => setChecking(false));
  }, []);

  React.useEffect(() => {
    check(true);
  }, [check]);

  // Poll /api/health cho đến khi số phiên bản thay đổi.
  //
  // Tiêu chí phải là "phiên bản đã đổi" chứ không phải "kết nối được": trong quá trình thay thế, phiên bản cũ sẽ
  // khởi động lại trong thoáng chốc một lần (lần đó chỉ để thay artex.new rồi thoát ngay), chỉ nhìn kết nối sẽ đánh giá nhầm thành công.
  const waitForNewVersion = React.useCallback(async (fromVersion: string) => {
    setRestarting(true);
    const deadline = Date.now() + RESTART_TIMEOUT_MS;
    while (Date.now() < deadline) {
      await sleep(2000);
      try {
        const r = await fetch("/api/health", { cache: "no-store" });
        if (r.ok) {
          const j = (await r.json()) as { version?: string };
          if (j.version && j.version !== fromVersion) {
            toast.success(`Đã cập nhật lên ${j.version}, đang tải lại trang`);
            await sleep(800);
            window.location.reload();
            return;
          }
        }
      } catch {
        // Không kết nối được trong cửa sổ khởi động lại là điều dự kiến, tiếp tục poll.
      }
    }
    setRestarting(false);
    toast.error("Hết thời gian chờ dịch vụ khởi động lại. Hãy kiểm tra nhật ký backend, hoặc xác nhận artex được khởi động qua start.sh / start.bat.");
  }, []);

  // Đăng ký tiến trình cập nhật. SSE không đi qua rewrite /api của Next (lớp đó sẽ buffer, sự kiện không đẩy ra được).
  const openStream = React.useCallback(
    (fromVersion: string) => {
      const es = new EventSource(sseUrl("/api/update/stream"));
      es.onmessage = (ev) => {
        let p: UpdateProgress;
        try {
          p = JSON.parse(ev.data) as UpdateProgress;
        } catch {
          return;
        }
        setProgress(p);
        if (p.phase === "failed") {
          es.close();
          setBusy(false);
          toast.error("Cập nhật thất bại: " + (p.error || p.message));
          return;
        }
        if (p.phase === "staged") {
          es.close();
          void waitForNewVersion(fromVersion);
        }
      };
      es.onerror = () => {
        // SSE chắc chắn ngắt khi tiến trình thoát. Nếu đã vào trạng thái chờ khởi động lại, đây là hiện tượng bình thường,
        // để poll /api/health tiếp tục đánh giá.
        es.close();
      };
      return es;
    },
    [waitForNewVersion],
  );

  const doUpdate = () => {
    if (!info) return;
    const from = info.current;
    const ok = window.confirm(
      `Xác nhận cập nhật lên ${info.latest}?\n\n` +
        "Cập nhật sẽ khởi động lại chương trình, các tác vụ đang chạy sẽ bị gián đoạn.\n" +
        (info.mode === "docker"
          ? "\nLưu ý: cập nhật trong container chỉ thay thế chương trình, không cập nhật chuỗi công cụ playwright / nmap trong image;" +
            "nếu phiên bản mới phụ thuộc công cụ mới, hãy dùng docker compose pull."
          : ""),
    );
    if (!ok) return;

    setBusy(true);
    setProgress({ phase: "downloading", percent: 0, message: "Đang chuẩn bị…" });
    const es = openStream(from);
    api.applyUpdate().catch((e) => {
      es.close();
      setBusy(false);
      setProgress(null);
      toast.error("Khởi động cập nhật thất bại: " + (e as Error).message);
    });
  };

  const doRollback = () => {
    if (!info) return;
    if (
      !window.confirm(
        "Khôi phục về phiên bản trước?\n\nChương trình sẽ khởi động lại, các tác vụ đang chạy sẽ bị gián đoạn.\nLưu ý: cấu trúc cơ sở dữ liệu sẽ không bị đảo ngược, phiên bản cũ có thể không nhận ra dữ liệu do phiên bản mới ghi.",
      )
    )
      return;
    const from = info.current;
    setBusy(true);
    api
      .rollbackUpdate()
      .then(() => {
        toast.success("Đã chuyển về phiên bản trước, đang khởi động lại…");
        void waitForNewVersion(from);
      })
      .catch((e) => {
        setBusy(false);
        toast.error("Khôi phục thất bại: " + (e as Error).message);
      });
  };

  const phase = progress?.phase;
  const showProgress = busy || restarting;
  // Chỉ giai đoạn tải xuống mới có phần trăm thực (tính theo Content-Length). Kiểm tra/giải nén/chờ khởi động lại đều là
  // giai đoạn không biết trước thời lượng, thanh tiến trình lấp đầy và thêm hiệu ứng pulse để biểu thị "đang bận nhưng chưa chắc còn bao lâu".
  const downloading = !restarting && phase === "downloading";
  const pct = downloading ? Math.max(progress?.percent ?? 0, 0) : 100;

  return (
    // Trang cài đặt là bố cục waterfall nhiều cột, thẻ tự chịu trách nhiệm khoảng cách hàng và cấm ngắt qua cột (xem chú thích trong page.tsx).
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DownloadIcon className="size-4" />
          Phiên bản & cập nhật
        </CardTitle>
        <CardDescription>Kiểm tra và cài đặt phiên bản mới từ GitHub. Cập nhật sẽ khởi động lại chương trình, các tác vụ đang chạy sẽ bị gián đoạn.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">Phiên bản hiện tại</span>
          <Badge variant="secondary" className="font-mono">
            {info?.current ?? "…"}
          </Badge>
          {info && (
            <>
              <Badge variant="outline" className="font-mono">
                {info.os}/{info.arch}
              </Badge>
              <Badge variant="outline">{info.mode === "docker" ? "Docker" : "Chương trình độc lập"}</Badge>
            </>
          )}
          {info?.latest && (
            <>
              <span className="text-muted-foreground">Phiên bản mới nhất</span>
              <Badge variant={info.has_update ? "default" : "secondary"} className="font-mono">
                {info.latest}
              </Badge>
            </>
          )}
          {info?.html_url && (
            <a
              href={info.html_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline"
            >
              Nhật ký thay đổi <ExternalLinkIcon className="size-3" />
            </a>
          )}
        </div>

        {info?.boot_notice && (
          <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.boot_notice}
          </p>
        )}

        {info?.error && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            Không kết nối được GitHub: {info.error}
            {"　"}Có thể cấu hình proxy toàn cục ở trên rồi thử lại.
          </p>
        )}

        {info && !info.comparable && info.reason && <p className="text-xs text-muted-foreground">{info.reason}</p>}

        {info?.has_update && info.asset_available === false && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.latest} không cung cấp gói phát hành cho {info.os}/{info.arch} (thiếu {info.asset}), không thể tự động cập nhật.
          </p>
        )}

        {info?.has_update && info.asset_available !== false && (
          <p className="text-xs text-muted-foreground">
            Sẽ tải xuống <span className="font-mono">{info.asset}</span>
            {info.size ? ` (${humanSize(info.size)})` : ""}, chỉ thay thế sau khi kiểm tra SHA256 và kiểm thử khói, thất bại sẽ tự động giữ phiên bản hiện tại.
          </p>
        )}

        {info && !info.has_update && info.comparable && !info.error && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <CheckCircle2Icon className="size-3.5 text-emerald-600" />
            Hiện tại đã là phiên bản mới nhất.
          </p>
        )}

        {info?.mode === "docker" && info.has_update && (
          <p className="text-xs text-muted-foreground">
            Cập nhật trong Docker chỉ thay thế chương trình, không cập nhật chuỗi công cụ playwright / nmap trong image, và sau khi
            <span className="font-mono"> docker compose up -d </span>
            rebuild container sẽ quay về phiên bản kèm theo image. Cần nâng cấp cả image hãy chạy
            <span className="font-mono"> docker compose pull artex &amp;&amp; docker compose up -d artex</span>.
          </p>
        )}

        {showProgress && (
          <div className="space-y-1.5">
            <Progress value={pct} className={downloading ? undefined : "animate-pulse"} />
            <p className="text-xs text-muted-foreground">
              {restarting ? "Đang khởi động lại và áp dụng phiên bản mới, vui lòng chờ (trang sẽ tự làm mới)…" : progress?.message}
            </p>
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => check(false)} disabled={checking || busy || restarting}>
            <RefreshCwIcon className={checking ? "size-4 animate-spin" : "size-4"} />
            Kiểm tra cập nhật
          </Button>
          <Button
            size="sm"
            onClick={doUpdate}
            disabled={busy || restarting || !info?.has_update || info?.asset_available === false}
          >
            <DownloadIcon className="size-4" />
            {info?.has_update ? `Cập nhật lên ${info.latest}` : "Cập nhật ngay"}
          </Button>
          {info?.has_backup && (
            <Button variant="ghost" size="sm" onClick={doRollback} disabled={busy || restarting}>
              <RotateCcwIcon className="size-4" />
              Khôi phục về phiên bản trước
            </Button>
          )}
        </div>

        <p className="text-xs text-muted-foreground">
          Cập nhật một chạm phụ thuộc script daemon để khởi động lại chương trình. Hãy khởi động ARTEX qua <span className="font-mono">start.sh</span> (trên Windows là
          <span className="font-mono"> start.bat</span>); khi chạy trực tiếp file artex, chương trình sẽ không tự được khởi động lại sau khi thoát.
        </p>
      </CardContent>
    </Card>
  );
}
