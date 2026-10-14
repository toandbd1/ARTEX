# api-recon — Sổ tay tham khảo

Công thức grep, mẫu `config.json` và xử lý sự cố. Mọi grep đều chạy trên thư mục `js/`. Khi bundle là một dòng duy nhất có thể chạy trước `js-beautify` hoặc `sed 's/}/}\n/g'`; thường thì raw grep kèm cửa sổ ngữ cảnh là đủ.

## Giải thích script

Mọi file trong `scripts/` đều là **mẫu tham khảo**, trước khi chạy phải điều chỉnh theo site mục tiêu. Các điểm thay đổi điển hình:

| Script | Mục thường cần điều chỉnh |
|---|---|
| `harvest_static.py` | regex endpoint, parse manifest webpack/Vite, publicPath của micro-frontend, thử lại/đồng thời |
| `runtime_harvest.js` | tên trường và giá trị thành công của neutralize, rule match và cấu trúc body của stub, nguồn routes, ghi WS, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, có bật L3 không, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` các link phá hoại, cookie, depth/max, lọc cùng tên miền |
| `extract_route_map.py` | regex `routeMap` / `routeLink`, mẫu đặt tên KEY |
| `build_perm_tree.py` | parse `userRouteAuth`, heuristic phân tầng `ROOTS`/`PREFIX_PARENT`, tên trường tầng ngoài của stub |
| `config.json` | điểm vào thống nhất cho tất cả tham số riêng của từng site ở trên |

File sau khi điều chỉnh nên đặt trong thư mục làm việc của tác vụ (vd `recon/`), trong báo cáo ghi rõ các thay đổi cụ thể so với script tham khảo.

---

## A. Đảo ngược ba cánh cửa

### A1. Cửa render — "Làm sao biết đã đăng nhập?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Tìm chuỗi `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))`, xác định **key lưu trữ**, **container** (Cookie vs localStorage), **mã hóa**:

| Mã hóa | Cách forge trong config |
|---|---|
| Chuỗi thuần / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | JWT không chữ ký/`alg:none`, hoặc ký bằng khóa trong bundle |
| Mã hóa (SM2/AES/RSA) | tìm khóa hardcode; cửa render chỉ cần blob giải mã được thì forge được; nếu không thì dùng tĩnh dự phòng |

→ Ghi vào `cookies` / `localStorage`.

### A2. Cửa interceptor — "Cái gì kích hoạt nhảy /login?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Xác định: **tên trường**, **giá trị thành công** (thường `0` hoặc `200`), **giá trị thất bại kích hoạt chuyển hướng**. Kiểm chứng bằng junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ Ghi vào `neutralize.fields` + `neutralize.success`.

### A3. Cửa nội dung — "Menu/quyền lấy từ đâu?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**Dữ liệu hai tầng** (backend doanh nghiệp thường gặp):

| API | payload điển hình | Bên tiêu thụ |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | route guard, ACL cấp nút |
| `.../permissions/all` | `tree[{ code, position, children }]` | render menu thanh bên |
| `userRouteAuth` trong bundle | `{ CODE: { url, name? } }` | code → path frontend |
| `routeMap` trong bundle | `{ KEY: { name, link } }` | phân giải alias (webpack `o.DASHBOARD`) |

Đọc code bên tiêu thụ để xác nhận: `getResultTree(tree, permissions)` lọc thế nào, `v-if` / `hasAuth(code)` kiểm tra trường nào.

**Forge thủ công** (site nhỏ): dựng permissive payload → `stubs`.

**Khôi phục cây quyền đầy đủ** (site lớn, thanh bên/module con vẫn trắng): xem **mục I**.

---

## B. Mẫu config.json

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Giải thích trường:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), `both`
- Tiền tố `cookies[].value`: `b64json:` → base64(JSON); `json:` → JSON nguyên bản; không tiền tố → chữ nghĩa đen
- `forward: true` chuyển tiếp request thật và viết lại trường mã; `false` stub hoàn toàn offline
- `mockTier`: các tầng preload bật ở chế độ coverage, vd `L1+L2`, `L1+L2+L3`
- `routes` lấy từ `routes.txt`; sau khi forge menu, harness tự bổ sung `<a href>`
- `captureResponses` / `recordWs` chỉ có hiệu lực ở chế độ depth
- `waitUntil`: SPA lớn dùng `domcontentloaded`, tránh `networkidle2` bị treo
- `routeTimeout`: timeout `page.goto` của từng route (mili giây)
- `proxy`: `--proxy-server` của Puppeteer; cũng có thể đặt `HTTP_PROXY` / `HTTPS_PROXY`

### B1. Mẫu stub đôi (role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

Tên trường tầng ngoài (`response_code` / `code` / `data`) phải khớp với cửa interceptor ở A2; `permissions` phải bao phủ mọi leaf code trong tree.

---

## C. Chế độ coverage: cấu hình preload

Sửa đối tượng `CONFIG` ở đầu `scripts/preload.js`, hoặc thay thế trước khi inject qua CDP:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* giống stubs trong config.json */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Kiểm chứng: `window.__API_RECON_PRELOAD__ === true` và pathname ổn định.

Export kết quả ghi lại:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. Khả năng Hook của preload / runtime

Các khả năng Hook trình duyệt tích hợp của preload (coverage) và runtime_harvest (depth) cùng phạm vi bao phủ:

| Khả năng Hook | Giá trị với phát hiện API | Bao phủ |
|---|---|---|
| Hook fetch / XHR.open | ghi URL/method của request | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | phát hiện header như Authorization | ✅ `observe.xhrHeaders` |
| Hook đọc localStorage/cookie | xác nhận tên key phiên | ⚠️ tùy chọn `observe.storageReads/cookieReads` |
| Lấy route của Vue | bổ sung frontendRoutes | ✅ `__API_RECON_ROUTES__` (route đã tải) |
| Vô hiệu hóa Vue route guard / chặn chuyển hướng đăng nhập | mở rộng module để kích hoạt API | ✅ `neutralizeVueRouter` + vô hiệu hóa chuyển hướng gốc |
| Lấy route của React | bổ sung route | ⚠️ tĩnh + click; không có Hook chuyên dụng |
| Chặn chuyển hướng trang (path đăng nhập) | giữ lại trang để phân tích | ⚠️ chỉ chặn path đăng nhập, tránh chặn điều hướng nghiệp vụ |
| Hook thư viện mã hóa (CryptoJS/SM...) | tham số mã hóa → API body plaintext | ❌ phải Hook thủ công tham số đầu vào hàm mã hóa; kết luận ghi vào config |
| bypass anti-debug | nếu không runtime không ghi được API | ❌ phải xử lý thủ công; tĩnh vẫn dùng được |

---

## E. Regex trích endpoint (khi tĩnh quá ít)

Nới rộng trong `extract_endpoints` của `harvest_static.py`, hoặc thủ công:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Xử lý sự cố

| Hiện tượng | Nguyên nhân → xử lý |
|---|---|
| API tĩnh rất ít | phương ngôn endpoint không khớp → nới rộng regex (mục D) |
| Số chunk ≪ manifest | chunk chỉ có CSS hoặc chưa triển khai; 404 đã thử lại |
| runtime vẫn hiển thị trang đăng nhập | cửa render sai → kiểm tra lại A1: tên key, container, mã hóa, domain |
| Vào được vỏ nhưng module trắng | cửa nội dung → forge menu (A3); path `routes` có thể sai |
| Mỗi route chỉ có bootstrap/locale | mã quyền chưa đủ → khôi phục cây quyền ở mục I; kiểm tra stub đôi `role_permissions` + `permissions/all` |
| Thanh bên có mục nhưng trang con trắng | tree thiếu node trung gian hoặc code không khớp `userRouteAuth` |
| API nào cũng nhảy đăng nhập | cửa interceptor → xác nhận `neutralize`; trường lồng nhau cần mở rộng logic walk |
| Frame WS = 0 | cần tương tác người dùng mới subscribe; tăng `perRouteMs` |
| Response body rỗng | chỉ khi `forward: true` mới có response thật |
| Thiếu Chromium | cài chromium hoặc đặt `config.chromium` / `CHROMIUM` |
| Mock nhiều vẫn về đăng nhập | Hook quá muộn hoặc thiếu setter `location.href` → document-start + preload |
| Danh sách toàn rỗng | mảng rỗng của L3 là bình thường; tiếp tục click Tab/cài đặt/chi tiết |
| Nhầm Redux action thành route | lọc các path nội bộ chứa get/set/change/clear/toggle/upload |
| Vue vẫn nhảy đăng nhập | preload không phải document-start → đổi thời điểm inject; hoặc khi `neutralizeVueRouter: false` thì tự dọn guard |
| Response có URL nhưng không vào log | bật `extractUrlsFromResponse`; hoặc trích thủ công từ `__API_RECON_DETAIL__` |
| Không biết tên header Authorization | bật `observe.xhrHeaders` hoặc xem header request bằng DevTools |
| runtime cực chậm / timeout | đổi `waitUntil: domcontentloaded`; giảm `routeTimeout`; đừng dùng `networkidle2` |
| Kết nối proxy thất bại | kiểm tra `proxy` / biến môi trường; cổng proxy của Puppeteer và curl phải giống nhau |

---

## G. Mục tiêu hardened

Khi server kiểm tra phiên từng bước (cookie ký không forge được, menu render phía server không stub được), runtime sẽ kẹt ở lớp vỏ. Hành vi dự kiến:

- **Tĩnh đủ để liệt kê endpoint** — path module nằm trong code
- Nếu ủy quyền cho phép, dùng **phiên thật** chạy cùng harness: `forward: true`, không cần neutralize, ghi lại methods/params/responses thật

---

## H. Checklist một tác vụ

1. Xác nhận phạm vi ủy quyền
2. **Đọc** `scripts/harvest_static.py` → điều chỉnh theo mục tiêu → chạy → xem `api_static.txt`, `routes.txt`
3. **Phase 1b**: mở rộng cửa sổ quanh điểm neo path + tầng binding → `param_candidates.json` (mục J)
4. Đảo ngược A1/A2/A3 → viết `config.json` riêng cho site
5. **Đọc và điều chỉnh** `runtime_harvest.js` / `preload.js` rồi mới chạy
6. `runtimeMode=depth`: `npm install` → chạy script harvest đã điều chỉnh
7. `runtimeMode=coverage/both`: inject preload đã điều chỉnh lúc document-start → liệt kê động bằng browser MCP + **ma trận kích hoạt tham số**
8. Module không render → **khôi phục cây quyền ở mục I** → patch stubs → chạy lại
9. Diff nhiều mẫu tham số + suy ngược từ lỗi → `params_merged.json`
10. Gộp → `site_map.json` + `api_merged.txt`, ghi chú trung thực về bao phủ, khoảng trống và điểm sửa script

---

## I. Khôi phục cây quyền (đào sâu Phase 4)

Dùng khi forge `menus: [{ path, show: true }]` đơn giản không hiệu quả, module con vẫn không mount.

### I1. Định vị module auth

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Ghi lại: **path API quyền**, **tên trường response**, **tên file chunk tiêu thụ**.

### I2. Trích routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# tạo ra recon/route_map.json
```

