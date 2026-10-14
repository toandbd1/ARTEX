"use client";

import * as React from "react";

import { BellIcon, PlusIcon, SendIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { NotificationChannel, NotificationFilter, NotificationMeta } from "@/lib/types";

import {
  CHANNEL_FIELDS,
  type ChannelForm,
  emptyForm,
  KIND_LABEL,
  parseIDs,
  parseKeywords,
  parseKV,
  SEVERITY_OPTIONS,
} from "./_components/channel-fields";
import { ConfigField, FilterSummary } from "./_components/channel-form";
import { DeliveryList } from "./_components/delivery-list";
import { formatBacklog, StatTile } from "./_components/stat-tile";

// Trang này chỉ chịu trách nhiệm điều phối: tải dữ liệu, duy trì trạng thái biểu mẫu, gọi API.
// Định nghĩa trường và phân tích ở _components/channel-fields.ts, điều khiển và tóm tắt lọc ở
// _components/channel-form.tsx, bản ghi gửi ở _components/delivery-list.tsx — tách ra vì
// mỗi phần có thể đọc hiểu độc lập, còn nhồi chung một file thì trang này gần 1100 dòng.
export default function NotifyPage() {
  const [meta, setMeta] = React.useState<NotificationMeta | null>(null);
  const [channels, setChannels] = React.useState<NotificationChannel[]>([]);
  const [tab, setTab] = React.useState<"channels" | "deliveries">("channels");

  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<NotificationChannel | null>(null);
  const [form, setForm] = React.useState<ChannelForm>(emptyForm("dingtalk"));
  const [saving, setSaving] = React.useState(false);
  const [testing, setTesting] = React.useState(false);

  const [globalSaving, setGlobalSaving] = React.useState(false);
  const [baseURL, setBaseURL] = React.useState("");
  const [digestMin, setDigestMin] = React.useState("");

  const load = React.useCallback(() => {
    api
      .notifyMeta()
      .then((m) => {
        setMeta(m);
        setBaseURL(m.public_base_url);
        setDigestMin(m.digest_interval_min);
      })
      .catch((e) => toast.error("Đọc cấu hình đẩy thất bại: " + (e as Error).message));
    // Thất bại khi tải danh sách kênh phải báo ra: thất bại ngầm sẽ hiển thị thành "không có kênh nào",
    // người dùng sẽ tưởng mất cấu hình, đáng lo hơn báo lỗi trực tiếp.
    api
      .notifyChannels()
      .then(setChannels)
      .catch((e) => toast.error("Đọc danh sách kênh thất bại: " + (e as Error).message));
  }, []);
  React.useEffect(() => {
    load();
  }, [load]);

  function setF(patch: Partial<ChannelForm>) {
    setForm((f) => ({ ...f, ...patch }));
  }
  function setCfg(key: string, value: unknown) {
    setForm((f) => ({ ...f, config: { ...f.config, [key]: value } }));
  }

  function openAdd() {
    setEditing(null);
    setForm(emptyForm(meta?.kinds[0]?.kind ?? "dingtalk"));
    setOpen(true);
  }

  function openEdit(ch: NotificationChannel) {
    setEditing(ch);
    // filter ở backend là struct Go, luôn serialize thành object (không phải null), nên không cần fallback.
    const f = ch.filter;
    setForm({
      name: ch.name,
      kind: ch.kind,
      mode: ch.mode,
      enabled: ch.enabled,
      ratePerMin: String(ch.rate_per_min),
      // Trong config do backend trả về, thông tin xác thực là giá trị che giấu; đặt nguyên vẹn vào biểu mẫu,
      // gửi lại nguyên vẹn khi submit, backend dựa vào đó để giữ giá trị gốc trong DB.
      config: { ...ch.config },
      minSeverity: f.min_severity ?? "",
      includeText: (f.vulnclass_include ?? []).join("\n"),
      excludeText: (f.vulnclass_exclude ?? []).join("\n"),
      taskIDsText: (f.task_ids ?? []).join(","),
      assetIDsText: (f.asset_ids ?? []).join(","),
      onStatusChange: f.on_status_change ?? false,
    });
    setOpen(true);
  }

  // buildConfig chuyển trạng thái biểu mẫu thành config của kênh.
  //
  // Quy tắc duy nhất, hai loại giá trị:
  //   - Giá trị che giấu ("__masked__...") gửi lại nguyên vẹn → backend hiểu là "trường này không đổi, giữ giá trị gốc trong DB"
  //   - Còn lại đều submit theo input của người dùng, chuỗi rỗng nghĩa là "xóa trắng trường này"
  //
  // Lý do không xử lý riêng trường thông tin xác thực (vd "để trống thì bỏ qua") là vì điều đó khiến người dùng **không thể xóa**
  // một khóa đã cấu hình sai — trên giao diện không có thao tác nào diễn đạt "tôi muốn xóa nó". Với quy tắc hiện tại,
  // xóa trắng ô nhập chính là xóa trắng trường đó, ngữ nghĩa duy nhất và người dùng kiểm soát được.
  // Giá trị che giấu không xuất hiện trong ô nhập (xem ConfigField), nên "ô có chữ" luôn tương đương
  // "do người dùng chủ động nhập".
  function buildConfig(): Record<string, unknown> {
    const defs = CHANNEL_FIELDS[form.kind] ?? [];
    const out: Record<string, unknown> = {};
    for (const d of defs) {
      const raw = form.config[d.key];
      if (d.kind === "switch") {
        out[d.key] = raw === true;
        continue;
      }
      if (typeof raw === "string" && raw.startsWith("__masked__")) {
        out[d.key] = raw;
        continue;
      }
      if (d.kind === "number") {
        const n = Number(raw);
        out[d.key] = Number.isFinite(n) && n > 0 ? n : 0;
        continue;
      }
      if (d.kind === "kv") {
        out[d.key] = parseKV(String(raw ?? ""));
        continue;
      }
      if (d.kind === "list") {
        out[d.key] = String(raw ?? "")
          .split(/[\s,，]+/)
          .map((s) => s.trim())
          .filter(Boolean);
        continue;
      }
      out[d.key] = String(raw ?? "").trim();
    }
    return out;
  }

  function buildFilter(): NotificationFilter {
    return {
      min_severity: form.minSeverity || undefined,
      vulnclass_include: parseKeywords(form.includeText),
      vulnclass_exclude: parseKeywords(form.excludeText),
      task_ids: parseIDs(form.taskIDsText),
      asset_ids: parseIDs(form.assetIDsText),
      on_status_change: form.onStatusChange,
    };
  }

  async function saveForm() {
    if (!form.name.trim()) {
      toast.error("Vui lòng điền tên kênh");
      return;
    }
    setSaving(true);
    try {
      const payload = {
        name: form.name.trim(),
        kind: form.kind,
        mode: form.mode,
        enabled: form.enabled,
        config: buildConfig(),
        filter: buildFilter(),
        rate_per_min: form.ratePerMin.trim() === "" ? undefined : Number(form.ratePerMin),
      };
      if (editing) {
        await api.notifyUpdateChannel(editing.id, payload);
        toast.success("Đã lưu");
        setOpen(false);
      } else {
        await api.notifyCreateChannel(payload);
        toast.success("Đã thêm kênh");
        setOpen(false);
      }
      load();
    } catch (e) {
      toast.error("Lưu thất bại: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function testChannel() {
    if (!editing) return;
    setTesting(true);
    try {
      const r = await api.notifyTestChannel(editing.id);
      toast.success(`Đã gửi tin nhắn thử nghiệm (${r.latency_ms} ms), hãy vào nhóm để xác nhận`);
    } catch (e) {
      // Backend trả lại nguyên văn lỗi gốc của kênh, đây là manh mối duy nhất để dò cấu hình, hiển thị nguyên vẹn.
      toast.error("Kiểm thử thất bại: " + (e as Error).message, { duration: 12000 });
    } finally {
      setTesting(false);
    }
  }

  async function removeChannel(ch: NotificationChannel) {
    try {
      await api.notifyDeleteChannel(ch.id);
      toast.success(`Đã xóa: ${ch.name}`);
      setOpen(false);
      load();
    } catch (e) {
      toast.error("Xóa thất bại: " + (e as Error).message);
    }
  }

  async function toggleEnabled(ch: NotificationChannel) {
    try {
      await api.notifyUpdateChannel(ch.id, { enabled: !ch.enabled });
      load();
    } catch (e) {
      toast.error("Thao tác thất bại: " + (e as Error).message);
    }
  }

  async function toggleGlobal(on: boolean) {
    setGlobalSaving(true);
    try {
      await api.setSettings({ notify_enabled: on });
      setMeta((m) => (m ? { ...m, enabled: on } : m));
      toast.success(on ? "Đã bật đẩy thông báo" : "Đã tạm dừng đẩy thông báo");
    } catch (e) {
      toast.error("Thao tác thất bại: " + (e as Error).message);
    } finally {
      setGlobalSaving(false);
    }
  }

  async function saveGlobal() {
    setGlobalSaving(true);
    try {
      const patch: Record<string, unknown> = { notify_public_base_url: baseURL.trim() };
      const n = Number(digestMin);
      if (Number.isFinite(n) && n > 0) patch.notify_digest_interval_min = n;
      await api.setSettings(patch);
      toast.success("Đã lưu");
      load();
    } catch (e) {
      toast.error("Lưu thất bại: " + (e as Error).message);
    } finally {
      setGlobalSaving(false);
    }
  }

  const fields = CHANNEL_FIELDS[form.kind] ?? [];
  const secretKeys = new Set(meta?.kinds.find((k) => k.kind === form.kind)?.secret_keys ?? []);
  const defaultRate = meta?.kinds.find((k) => k.kind === form.kind)?.default_rate_per_min ?? 0;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Đẩy thông báo</h1>
          <p className="text-muted-foreground text-sm">
            Đẩy thông báo đến DingTalk / Feishu / WeCom khi phát hiện lỗ hổng · mỗi kênh có thể đặt riêng thời điểm đẩy và quy tắc lọc
          </p>
        </div>
        {meta && (
          // Dùng div thay vì label: Switch đã có aria-label, bọc thêm một label bên ngoài
          // vừa không liên kết được với điều khiển gốc nào, vừa khiến việc bấm vào chữ trông như có thể chuyển đổi.
          <div className="flex shrink-0 items-center gap-2 text-sm">
            <span className="text-muted-foreground">Công tắc tổng</span>
            <Switch
              checked={meta.enabled}
              disabled={globalSaving}
              onCheckedChange={toggleGlobal}
              aria-label="Công tắc tổng đẩy thông báo"
            />
          </div>
        )}
      </div>

      {meta && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
          <StatTile label="Kênh" value={`${meta.stats.channels_on} / ${meta.stats.channels}`} hint="Đang bật / tổng số" />
          <StatTile label="Đã gửi hôm nay" value={String(meta.stats.sent_today)} />
          <StatTile label="Chờ gửi" value={String(meta.stats.pending)} />
          <StatTile label="Thất bại" value={String(meta.stats.failed)} tone={meta.stats.failed > 0 ? "red" : undefined} />
          <StatTile
            label="Tồn đọng lâu nhất"
            value={formatBacklog(meta.stats.backlog_age_ms)}
            // Tuổi tồn đọng hữu ích hơn số mục tồn đọng rất nhiều: 3 mục tồn đọng có thể là từ 3 giây đến 3 giờ.
            hint={meta.stats.backlog_age_ms > 5 * 60_000 ? "Đẩy thông báo có thể đang bị kẹt" : undefined}
            tone={meta.stats.backlog_age_ms > 5 * 60_000 ? "red" : undefined}
          />
        </div>
      )}

      <Card className="gap-3">
        <CardHeader>
          <CardTitle className="text-base">Cài đặt toàn cục</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="grid gap-2">
            <Label htmlFor="n-base">Địa chỉ liên kết về</Label>
            <Input
              id="n-base"
              placeholder="https://artex.example.com"
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">Địa chỉ mà nút "Xem chi tiết" trong tin nhắn trỏ tới. Để trống thì không kèm nút.</p>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="n-digest">Chu kỳ tổng hợp (phút)</Label>
            <Input
              id="n-digest"
              type="number"
              min={1}
              max={1440}
              placeholder="30"
              value={digestMin}
              onChange={(e) => setDigestMin(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">Chỉ có hiệu lực với kênh ở chế độ "Tổng hợp".</p>
          </div>
          <div className="sm:col-span-2">
            <Button onClick={saveGlobal} disabled={globalSaving}>
              Lưu cài đặt toàn cục
            </Button>
          </div>
        </CardContent>
      </Card>

      <Tabs value={tab} onValueChange={(v) => setTab(v as "channels" | "deliveries")} className="flex flex-col gap-4">
        <TabsList>
          <TabsTrigger value="channels">Kênh</TabsTrigger>
          <TabsTrigger value="deliveries">Bản ghi gửi</TabsTrigger>
        </TabsList>

        <TabsContent value="channels">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <button
              type="button"
              onClick={openAdd}
              className="text-foreground/70 border-foreground/70 hover:bg-muted/60 hover:shadow-sm flex min-h-[130px] flex-col items-center justify-center gap-2 rounded-xl border border-dashed transition"
            >
              <PlusIcon className="size-6" />
              <span className="text-sm">Thêm kênh</span>
            </button>

            {channels.map((ch) => (
              <Card
                key={ch.id}
                onClick={() => openEdit(ch)}
                className="hover:border-primary/60 cursor-pointer gap-3 transition hover:shadow-sm"
              >
                <CardHeader>
                  <div className="flex items-center gap-2">
                    <BellIcon className="text-muted-foreground size-4 shrink-0" />
                    <CardTitle className="truncate text-base">{ch.name}</CardTitle>
                    {/* Toàn bộ thẻ có thể bấm (vào chỉnh sửa), nên hai điều khiển này phải tự chặn bubbling,
                        nếu không bật/tắt/xóa sẽ đồng thời kích hoạt chỉnh sửa. Gắn stopPropagation lên chính điều khiển
                        thay vì bọc một div: bọc div sẽ tạo ra một phần tử tĩnh "trông có thể tương tác nhưng không
                        có vai trò", vừa gây cảnh báo a11y, vừa không hợp về mặt ngữ nghĩa. */}
                    <div className="ml-auto flex items-center gap-2">
                      <Switch
                        checked={ch.enabled}
                        onCheckedChange={() => toggleEnabled(ch)}
                        onClick={(e) => e.stopPropagation()}
                        aria-label="Bật"
                      />
                      <Button
                        size="icon"
                        variant="outline"
                        aria-label="Xóa"
                        onClick={(e) => {
                          e.stopPropagation();
                          // void bỏ Promise một cách tường minh: removeChannel tự catch và toast,
                          // ở đây không cần await (onClick không phải async).
                          void removeChannel(ch);
                        }}
                      >
                        <Trash2Icon className="text-destructive" />
                      </Button>
                    </div>
                  </div>
                </CardHeader>
                <CardContent className="grid gap-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="outline">{KIND_LABEL[ch.kind] ?? ch.kind}</Badge>
                    <Badge variant="outline">{ch.mode === "digest" ? "Tổng hợp" : "Thời gian thực"}</Badge>
                    {!ch.enabled && <Badge variant="outline">Đã tắt</Badge>}
                  </div>
                  <FilterSummary filter={ch.filter} />
                </CardContent>
              </Card>
            ))}
          </div>
        </TabsContent>

        <TabsContent value="deliveries">
          <DeliveryList channels={channels} />
        </TabsContent>
      </Tabs>

      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="w-full data-[side=right]:sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>{editing ? editing.name : "Thêm kênh thông báo"}</SheetTitle>
            <SheetDescription>
              {KIND_LABEL[form.kind] ?? form.kind}
              {defaultRate > 0 ? ` · Giới hạn mặc định ${defaultRate} tin/phút` : " · Không giới hạn"}
            </SheetDescription>
          </SheetHeader>

          <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">
            <div className="grid gap-4 py-4">
              <div className="grid gap-2">
                <Label>Loại kênh</Label>
                <Select
                  value={form.kind}
                  onValueChange={(v) => {
                    // Đổi loại đồng nghĩa đổi cả bộ trường thông tin xác thực, không thể gộp cấu hình cũ vào.
                    setF({ kind: v, config: {} });
                  }}
                  disabled={!!editing}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(meta?.kinds ?? []).map((k) => (
                      <SelectItem key={k.kind} value={k.kind}>
                        {KIND_LABEL[k.kind] ?? k.kind}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {editing && (
                  <p className="text-muted-foreground text-xs">
                    Không thể sửa loại kênh — đổi loại đồng nghĩa đổi cả bộ thông tin xác thực, hãy tạo kênh mới.
                  </p>
                )}
              </div>

              <div className="grid gap-2">
                <Label htmlFor="n-name">Tên kênh</Label>
                <Input
                  id="n-name"
                  placeholder="Nhóm ứng phó khẩn cấp / nhóm thông báo hằng ngày"
                  value={form.name}
                  onChange={(e) => setF({ name: e.target.value })}
                />
              </div>

              {fields.length === 0 ? (
                <p className="text-muted-foreground text-sm">
                  Biểu mẫu của kênh này chưa được định nghĩa (frontend thiếu mục CHANNEL_FIELDS), hãy bổ sung rồi thử lại.
                </p>
              ) : (
                fields.map((d) => (
                  <ConfigField
                    key={d.key}
                    def={d}
                    value={form.config[d.key]}
                    isSecret={secretKeys.has(d.key)}
                    onChange={(v) => setCfg(d.key, v)}
                  />
                ))
              )}

              <div className="grid gap-2">
                <Label>Thời điểm đẩy</Label>
                <Select value={form.mode} onValueChange={(v) => setF({ mode: v as "realtime" | "digest" })}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="realtime">Thời gian thực · mỗi lỗ hổng gửi một tin riêng</SelectItem>
                    <SelectItem value="digest">Tổng hợp · gộp thành một tin theo chu kỳ</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs">
                  Muốn "mức cao gửi thời gian thực, còn lại tổng hợp" thì tạo hai kênh: một thời gian thực + ngưỡng mức cao, một tổng hợp + không giới hạn mức.
                </p>
              </div>

              <div className="grid gap-2">
                <Label htmlFor="n-rate">Giới hạn tốc độ (tin/phút)</Label>
                <Input
                  id="n-rate"
                  type="number"
                  min={0}
                  placeholder={defaultRate > 0 ? String(defaultRate) : "0 = không giới hạn"}
                  value={form.ratePerMin}
                  onChange={(e) => setF({ ratePerMin: e.target.value })}
                />
                <p className="text-muted-foreground text-xs">
                  Để trống dùng giá trị mặc định của kênh; 0 nghĩa là không giới hạn. Vượt giới hạn sẽ không mất tin, chỉ gửi chậm lại.
                </p>
              </div>

              <div className="border-t pt-4">
                <p className="mb-3 text-sm font-medium">Quy tắc lọc (để trống là không lọc)</p>
                <div className="grid gap-4">
                  <div className="grid gap-2">
                    <Label>Mức tối thiểu</Label>
                    <Select
                      value={form.minSeverity || "all"}
                      onValueChange={(v) => setF({ minSeverity: v === "all" ? "" : v })}
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {SEVERITY_OPTIONS.map((o) => (
                          <SelectItem key={o.value || "all"} value={o.value || "all"}>
                            {o.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-inc">Chỉ đẩy các loại lỗ hổng này</Label>
                    <Textarea
                      id="n-inc"
                      placeholder={"SQL injection\nCommand execution"}
                      value={form.includeText}
                      onChange={(e) => setF({ includeText: e.target.value })}
                    />
                    <p className="text-muted-foreground text-xs">
                      Mỗi dòng một từ khóa, khớp chuỗi con không phân biệt hoa thường. Để trống = tất cả loại.
                    </p>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-exc">Loại trừ các loại lỗ hổng này</Label>
                    <Textarea
                      id="n-exc"
                      placeholder={"Rò rỉ thông tin"}
                      value={form.excludeText}
                      onChange={(e) => setF({ excludeText: e.target.value })}
                    />
                    <p className="text-muted-foreground text-xs">Loại trừ ưu tiên hơn bao gồm: khi trúng cả hai sẽ bị loại trừ.</p>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-tasks">Giới hạn ID tác vụ</Label>
                    <Input
                      id="n-tasks"
                      placeholder="1, 2, 3"
                      value={form.taskIDsText}
                      onChange={(e) => setF({ taskIDsText: e.target.value })}
                    />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="n-assets">Giới hạn ID tài sản</Label>
                    <Input
                      id="n-assets"
                      placeholder="10, 11"
                      value={form.assetIDsText}
                      onChange={(e) => setF({ assetIDsText: e.target.value })}
                    />
                    <p className="text-muted-foreground text-xs">Tác vụ/tài sản để trống = không giới hạn; sau khi điền yêu cầu có giao với lỗ hổng.</p>
                  </div>
                  <div className="flex items-center gap-2 text-sm">
                    <Switch
                      checked={form.onStatusChange}
                      onCheckedChange={(v) => setF({ onStatusChange: v })}
                      aria-label="Nhận thay đổi trạng thái"
                    />
                    Cũng đẩy khi trạng thái xử lý lỗ hổng thay đổi (chỉ chế độ thời gian thực)
                  </div>
                </div>
              </div>

              <div className="flex items-center gap-2 text-sm">
                <Switch checked={form.enabled} onCheckedChange={(v) => setF({ enabled: v })} aria-label="Bật" />
                Bật kênh này
              </div>
            </div>

            <div className="flex gap-2 pt-2 pb-6">
              <Button onClick={saveForm} disabled={saving}>
                {editing ? "Lưu" : "Thêm"}
              </Button>
              {editing && (
                <Button variant="outline" onClick={testChannel} disabled={testing}>
                  <SendIcon /> Gửi tin nhắn thử nghiệm
                </Button>
              )}
            </div>
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}
