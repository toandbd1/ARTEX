---
name: api-recon
description: Gọi skill này khi cần thu thập các API endpoint của website.
---

# API Recon (trinh sát interface frontend)

Trên điều kiện tiền đề **đã được ủy quyền**, phát hiện đầy đủ nhất có thể: **API backend** (path, method, tham số, response body), **route frontend**, **điểm kích hoạt chức năng UI** (Tab, popup, thao tác bảng...).

---

## Ranh giới và cấm (Agent bắt buộc đọc · vi phạm tức là vượt phạm vi)

Skill này **chỉ trinh sát mặt API / tham số**, không phải giai đoạn đào lỗ hổng hay khai thác xâm nhập.

### Ranh giới tác vụ

| Phạm vi | Cho phép | Cấm |
|---|---|---|
| **Mục tiêu** | Liệt kê path, method, tham số, route, điểm kích hoạt UI | SQLi/XSS/vượt quyền/brute-force/fuzz lỗ hổng, tấn công sửa gói tin, thao tác phá hoại |
| **Xác thực** | Hook + stub/mock vượt qua cửa đăng nhập **phía client** | Xin hoặc đoán tài khoản mật khẩu của người dùng; thử submit form đăng nhập thật |
| **Runtime** | Hook interface khi không có credential, dùng mock response để SPA vào được lớp vỏ sau đăng nhập | Các luồng bắt buộc phải có phiên backend thật mới tiếp tục được |

### Phân tích động không credential (mặc định ở Phase 3)

1. Qua `preload.js` / `runtime_harvest.js` **chặn và stub** các interface bootstrap như đăng nhập, quyền, menu;
2. Trả về cho các interface truy vấn nghiệp vụ mock body **đúng cấu trúc, mã nghiệp vụ thành công, dữ liệu có thể rỗng**;
3. Giúp frontend vẫn render được trang sau đăng nhập ngay cả khi không có backend hoặc gặp 401, từ đó kích hoạt thêm nhiều XHR/fetch/WebSocket;
4. **Dữ liệu rỗng, bảng trắng, UI placeholder đều là dự kiến** — đừng vì thế mà chuyển sang đăng nhập thật hay test lỗ hổng.

**Một câu tóm tắt**: dùng mock để mở rộng route frontend và mount component, **chỉ ghi lại request outbound**; backend trả về gì không quan trọng, quan trọng là frontend **còn gọi những interface nào nữa**.

### Cấm cứng về quy trình

| Cấm | Cách làm thay thế |
|---|---|
| Trước khi Phase 1 hoàn thành, grep/curl/Read entry chính `index-*.js` để trích API path | Chạy `OUTDIR/harvest_static.py` |
| Tự viết `extract_apis.py` hoặc script thay thế harvest | Sửa `OUTDIR/harvest_static.py` rồi chạy lại |
| Cùng một grep/lệnh thất bại ≥2 lần vẫn lặp lại | Đổi chiến lược: đọc tool_logs, sửa harvest, tra reference |
| Bỏ qua cổng A/B, chạy trực tiếp bản gốc trong `scripts/` | Sao chép sang OUTDIR và sửa theo mục tiêu |
| Tên người dùng/mật khẩu thật, OTP, OAuth... xác thực | stub/mock (xem phần trên) |
| Lấy lý do "cần dữ liệu thật" để bỏ stub, làm test vượt quyền/injection | Chỉ ghi outbound, thuộc ranh giới recon |
| Xóa, export dữ liệu nhạy cảm, ghi hàng loạt và các thao tác không thể hoàn tác | Click coverage cũng vậy |
| Chưa hoàn thành runtime + liệt kê động mà tuyên bố đã có toàn bộ trang và interface | Xem "Định nghĩa hoàn thành" hoặc ghi chú giới hạn |
| Chưa hoàn thành ma trận kích hoạt tham số + diff mà tuyên bố đã nắm toàn bộ tham số | Ma trận Phase 3b + diff Phase 5 |
| Dùng một mẫu runtime duy nhất để suy ra bắt buộc/tùy chọn | Diff nhiều mẫu hoặc suy ngược từ rule kiểm tra/lỗi |