Nếu `[!] no routeMap pattern found`: nới rộng regex trong `extract_route_map.py`, hoặc grep thủ công:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Dựng cây quyền + stub

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Logic của script:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (gồm alias webpack `He=o.DASHBOARD`)
2. Dùng `route_map.json` phân giải alias → path thật
3. Suy ra parent theo tiền tố code (`MONITOR_ALERT` → `MONITOR`)
4. Xuất `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. Khi có `--config` tự ghi vào `stubs` của `config.json` và mở rộng `routes`

**Điều chỉnh theo mục tiêu** (ở đầu script):
- `DEFAULT_ROOTS`: danh sách code module cấp cao nhất
- `DEFAULT_PREFIX_PARENT`: ánh xạ `PREFIX_` → parent
- `DEFAULT_EXTRA_PARENT`: node mồ côi không theo quan hệ tiền tố

### I4. Kiểm tra tính nhất quán của stub

```bash
# số permissions nên ≈ số mục userRouteAuth
wc -l recon/perm_codes_all.txt
# routes phải bao phủ toàn bộ link của route_map
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Chạy lại runtime và đối chiếu

```bash
node recon/runtime_harvest.js recon/config.json
# đối chiếu số dòng runtime_api.json trước/sau forge; kiểm tra /attack, /asset... đã xuất hiện API module chưa
```

