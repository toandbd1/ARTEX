"use client";

import * as React from "react";

import { CpuIcon, FlaskConicalIcon, KeyboardIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { Settings } from "@/lib/types";

import { UpdateCard } from "./_components/update-card";

export default function SystemSettingsPage() {
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // Phạm vi tiêm ràng buộc thao tác (mặc định đều bật).
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // Tính năng thử nghiệm: nén ngữ cảnh noa (mặc định tắt).
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // Tùy chọn thuần frontend: không qua /api/settings, đọc/ghi trực tiếp localStorage.
  const sendMode = useChatSendMode();

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    setGlobalProxyInput(s.global_proxy ?? "");
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
  }, []);

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("Số lượng đồng thời phải là số nguyên lớn hơn 0");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("Đã lưu số work agent đồng thời (có hiệu lực với các tác vụ khởi động sau)");
      })
      .catch((e) => toast.error("Lưu thất bại: " + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const savePython = () => {
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("Đã lưu cấu hình trình thông dịch Python");
      })
      .catch((e) => toast.error("Lưu thất bại: " + (e as Error).message))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSaving(false));
  };

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "Đã bật Agent tự động liên kết lưu lượng" : "Đã tắt Agent tự động liên kết lưu lượng");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`Lưu thất bại: ${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "Đã bật nén ngữ cảnh noa (có hiệu lực với các lần chạy khởi động sau)" : "Đã tắt nén ngữ cảnh noa (khôi phục nén tích hợp sẵn)");
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(`Lưu thất bại: ${(e as Error).message}`);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("Đã lưu cấu hình tìm kiếm mạng");
      })
      .catch((e) => {
        toast.error("Lưu thất bại: " + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("Đã lưu Brave API Key");
      })
      .catch((e) => toast.error("Lưu thất bại: " + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("Đã lưu Tavily API Key");
      })
      .catch((e) => toast.error("Lưu thất bại: " + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "Đã lưu proxy đầu ra" : "Đã xóa proxy đầu ra (chuyển về kết nối trực tiếp)");
      })
      .catch((e) => toast.error("Lưu thất bại: " + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  const saveGlobalProxy = () => {
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(globalProxyInput.trim() ? "Đã lưu proxy toàn cục" : "Đã xóa proxy toàn cục (chuyển về kết nối trực tiếp)");
      })
      .catch((e) => toast.error("Lưu thất bại: " + (e as Error).message))
      .finally(() => setSavingGlobalProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`Kiểm thử tìm kiếm thành công · ${r.backend} trả về ${r.count} kết quả`);
        else toast.error("Kiểm thử tìm kiếm thất bại: " + (r.error || "Lỗi không xác định"));
      })
      .catch((e) => toast.error("Kiểm thử tìm kiếm thất bại: " + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Cấu hình hệ thống</h1>
        <p className="text-muted-foreground text-sm">Công tắc runtime toàn cục</p>
      </div>

      {/* Nhiều cột thay vì grid: thẻ tìm kiếm mạng cao gấp nhiều lần các thẻ khác, và chiều cao thay đổi theo backend được chọn
          (ô nhập key của brave/tavily render có điều kiện). Grid sẽ căng theo thẻ cao nhất cho cả hàng, để lại khoảng trống lớn
          bên cạnh, còn nhiều cột tự động cân bằng lấp đầy theo chiều cao nội dung. Khoảng cách thẻ dùng mb thay vì gap — trong
          bố cục nhiều cột, column-gap chỉ quản khoảng cách cột, khoảng cách hàng phải do chính phần tử con cung cấp. */}
      <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
        <UpdateCard />

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Thu thập lưu lượng
            </CardTitle>
            <CardDescription>
              Sau khi bật, toàn bộ lưu lượng HTTP của mọi Agent sẽ qua proxy ghi lại và được lưu đầy đủ vào DB, đồng thời tiêm công cụ
              traffic_search / traffic_get và cấu hình proxy vào Agent (prompt có phần mô tả proxy).
              <br />
              Khi tắt (mặc định) sẽ không ghi bất kỳ lưu lượng nào: Agent
              <b>không</b> nhận được cấu hình proxy và công cụ lưu lượng, prompt cũng <b>không chứa</b> nội dung liên quan đến proxy. Sau khi chuyển đổi sẽ rebuild Agent ngay để có hiệu lực.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
              {trafficCapture ? "Đã bật · đang ghi lưu lượng và tiêm proxy" : "Đã tắt · không ghi, không tiêm proxy"}
            </Label>
            <Switch
              id="traffic-capture"
              checked={trafficCapture}
              disabled={!loaded || saving}
              onCheckedChange={toggleTraffic}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Agent tự động liên kết lưu lượng
            </CardTitle>
            <CardDescription id="agent-traffic-binding-description">
              Mặc định tắt. Sau khi bật, report Agent được kích hoạt khi lỗ hổng ghi vào DB sẽ đối chiếu HTTP request/response đã có, liên kết lưu lượng tương ứng rồi mới viết báo cáo.
              <b>Việc xem gói dữ liệu và các lượt gọi công cụ bổ sung sẽ làm tăng tiêu hao Token.</b>
              <br />
              TCP, chưa bắt gói hoặc không có lưu lượng khớp thì vẫn báo cáo bình thường. Công tắc này không ảnh hưởng đến thu thập lưu lượng, liên kết thủ công và xem bằng chứng đã lưu. Có hiệu lực với
              lượt Agent tiếp theo; sau khi tắt sẽ từ chối ngay các liên kết tự động mới.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
              {agentTrafficBinding ? "Đã bật · sẽ tăng tiêu hao Token" : "Đã tắt · có thể tiếp tục liên kết thủ công"}
            </Label>
            <Switch
              id="agent-traffic-binding"
              aria-describedby="agent-traffic-binding-description"
              checked={agentTrafficBinding}
              disabled={!loaded || saving}
              onCheckedChange={toggleAgentTrafficBinding}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Proxy toàn cục
            </CardTitle>
            <CardDescription>
              <b>Lưu lượng mục tiêu</b> của tất cả Agent đi ra mạng qua proxy này (ẩn IP nguồn / đi qua bước nhảy). Hỗ trợ <b>http / https / socks5</b>, có thể kèm xác thực{" "}
              <code>user:pass</code>. Để trống = kết nối trực tiếp.
              <br />
              Khi bật <b>thu thập lưu lượng</b>, nó đóng vai trò <b>upstream</b> của proxy ghi lại (lưu lượng vẫn được lưu đầy đủ vào DB, rồi đi ra mạng qua proxy này); khi tắt thu thập, nó được tiêm trực tiếp
              cho bash / WebFetch của Agent để đi ra mạng. Độc lập với proxy tìm kiếm mạng và proxy LLM.
              <br />
              <b>Gợi ý</b>: socks5 khi <b>tắt thu thập</b> phụ thuộc vào việc từng công cụ dòng lệnh có hỗ trợ <code>ALL_PROXY</code> không (curl
              dùng được, một số công cụ có thể bỏ qua); nếu chủ yếu dùng socks5, nên bật thu thập lưu lượng — đường đi này do chính MITM
              quay số, công cụ không hề biết và ổn định hiệu lực.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
              Địa chỉ proxy
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="global-proxy"
                autoComplete="off"
                placeholder="socks5://user:pass@host:1080 hoặc http://host:port (để trống = kết nối trực tiếp)"
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                Lưu
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? "Đã cấu hình · mọi lưu lượng mục tiêu đi ra mạng qua proxy này" : "Chưa cấu hình · lưu lượng mục tiêu kết nối trực tiếp"}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              Tiêm ràng buộc thao tác
            </CardTitle>
            <CardDescription>
              Sau khi bật, các <b>ràng buộc thao tác</b> của từng tác vụ (các mục allow/deny được duy trì trong "Ràng buộc thao tác" ở trang tổng quan tác vụ) sẽ được ghép vào
              system prompt của Agent tương ứng, dùng để khoanh vùng ranh giới thăm dò (vd "chỉ test cổng hiện tại" "cấm brute-force").
              <br />
              Có thể kiểm soát riêng việc tiêm vào <b>người lập kế hoạch (planner)</b> và <b>người thực thi (worker)</b>
              ; mặc định đều bật. Chuyển đổi có hiệu lực ngay (lượt đọc tiếp theo), không cần rebuild Agent. Sau khi tắt, Agent đó sẽ không thấy ràng buộc nữa.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                Tiêm vào người lập kế hoạch (planner){injectPlanner ? " · đã bật" : " · đã tắt"}
              </Label>
              <Switch
                id="inject-planner"
                checked={injectPlanner}
                disabled={!loaded}
                onCheckedChange={toggleInjectPlanner}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
                Tiêm vào người thực thi (worker){injectWorker ? " · đã bật" : " · đã tắt"}
              </Label>
              <Switch
                id="inject-worker"
                checked={injectWorker}
                disabled={!loaded}
                onCheckedChange={toggleInjectWorker}
              />
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <FlaskConicalIcon className="size-4" />
              Tính năng thử nghiệm
            </CardTitle>
            <CardDescription>
              Cơ chế đang trong quá trình kiểm chứng, mặc định tắt. Có thể thay đổi hành vi của Agent hoặc ảnh hưởng đến độ ổn định, hãy bật sau khi đã hiểu rõ tác động.
              <br />
              <b>Nén ngữ cảnh noa</b>: do mô hình chủ động nén lịch sử hội thoại dài (norma v0.4.0). Sau khi bật, bốn loại Agent mà nền tảng tích hợp (
              <b>người lập kế hoạch / người thực thi / Agent chính / hội thoại</b>) sẽ chuyển sang dùng noa để tiếp quản ngữ cảnh, thay thế nén tích hợp sẵn,
              bản gốc sau nén sẽ được lưu trữ vào thư mục làm việc của tác vụ để tiện truy vết. Chuyển đổi có hiệu lực ngay (với các lần chạy khởi động sau), không cần rebuild Agent;
              sau khi tắt sẽ lập tức khôi phục nén tích hợp sẵn.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
              Nén ngữ cảnh noa{noaCompaction ? " · đã bật" : " · đã tắt"}
            </Label>
            <Switch
              id="noa-compaction"
              checked={noaCompaction}
              disabled={!loaded}
              onCheckedChange={toggleNoaCompaction}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <SearchIcon className="size-4" />
              Tìm kiếm mạng
            </CardTitle>
            <CardDescription>
              Đây là <b>công tắc tổng + cấu hình nguồn</b> của tìm kiếm mạng. Sau khi bật, mới có thể trong <b>cấu hình của từng Agent</b> chọn riêng có bật
              <b>web_search</b> hay không (chỉ trả về tiêu đề/liên kết/tóm tắt, không lấy nội dung chính; việc lấy do WebFetch đảm nhiệm). Tìm kiếm mạng <b>không đi qua</b>
              proxy ghi lại, độc lập với thu thập lưu lượng.
              <br />
              Nguồn có thể chọn <b>DuckDuckGo (ddgs)</b> (không cần Key), <b>Brave (bản miễn phí)</b> (cần điền Brave API Key),{" "}
              <b>Tavily</b> (cần điền Tavily API Key) hoặc <b>DeepSeek</b> (tái sử dụng cấu hình LLM hiện tại). Khi công tắc tổng tắt, công tắc tìm kiếm mạng của từng
              Agent không khả dụng.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                {webSearch ? "Công tắc tổng đã bật · có thể bật riêng trong cấu hình từng Agent" : "Đã tắt · các Agent không thể bật tìm kiếm mạng"}
              </Label>
              <Switch
                id="web-search"
                checked={webSearch}
                disabled={!loaded || saving}
                onCheckedChange={(v) => {
                  setWebSearch(v); // optimistic
                  saveWebSearch({ web_search_enabled: v });
                }}
              />
            </div>

            {webSearch && (
              <div className="flex items-center justify-between gap-4">
                <Label className="text-sm font-normal text-muted-foreground">Nguồn tìm kiếm</Label>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="w-48 shrink-0">
                    <SelectValue placeholder="Chọn nguồn" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">DuckDuckGo (ddgs · miễn phí không cần Key)</SelectItem>
                    <SelectItem value="brave-free">Brave (bản miễn phí · cần Key)</SelectItem>
                    <SelectItem value="tavily">Tavily (cần Key)</SelectItem>
                    <SelectItem value="deepseek">DeepSeek (chính thức)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {webSearch && backend === "deepseek" && (
              <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                <p className="text-sm font-medium">Tìm kiếm trực tuyến chính thức của DeepSeek</p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  Nguồn này tái sử dụng trực tiếp <b>cấu hình LLM đang kích hoạt</b>. Do đó nó
                  <b>chỉ hỗ trợ mô hình DeepSeek chính thức</b>, và cấu hình đó <b>phải dùng giao thức anthropic</b>
                  — endpoint giao thức OpenAI của DeepSeek không hỗ trợ tìm kiếm phía máy chủ. Sau khi chuyển cấu hình LLM, nguồn này có thể mất hiệu lực.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  Khác với các nguồn khác, việc tìm kiếm do <b>máy chủ DeepSeek thực thi</b>: mỗi lần tìm kiếm tiêu tốn thêm một lượt gọi mô hình (phát sinh phí
                  Token), request tìm kiếm <b>không đi qua proxy đầu ra ở trên</b>, cũng <b>không tính vào vết lưu lượng</b>; kết quả trả về <b>chỉ có tiêu đề và liên kết</b>
                  (không tóm tắt), khi cần nội dung chính thì do WebFetch lấy.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  Bạn tự xác nhận có thỏa các điều kiện trên hay không, hệ thống không chặn; có thể dùng nút "Kiểm thử tìm kiếm" bên dưới để chạy thử thực tế.
                </p>
              </div>
            )}

            {webSearch && backend === "brave-free" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                  Brave Search API Key
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">Đã cấu hình</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="brave-key"
                    type="password"
                    autoComplete="off"
                    placeholder={braveKeySet ? "Đã cấu hình (để trống thì giữ nguyên)" : "Nhập Brave API Key"}
                    value={braveKeyInput}
                    disabled={!loaded || savingKey}
                    onChange={(e) => setBraveKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveBraveKey}
                    disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                  >
                    Lưu
                  </Button>
                </div>
                {braveNeedsKey && (
                  <p className="text-xs text-amber-500">
                    Đã chọn Brave nhưng chưa cấu hình Key — trước khi lưu Key, công cụ tìm kiếm sẽ không được bật.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">
                  Hạn mức bản miễn phí khoảng 2.000 lần/tháng. Truy cập https://brave.com/search/api/ để lấy Key.
                </p>
              </div>
            )}

            {webSearch && backend === "tavily" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                  Tavily Search API Key
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">Đã cấu hình</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="tavily-key"
                    type="password"
                    autoComplete="off"
                    placeholder={tavilyKeySet ? "Đã cấu hình (để trống thì giữ nguyên)" : "Nhập Tavily API Key (tvly-…)"}
                    value={tavilyKeyInput}
                    disabled={!loaded || savingTavilyKey}
                    onChange={(e) => setTavilyKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveTavilyKey}
                    disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                  >
                    Lưu
                  </Button>
                </div>
                {webSearch && backend === "tavily" && !tavilyKeySet && (
                  <p className="text-xs text-amber-500">
                    Đã chọn Tavily nhưng chưa cấu hình Key — trước khi lưu Key, công cụ tìm kiếm sẽ không được bật.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">Truy cập https://tavily.com để đăng ký và lấy API Key.</p>
              </div>
            )}

            {webSearch && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                  Proxy đầu ra (tùy chọn)
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder="http://host:port hoặc socks5://host:port (để trống = kết nối trực tiếp)"
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    Lưu
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">
                  Proxy đầu ra độc lập, chỉ dùng để truy cập endpoint tìm kiếm (VPN/SOCKS v.v.). Không liên quan đến proxy MITM ghi lưu lượng; khi mạng không thông thì truy cập qua proxy này.
                </p>
              </div>
            )}

            {webSearch && (
              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <p className="text-muted-foreground text-xs">
                  Dùng cấu hình hiện tại (nguồn + proxy + Key) để tìm thử một lần "test", kiểm tra có dùng được không.
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={runTest}
                  disabled={!loaded || testing}
                  className="shrink-0"
                >
                  {testing ? "Đang kiểm thử…" : "Kiểm thử tìm kiếm"}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Script tùy chỉnh · Trình thông dịch Python
            </CardTitle>
            <CardDescription>
              Công cụ tùy chỉnh loại <b>script</b> dùng nó để chạy Python. Khi khởi động sẽ tự động phát hiện (ưu tiên python3); tại đây có thể điền thủ công đường dẫn tuyệt đối của venv /
              phiên bản cụ thể, để trống thì tự động phát hiện khi chạy.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                className="font-mono text-sm"
                placeholder="/usr/bin/python3 (để trống = tự động phát hiện)"
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                Phát hiện lại
              </Button>
              <Button onClick={savePython} disabled={!loaded || saving}>
                Lưu
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CpuIcon className="size-4" />
              Đồng thờ công việc · Số Work Agent
            </CardTitle>
            <CardDescription>
              Số lượng work agent chạy đồng thờ cho mỗi tác vụ (mặc định 3). Giá trị càng lớn thăm dò đồng thờ càng nhiều, tiêu hao cũng càng cao. Sau khi sửa
              <b>có hiệu lực với các tác vụ khởi động sau</b>, tác vụ đang chạy không bị ảnh hưởng.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                className="w-32 font-mono text-sm"
                placeholder="3"
                value={workers}
                disabled={!loaded || savingWorkers}
                onChange={(e) => setWorkers(e.target.value)}
              />
              <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
                Lưu
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyboardIcon className="size-4" />
              Phím gửi của ô nhập phiên
            </CardTitle>
            <CardDescription>
              Trang hội thoại và ô nhập phiên của Agent chính trong chi tiết tác vụ dùng chung cài đặt này, có hiệu lực ngay sau khi chọn, không cần lưu.
              <br />
              Tùy chọn này <b>chỉ tồn tại trong trình duyệt này</b>, không đồng bộ theo tài khoản, đổi trình duyệt hoặc xóa dữ liệu trang sẽ phải đặt lại.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
              Cách gửi
            </Label>
            <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
              <SelectTrigger id="chat-send-mode" className="w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CHAT_SEND_MODE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