---

## Mô hình hai tầng + chế độ chạy

| Tầng | Sản phẩm | Giới hạn |
|---|---|---|
| **Tĩnh** (JS bundle) | Toàn bộ path endpoint, dự thảo route, ứng viên trường điểm đóng gói | Không có HTTP method; tham số phải qua Phase 1b; bỏ sót URL ghép lúc runtime |
| **Runtime** (phiên sống) | method + body + response + URL động + WS/SSE; diff nhiều mẫu bổ sung tham số | Trang phải thực sự render mới phát request; một mẫu không đủ để xác định bắt buộc/tùy chọn |

| Chế độ chạy | Engine | Áp dụng |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | Danh sách API, METHOD/params/response body, WS/SSE, chạy hàng loạt có thể tái hiện |
| **coverage** | browser + `preload.js` | Click Tab/popup/bảng, bao phủ điểm chức năng sâu hơn |
| **both** | depth trước rồi coverage | Đầy đủ nhất, tốn thời gian nhất |

**Phương pháp tham số** (không có script dùng chung): path dùng harvest/regex; tham số dùng **mở rộng cửa sổ quanh điểm neo + chuỗi binding UI + diff nhiều mẫu + suy ngược từ lỗi** (công thức grep xem mục J của [reference.md](reference.md)).

---

## Định nghĩa hoàn thành

Phải thỏa mãn tất cả mới được tuyên bố recon hoàn thành:

- [ ] **Tĩnh**: Phase 1 harvest tạo ra `api_static.txt`, `routes.txt`, `js/`
- [ ] **Runtime**: ít nhất một trong depth hoặc coverage; coverage/both phải có **Hook có hiệu lực + vòng liệt kê động**
- [ ] **Vào được vỏ**: khi truy cập path nghiệp vụ không phải `/login` (chú ý hash route)
- [ ] **Tham số**: coverage/both hoàn thành ma trận kích hoạt tham số + `param_samples.json`; Phase 5 gộp `params_merged.json`
- [ ] **Độ sâu** (nếu trang module trắng): Phase 4 khôi phục cây quyền và chạy lại, cho tới khi xuất hiện **API cấp module** (không chỉ locale/bootstrap)
- [ ] **Bàn giao**: Phase 5 sản phẩm đầy đủ (xem bảng sản phẩm Phase 5); `insert_assets` ghi tài sản dịch vụ và endpoint

---

## Script và cổng kiểm soát

`scripts/` chỉ là mẫu tham khảo, **cấm** chạy trực tiếp bản gốc rồi coi là kết quả cuối.

**Quy tắc**: đọc trước → sửa theo mục tiêu → ghi vào `OUTDIR` (vd `recon/`) → ghi `CHANGES.md`; không khớp thì viết lại theo phương pháp, chỉ mượn cấu trúc.

| Cổng | Khi nào | Script tham khảo → bản sao OUTDIR | Mục thường phải sửa |
|---|---|---|---|
| **A (tĩnh)** | Sau Phase 0, **trước lần đầu** chạy harvest/spider | `harvest_static.py` / `spider_mpa.py` | **Đa số site regex mặc định chạy được ngay**; chỉ khi manifest/phương ngôn không khớp mới sửa regex endpoint, `publicPath` webpack/Vite, exclude/cookie của MPA |
| **B (runtime)** | Sau Phase 2, trước khi chạy depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Key Cookie/localStorage, giá trị thành công của neutralize, stubs, regex login, tiền tố api, hash/history |

**Thứ tự bắt buộc cho SPA** (không được hoán đổi; số Phase ưu tiên hơn "khám phá trước rồi mới script"):