| Trước forge | Sau forge (thành công) |
|---|---|
| Mỗi route cùng 3–5 dòng bootstrap | Các route khác nhau kích hoạt API module khác nhau |
| Chỉ có `/api/locale/language` | Xuất hiện endpoint module `/api/web/...` |
| `routes.txt` vài route | `routes` 80–110+ lấy từ route_map |

### I6. Khi vẫn thất bại

- **Chế độ coverage**: click thanh bên + Tab, permission gating có thể chỉ request sau tương tác
- **Trường stub**: đối chiếu nesting của API thật (curl + session thật) với stub
- **Guard bổ sung**: grep các kiểm tra cấp nút như `hasPermission|checkRole|func.`, mở rộng `role_permissions.permissions`
- **Tĩnh dự phòng**: path API module vẫn trong `api_static.txt`, runtime chỉ bổ sung METHOD/body; tham số giữ `param_candidates.json` + mẫu đã ghi

---

## J. Đảo ngược tham số (Phase 1b / 5b / 5c)

**Phương pháp luận, không phải script dùng chung.** Tìm path dùng regex; tìm tham số dùng mở rộng cửa sổ quanh điểm neo + chuỗi binding UI + diff nhiều mẫu + suy ngược từ lỗi.

### J1. Mở rộng cửa sổ quanh điểm neo — từ path tìm đối tượng đóng gói

