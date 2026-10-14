"use client";

import { useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { AlertTriangle, ShieldCheck } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function LoginPage() {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [agreed, setAgreed] = useState(false);
  const [termsOpen, setTermsOpen] = useState(false);
  const [readToEnd, setReadToEnd] = useState(false);
  const termsBodyRef = useRef<HTMLDivElement>(null);

  // Phải cuộn tới cuối điều khoản (kể cả trường hợp nội dung hiển thị đủ mà không cần cuộn) mới được nhấn "Đồng ý".
  function handleTermsScroll() {
    const el = termsBodyRef.current;
    if (!el) return;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8) setReadToEnd(true);
  }

  useEffect(() => {
    if (!termsOpen) return;
    // Đặt lại khi mở, đồng thời xử lý trường hợp nội dung vốn chưa đủ một màn hình nên không kích hoạt được cuộn.
    setReadToEnd(false);
    const el = termsBodyRef.current;
    if (el && el.scrollHeight <= el.clientHeight + 8) setReadToEnd(true);
  }, [termsOpen]);

  useEffect(() => {
    // Đã đăng nhập thì vào thẳng giao diện chính (bản xuất tĩnh không có middleware lo phần chuyển hướng này).
    const token = auth.getToken();
    if (token) {
      // localStorage có thể vẫn còn thông tin xác thực nhưng cookie đã mất. Đồng bộ trước, rồi mới gửi yêu cầu mới,
      // tránh bộ bảo vệ phía máy chủ hoặc bộ đệm định tuyến đẩy chuyển hướng quay lại trang đăng nhập vẫn đang ở trạng thái checking.
      auth.setToken(token);
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError("Không thể kết nối tới dịch vụ backend"))
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!agreed) {
      setError("Vui lòng đọc và đồng ý với «Điều khoản sử dụng» trước");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.login("ARTEX", password);
      auth.setToken(token);
      window.location.replace("/function/tasks");
    } catch {
      setError("Tên người dùng hoặc mật khẩu không đúng");
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div role="status" className="flex min-h-dvh items-center justify-center text-muted-foreground">
        Đang kiểm tra trạng thái đăng nhập…
      </div>
    );
  }

  return (
    <div className="flex h-dvh">
      {/* Left panel */}
      <div className="hidden flex-col items-center justify-center bg-primary p-12 text-center lg:flex lg:w-1/3">
        <div className="relative flex items-center justify-center">
          <div className="absolute size-80 rounded-full border border-primary-foreground/10" />
          <div className="absolute size-60 rounded-full border border-primary-foreground/15" />
          <div className="absolute size-40 rounded-full border border-primary-foreground/20" />
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src="/logo.png" alt="ARTEX" width={160} height={160} className="relative brightness-0 invert" />
        </div>
      </div>

      {/* Right panel */}
      <div className="flex w-full items-center justify-center bg-background p-8 lg:w-2/3">
        <div className="w-full max-w-md space-y-10 py-24 lg:py-32">
          <div className="space-y-4 text-center">
            <h2 className="text-2xl font-medium tracking-tight">Đăng nhập</h2>
            <p className="mx-auto max-w-xl text-muted-foreground">Chào mừng trở lại, vui lòng nhập mật khẩu để tiếp tục sử dụng ARTEX</p>
          </div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">Tên người dùng</Label>
              <Input id="username" value="ARTEX" readOnly className="bg-muted text-muted-foreground" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Mật khẩu</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="Vui lòng nhập mật khẩu"
                autoFocus
                autoComplete="current-password"
              />
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="agree-terms"
                checked={agreed}
                onCheckedChange={(v) => setAgreed(v === true)}
                className="mt-0.5"
              />
              <Label htmlFor="agree-terms" className="text-sm font-normal leading-relaxed text-muted-foreground">
                Tôi đã đọc và đồng ý với
                <button
                  type="button"
                  onClick={() => setTermsOpen(true)}
                  className="mx-0.5 font-medium text-primary underline-offset-4 hover:underline"
                >
                  «Điều khoản sử dụng»
                </button>
              </Label>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={loading || !password || !agreed}>
              {loading ? "Đang đăng nhập..." : "Đăng nhập"}
            </Button>
          </form>
        </div>
      </div>

      <Dialog open={termsOpen} onOpenChange={setTermsOpen}>
        <DialogContent className="gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="flex-row items-center gap-3 border-b px-6 py-4">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <ShieldCheck className="size-5" />
            </div>
            <div className="space-y-0.5">
              <DialogTitle className="text-base">Điều khoản sử dụng và Tuyên bố miễn trừ trách nhiệm của ARTEX</DialogTitle>
              <p className="text-xs text-muted-foreground">
                Phiên bản v1.0 · Ngày hiệu lực 2026-09-18 · Vui lòng đọc đầy đủ toàn bộ các điều khoản dưới đây trước khi đăng nhập
              </p>
            </div>
          </DialogHeader>

          <div
            ref={termsBodyRef}
            onScroll={handleTermsScroll}
            className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
          >
            <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
              «Điều khoản sử dụng và Tuyên bố miễn trừ trách nhiệm» này (sau đây gọi là "Tuyên bố này") là thỏa thuận giữa bạn với tác giả và những người đóng góp của dự án ARTEX về việc sử dụng phần mềm này. Vui lòng đọc kỹ và hiểu đầy đủ nội dung các điều khoản trước khi sử dụng, đặc biệt là các điều khoản miễn trừ, giới hạn trách nhiệm và cấm được đánh dấu bằng chữ đậm hoặc khối màu.
              <span className="font-medium text-foreground">
                {" "}
                Một khi bạn tải xuống, cài đặt, truy cập hoặc sử dụng phần mềm này dưới bất kỳ hình thức nào, tức là bạn đã đọc, hiểu và đồng ý chấp nhận toàn bộ ràng buộc của Tuyên bố này.
              </span>
            </p>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  1
                </span>
                Điều 1 · Định nghĩa và giấy phép mã nguồn mở
              </h4>
              <p className="pl-7">
                Phần mềm này (ARTEX) là một chương trình mã nguồn mở phát hành theo giấy phép GNU Affero General Public License
                v3.0 (AGPL-3.0). Bạn có thể tự do sử dụng, sao chép, sửa đổi và phân phối phần mềm này theo giấy phép đó; nhưng mọi sản phẩm phái sinh (bao gồm dịch vụ trực tuyến cung cấp cho bên thứ ba qua mạng) đều phải được mở mã nguồn theo cùng giấy phép
                AGPL-3.0 và công khai mã nguồn đầy đủ tương ứng cho người dùng. Toàn văn điều khoản AGPL-3.0 căn cứ theo file LICENSE đính kèm.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  2
                </span>
                Điều 2 · Phạm vi sử dụng được ủy quyền
              </h4>
              <p className="pl-7">
                Phần mềm này chỉ dùng cho mục đích học tập cá nhân, nghiên cứu mã nguồn, thảo luận nguyên lý kỹ thuật bảo mật, và xác minh kỹ thuật trong môi trường cách ly cục bộ do chính bạn dựng, áp dụng cho các mục đích phi tấn công, phi phá hoại như học tập, nghiên cứu học thuật, rà soát mã nguồn. Ngoài các trường hợp được cho phép rõ ràng tại điều này, bạn không được dùng phần mềm này cho bất kỳ mục đích nào khác.
              </p>
            </section>

            <section className="space-y-2">
              <h4 className="flex items-center gap-2 font-medium text-destructive">
                <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
                  3
                </span>
                <AlertTriangle className="size-4" />
                Điều 3 · Hành vi bị cấm
              </h4>
              <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
                <li>
                  Nghiêm cấm quét, dò thăm, khai thác hoặc tấn công bất kỳ trang web, dịch vụ trực tuyến, hệ thống có kết nối mạng thuộc sở hữu của người khác hoặc bên thứ ba nào (bất kể đã được ủy quyền hay chưa, có phải tài sản của bạn hay không);
                </li>
                <li>Nghiêm cấm dùng phần mềm này cho bất kỳ hoạt động kiểm thử xâm nhập thực tế, đối kháng tấn công - phòng thủ, diễn tập đỏ - xanh hoặc môi trường production nào;</li>
                <li>Nghiêm cấm dùng phần mềm này để xâm nhập trái phép, đánh cắp dữ liệu, tống tiền, từ chối dịch vụ (DoS/DDoS) hoặc bất kỳ hoạt động phá hoại, phạm tội nào;</li>
                <li>Nghiêm cấm gỡ bỏ, sửa đổi hoặc lách qua bất kỳ thông tin bản quyền, giấy phép hoặc cảnh báo bảo mật nào trong phần mềm này và kết quả đầu ra của nó;</li>
                <li>Nghiêm cấm mọi hành vi vi phạm pháp luật, quy định và quy định quản lý của quốc gia hoặc khu vực nơi bạn sinh sống.</li>
              </ul>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  4
                </span>
                Điều 4 · Quyền sở hữu trí tuệ
              </h4>
              <p className="pl-7">
                Bản quyền và các quyền sở hữu trí tuệ liên quan của phần mềm này thuộc về tác giả và những người đóng góp của dự án, và các quyền tương ứng được cấp cho bạn trong phạm vi quy định của giấy phép
                AGPL-3.0. Ngoài các quyền được giấy phép đó cấp rõ ràng, Tuyên bố này không cấp cho bạn bất kỳ quyền nào khác dưới hình thức minh thị hay ngầm định.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  5
                </span>
                Điều 5 · Dữ liệu và quyền riêng tư
              </h4>
              <p className="pl-7">
                Phần mềm này là chương trình mã nguồn mở có thể tự triển khai, tác giả không vận hành bất kỳ dịch vụ tập trung nào và cũng không thu thập hay tải lên dữ liệu sử dụng của bạn. Mọi dữ liệu mà bạn tạo ra, xử lý hoặc tiếp xúc trong quá trình sử dụng đều do bạn tự kiểm soát và chịu trách nhiệm về tính hợp pháp và an toàn của nó; mọi hậu quả phát sinh do xử lý dữ liệu không đúng cách đều do bạn tự gánh chịu.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  6
                </span>
                Điều 6 · Tuân thủ và trách nhiệm pháp lý
              </h4>
              <p className="pl-7">
                Bạn phải tự tuân thủ toàn bộ pháp luật và quy định của quốc gia hoặc khu vực nơi bạn sinh sống về an ninh mạng, an toàn dữ liệu và bảo vệ thông tin cá nhân, tội phạm máy tính và các mặt liên quan.
                <span className="font-medium text-foreground">
                  {" "}
                  Mọi trách nhiệm pháp lý và hậu quả phát sinh do bạn vi phạm pháp luật nói trên hoặc thỏa thuận của Tuyên bố này đều do chính bạn độc lập gánh chịu, không liên quan đến tác giả và những người đóng góp của phần mềm này.
                </span>
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  7
                </span>
                Điều 7 · Tuyên bố miễn trừ và giới hạn trách nhiệm
              </h4>
              <p className="pl-7">
                Phần mềm này được cung cấp ở trạng thái "nguyên trạng (AS IS)" và "hiện có (AS
                AVAILABLE)", không kèm bất kỳ bảo đảm minh thị hay ngầm định nào, bao gồm nhưng không giới hạn ở bảo đảm về khả năng bán được, sự phù hợp cho mục đích cụ thể, tính chính xác và không vi phạm quyền. Trong phạm vi tối đa mà pháp luật áp dụng cho phép, tác giả và những người đóng góp của phần mềm này không chịu trách nhiệm về bất kỳ tổn thất trực tiếp, gián tiếp, ngẫu nhiên, đặc biệt hoặc mang tính hệ quả nào phát sinh từ việc sử dụng hoặc không thể sử dụng phần mềm này (bất kể cách sử dụng có đúng đắn hay không), bao gồm nhưng không giới hạn ở mất dữ liệu, hư hỏng hệ thống, gián đoạn kinh doanh, mất lợi nhuận hoặc tranh chấp pháp lý.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  8
                </span>
                Điều 8 · Thay đổi điều khoản và quyền giải thích cuối cùng
              </h4>
              <p className="pl-7">
                Tác giả có quyền cập nhật Tuyên bố này tùy theo pháp luật, quy định hoặc nhu cầu phát triển của dự án, phiên bản sau khi cập nhật sẽ được phát hành cùng dự án và có hiệu lực kể từ ngày công bố; việc bạn tiếp tục sử dụng phần mềm này tức là chấp nhận các điều khoản đã sửa đổi. Trong phạm vi pháp luật cho phép, quyền giải thích cuối cùng đối với Tuyên bố này thuộc về tác giả dự án. Nếu bất kỳ điều khoản nào của Tuyên bố này bị xác định là vô hiệu, điều đó không ảnh hưởng đến hiệu lực của các điều khoản còn lại.
              </p>
            </section>
          </div>

          <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-xs text-muted-foreground">
              {readToEnd ? "Bạn đã đọc toàn bộ điều khoản" : "Vui lòng cuộn điều khoản xuống cuối rồi mới xác nhận"}
            </p>
            <DialogClose asChild>
              <Button
                type="button"
                disabled={!readToEnd}
                onClick={() => {
                  setAgreed(true);
                  setError("");
                }}
              >
                Tôi đã đọc và đồng ý toàn bộ điều khoản
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