| Bước | Bắt buộc | Cấm |
|---|---|---|
| Sau khi Phase 0 hoàn thành | Lệnh Bash tiếp theo = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read entry chính `index-*.js` (thường >500KB) |
| Cổng A | Sao chép script → sửa nhỏ theo cần → **chạy ngay** | Trích API thủ công trước rồi mới quyết định có harvest không |
| Trước khi Phase 1 hoàn thành | Kiểm tra sản phẩm bằng `wc -l`; 404 thì sửa harvest và thử lại | Tự viết script extract; grep lặp lại các URL chưa tải |
| Từ Phase 1b | Chỉ grep `OUTDIR/js/*.js` | Dùng bundle chính thay harvest |

- ✅ Sao chép `harvest_static.py` → (tùy chọn) sửa regex → **chạy ngay**
- ❌ curl bundle chính → grep nhiều lần → viết extract tạm → cuối cùng mới harvest
- **MPA**: sau Phase 0, lệnh Bash tiếp theo = `python3 OUTDIR/spider_mpa.py ...`

---

## Công cụ và ràng buộc đầu ra

| Ràng buộc | Giải thích |
|---|---|
| File lớn | `index-*.js` >100KB **cấm** Read/grep vào context; dùng script trong OUTDIR xử lý hàng loạt |
| Đầu ra grep | Bắt buộc `\| head -20` hoặc `-m 5`; hội thoại chỉ giữ tóm tắt path, không dán đoạn bundle |
| Kiểm tra | Dùng `wc -l`, `ls \| wc -l`; không Read cả thư mục |
| Thăm dò regex ban đầu | Tùy chọn, ≤1 lần, chỉ với chunk nhỏ ≤50KB hoặc HTML; tĩnh chính thức lấy harvest làm chuẩn |
| reference | Công thức/mẫu/xử lý sự cố xem [reference.md](reference.md), không lặp lại toàn văn inline |

---

## Lộ trình thực thi

```
Phase 0 phân loại + OUTDIR
  → Cổng A → Phase 1 harvest (★ chạy ngay ★)
  → Phase 1b đảo ngược tham số
  → Phase 2 ba cánh cửa xác thực → config.json
  → Cổng B → Phase 3 runtime + ma trận tham số
  → Phase 4 cây quyền (khi cần) → chạy lại Phase 3
  → Phase 5 gộp báo cáo + insert_assets chèn hàng loạt tất cả tài sản dịch vụ và endpoint api đã phát hiện, khi chèn tuyệt đối không được bỏ sót tài sản đã phát hiện
```

Tích chọn theo thứ tự; **mục trước chưa hoàn thành thì không được vào Phase tiếp theo**.