```bash
# lấy path đã biết ở Phase 1 làm neo
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Tầng bao bọc và hình thức truyền

```bash
# axios / request thống nhất
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# tham số đường dẫn
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Cửa kiểm tra — bắt buộc / định dạng / enum

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Tầng binding — form → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

Runtime bổ sung: DevTools → Network → request → **Initiator** (call stack) lần ngược từ `fetch`/`send` lên hàm đóng gói.

### J5. Tham số mã hóa

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**Đừng đoán trường trên ciphertext** — Hook **tham số đầu vào** của hàm mã hóa, ghi plaintext payload trước khi mã hóa; kết luận ghi vào `config.json` / `param_candidates.json`.

### J6. Ma trận kích hoạt tham số (bắt buộc ở Phase 3)

Mỗi module ghi một lần theo thao tác, diff body/query của request:

| Thao tác | Quan tâm |
|---|---|
| Màn hình đầu danh sách | giá trị mặc định phân trang |
| Tìm kiếm | keyword, filters |
| Lọc nâng cao | trường optional |
| Tạo mới/chỉnh sửa | entity đầy đủ |
| Hàng loạt/export | `ids[]`, `exportType` |
| Sắp xếp/lật trang | `sortField`, `order` |

Sản phẩm `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Quy tắc độ tin cậy

| Độ tin cậy | Điều kiện |
|---|---|
| **Cao** | callsite tĩnh + ≥2 mẫu runtime khớp nhau |
| **Trung bình** | chỉ tĩnh, hoặc chỉ 1 lần runtime |
| **Thấp** | suy ngược từ response/lỗi, chưa kiểm chứng lần hai |
| **Chờ kích hoạt** | trường đã biết từ tĩnh, UI/quyền chưa chạy tới |

### J8. Cấu hình nhanh theo kịch bản

| Kịch bản | Thứ tự |
|---|---|
| Trang danh sách REST | J1 đối tượng đóng gói → J6 bốn lần diff → J3 rules |
| Form tạo mới/chỉnh sửa | J3 Form name → J4 chuỗi submit → runtime submit + cố ý để trống xem 400 |
| GraphQL | J2 khai báo variables → runtime ghi variables của từng operation |
| Body mã hóa | J5 Hook tham số đầu vào → trường trước mã hóa chính là params thật |

### J9. Ánh xạ với các giai đoạn api-recon

| api-recon | recon tham số |
|---|---|
| Phase 1 tĩnh | J1 mở rộng cửa sổ quanh điểm neo |
| Phase 2 A2 interceptor | trường inject toàn cục (tenantId, sign) |
| Phase 3 runtime | J6 ma trận kích hoạt + `param_samples.json` |
| Phase 4 cây quyền | form các module khác nhau → đủ quyền mới kích hoạt đủ trường |
| Phase 5 gộp | `params_merged.json` + độ tin cậy; không dùng một mẫu để định bắt buộc |

### J10. Xử lý sự cố

| Hiện tượng | Xử lý |
|---|---|
| Tĩnh có tên trường nhưng runtime chưa từng xuất hiện | ghi chú "chờ kích hoạt"; bổ sung cây quyền / click lọc nâng cao / duyệt từng option của select liên động |
| Cùng path nhưng shape body khác nhau | bình thường — ghi riêng theo `action`, đừng gộp cứng schema |
| Stub response giả nhưng muốn xem params | **xem request outbound** body/headers, đừng suy ngược từ stub response |
| 400 báo trường lồng nhau | chú ý lớp bao bọc `data`/`bizData`/`variables` |
| GraphQL chỉ thấy tên operation | mở rộng JSON `variables`; tĩnh tìm `$var: Type` |

---