1. [ ] **Phase 0**: thăm dò SPA/MPA; tạo `OUTDIR` → [Phase 0](#phase-0--phân-loại)
2. [ ] **Cổng A + Phase 1**: sao chép script → harvest **ngay** → kiểm tra `wc -l` → [Phase 1](#phase-1--tĩnh)
3. [ ] **Phase 1b**: mở rộng cửa sổ quanh điểm neo + tầng binding → `param_candidates.json` → [Phase 1b](#phase-1b--đảo-ngược-tham-số)
4. [ ] **Phase 2**: ba cánh cửa xác thực → `config.json` → [Phase 2](#phase-2--ba-cánh-cửa-xác-thực)
5. [ ] **Cổng B**: điều chỉnh script runtime → [Phase 3](#phase-3--runtime)
6. [ ] **Phase 3**: depth / coverage / both; xác nhận vào được vỏ; ma trận kích hoạt tham số → `param_samples.json`
7. [ ] **Phase 4** (nếu cần): cây quyền → patch stubs → chạy lại Phase 3 → [Phase 4](#phase-4--khôi-phục-cây-quyền)
8. [ ] **Phase 5**: gộp sản phẩm + báo cáo + `insert_assets` → [Phase 5](#phase-5--gộp-và-báo-cáo)

---

## Phase 0 — Phân loại

Kéo HTML entry, **tạo `OUTDIR`** (không sửa `scripts/` trong skill):

- **SPA**: vỏ rỗng + `<div id=app>` + chunk → Phase 1–5
- **MPA**: SSR + `<form>`, không có endpoint bundle → sau cổng A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Sản phẩm: `forms.txt`, `links.txt`, `api_inline.txt`. SPA nếu forms ≈ 0 → chuyển Phase 1.

---

## Phase 1 — Tĩnh

Tuân thủ [Script và cổng kiểm soát](#script-và-cổng-kiểm-soát) · [Công cụ và ràng buộc đầu ra](#công-cụ-và-ràng-buộc-đầu-ra).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: parse script của HTML → manifest webpack/Vite → tải toàn bộ lazy chunk → tạo ra `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- Số chunk so với manifest: 404 phải sửa harvest rồi thử lại, không curl thủ công từng chunk
- `api_static.txt` quá ít → nới rộng regex endpoint trong OUTDIR rồi chạy lại (xem reference)

### Phase 1b — Đảo ngược tham số

path lấy từ Phase 1; trường tham số phải recon riêng. Quy tắc grep xem [Công cụ và ràng buộc đầu ra](#công-cụ-và-ràng-buộc-đầu-ra).

**Tiêu chuẩn hoàn thành**: với interface quan trọng phải trả lời được — tên trường, vị trí truyền, suy luận kiểu, có bắt buộc không, giá trị mẫu, độ tin cậy.

#### 1b.0 — Hình thức truyền

| Hình thức | Tham số ở đâu | Tĩnh ưu tiên xem |
|---|---|---|
| REST JSON | body + query | cạnh điểm neo path `(params\|data\|body)\s*:\s*\{` |
| GraphQL | `variables` | template gql, `$page: Int` |
| form truyền thống | urlencoded | `<form>`, `FormData` |
| Tải lên file | multipart | `FormData.append` |
| Tham số đường dẫn | `/user/:id` | bảng route + `useParams` / `$route.params` |
| Mã hóa/chữ ký | gói vào `sign`/`data` | Hook tham số đầu vào hàm mã hóa (mục D reference) |

Sản phẩm: mỗi interface ghi chú `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Mở rộng cửa sổ quanh điểm neo

Lấy path đã biết làm neo, mở rộng cửa sổ để tìm đối tượng đóng gói:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Tầng bao bọc | Manh mối tham số |
|---|---|
| instance axios | `data` / `params` |
| request thống nhất | interceptor inject trường toàn cục |
| client OpenAPI | chữ ký method được sinh ra |
| React Query / SWR | tham số thứ hai của hook |
| Vue composable | tham số đầu vào của composable |

Dấu vết kiểu: `yup`/`zod`/rules, `Form.Item name=`, Swagger nhúng.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Tầng binding

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Nguồn binding | Cách làm |
|---|---|
| submit form | theo submit → transform → API |
| tìm kiếm bảng | `getFieldsValue()` → `params` |
| route | `:id` / `?tab=` |
| interceptor | `tenantId` toàn cục, phân trang, sign |
| select enum | `options` → giá trị enum của API |

DevTools call stack lần ngược từ `fetch`/`XHR.send` lên hàm đóng gói.

#### 1b.3 — Ba câu hỏi đóng gói (≠ ba cửa xác thực Phase 2)

| Câu hỏi | Phải trả lời gì |
|---|---|
| **Lắp ráp** | payload được build ở đâu, dấu vết transform |
| **Kiểm tra** | required, pattern, enum |
| **Truyền** | path / query / body / multipart / header |

Cửa interceptor (Phase 2) tiện thể đọc trường inject toàn cục (Authorization, `X-Tenant-Id`, sign).

#### 1b.4 — Nối tiếp với Phase 3

Trường ứng viên đến từ tĩnh/tầng binding; **bắt buộc/tùy chọn/phụ thuộc điều kiện** phải qua ma trận tham số Phase 3 + diff + suy ngược lỗi Phase 5.

---

## Phase 2 — Ba cánh cửa xác thực

grep trong `OUTDIR/js/` (kèm `head`), ghi vào `config.json` (công thức xem reference):

| Cửa | Câu hỏi | Từ khóa |
|---|---|---|
| **Cửa render** | Làm sao biết đã đăng nhập? | `isLogin`, `getToken`, Cookie/localStorage |
| **Cửa interceptor** | Cái gì kích hoạt nhảy `/login`? | `response_code`, `errno`, axios interceptor |
| **Cửa nội dung** | Menu/quyền lấy từ đâu? | `menu`, `permission`, `role`, `acl`, `routes` |

Cấm coi tên key localStorage là credential — phải xác nhận qua chunk/chuỗi request.

**Lối ra = cổng B**: kết luận chốt vào `config.json`, và sửa `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — Quan sát API (tùy chọn)

Dùng `preload.js` trong OUTDIR để xác nhận tên key phiên, Authorization, URL API lồng nhau:

| Cấu hình | Sản phẩm |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | quan sát headers |
| `extractUrlsFromResponse: true` | API con trong response |
| `observe.storageReads/cookieReads: true` | bổ sung lại config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Mỗi vòng coverage export: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Phải đã qua cổng B; tuân thủ [Ranh giới và cấm](#ranh-giới-và-cấm-agent-bắt-buộc-đọc--vi-phạm-tức-là-vượt-phạm-vi) · chiến lược mock không credential.

`config.json` đặt `"runtimeMode": "depth" | "coverage" | "both"` (mẫu xem reference).

### Hook và stub (dùng chung cho depth + coverage)

| Tầng | Phạm vi | Mục đích |
|---|---|---|
| L1 chính xác | stub auth/quyền/bootstrap | qua xác thực màn hình đầu |
| L2 sửa âm | mọi JSON response | mã chưa đăng nhập → thành công |
| L3 dự phòng | `/api`... không trúng L1 | body thành công rỗng, mở rộng UI |

- **depth**: fake auth + `forward` sửa mã nghiệp vụ + `stubs`; duyệt `routes` (hash/history); sản phẩm `runtime_api.json`
- **coverage**: inject `preload.js` lúc **document-start** (CDP `addScriptToEvaluateOnNewDocument` hoặc Userscript)

Kiểm chứng: `window.__API_RECON_PRELOAD__` tồn tại; path nghiệp vụ không nhảy về `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — Liệt kê động coverage (bắt buộc)

1. Điều hướng chính/thanh bên — click từng mục, chờ mạng 1–3s
2. Tab — `role=tab`, `.ant-tabs-tab`
3. Bảng — xem/chỉnh sửa/chi tiết ở dòng đầu
4. Thanh công cụ — export, lọc, tạo mới (**tránh xóa không thể hoàn tác**)
5. Mỗi khi vào module — gộp API/route
6. SPA — với path chưa bao phủ trong `routes.txt` dùng `pushState` có kiểm soát (MPA bị cấm)

**Ma trận kích hoạt tham số** (bắt buộc): mỗi module ghi một lần theo loại thao tác, **diff nhiều mẫu**:

| Thao tác | Tham số thường xuất hiện thêm |
|---|---|
| Màn hình đầu của danh sách | phân trang + lọc mặc định |
| Bấm tìm kiếm | keyword, filter |
| Lọc nâng cao | thêm optional |
| Tạo mới/chỉnh sửa | entity đầy đủ |
| Hàng loạt/export/sắp xếp | `ids[]`, `exportType`, `sortField` |

**Dưới stub, outbound body/headers vẫn là thật** — lấy request làm chuẩn. Ghi lại → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + preload lúc document-start
- **React**: `routes.txt` + click thanh bên + `pushState`
- **both**: 3a depth trước, rồi 3b coverage

---

## Phase 4 — Khôi phục cây quyền

**Kích hoạt**: trang module trắng / mỗi route chỉ có bootstrap (như locale) → chưa qua cửa nội dung.

| Hiện tượng | Ý nghĩa |
|---|---|
| Vào vỏ thành công | Cửa render + cửa interceptor đã qua |
| Thanh bên thiếu mục/click trắng | shape của stub hoặc mã quyền chưa đủ |
| API mỗi route giống nhau và rất ít | `v-if permission` chưa vượt qua |
| `routes.txt` ít hơn nhiều so với bundle | phải bổ sung từ module auth |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Chuỗi điển hình: `role_permissions` (flat codes) + `permissions/all` (tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Sản phẩm trung gian: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

Kiểm tra stub: `response_code` tầng ngoài khớp với cửa interceptor; flat codes khớp với tree; `routes` bao phủ toàn bộ link của `route_map`.

Sau khi cập nhật `config.json` **chạy lại Phase 3**. SPA lớn có thể chỉnh `waitUntil`, `routeTimeout`, `perRouteMs` (xem mục A3/I của reference).

---

## Phase 5 — Gộp và báo cáo

### Bảng sản phẩm

| File | Giai đoạn | Nội dung |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | bundle tĩnh và path |
| `param_candidates.json` | 1b | ứng viên trường tham số tĩnh |
| `config.json` | 2 | ba cánh cửa + cấu hình runtime |
| `runtime_api.json` | 3a | ghi chi tiết depth (gồm WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | nhiều mẫu, nhật ký click, detail |
| `route_map.json`... | 4 | file trung gian cây quyền (nếu chạy) |
| `params_merged.json` | 5 | trường tham số đã gộp + độ tin cậy |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | route, API, params, điểm chức năng, giới hạn |
| **insert_assets** | 5 | ghi tất cả tài sản dịch vụ, endpoint vào kho tài sản |

### 5b — Gộp tham số

Diff từ `param_samples.json`, **không có script gộp dùng chung**. Quy tắc độ tin cậy xem reference J7 (cao/trung bình/thấp/chờ kích hoạt).

### 5c — Suy ngược từ lỗi

Trong phạm vi ủy quyền có thể gửi request thiếu dữ liệu để đọc 400 (**thuộc recon tham số, không phải test lỗ hổng**): `field 'x' is required`, lỗi enum... Chú ý bao bọc `data`, `variables`, `bizData` trước khi mã hóa.

Báo cáo phải ghi rõ: runtimeMode, số API tĩnh/runtime, độ tin cậy tham số, module chưa bao phủ, tóm tắt `CHANGES.md` so với script tham khảo.

Cấu trúc gợi ý của `site_map.json`:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

Thêm trường và công thức grep xem [reference.md](reference.md).

---

## Giải thích chung

- **Độc lập framework**: webpack/Vite/Angular lazy load dùng cùng phương pháp
- **Truyền**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web nằm ngoài phạm vi
- **SSR**: fetch phía client ghi được; RSC/Server Actions không liệt kê hết được
- **Điểm mù**: JSVMP, WASM, kiểm tra mạnh HMAC/mTLS → tĩnh + ghi chú giới hạn
- **Điểm mù tham số**: liên động điều kiện, hidden params, đóng gói WASM → "chờ kích hoạt"/"không tới được"
- **Tĩnh là lưới an toàn**: khi runtime bị chặn, tĩnh vẫn liệt kê được endpoint

---

## Tài nguyên bổ sung

- Công thức grep, mẫu `config.json`, xử lý sự cố, Hook, mục J đảo ngược tham số, mẫu site_map: **[reference.md](reference.md)**
- Đường dẫn script tham khảo xem bảng [Script và cổng kiểm soát](#script-và-cổng-kiểm-soát)
