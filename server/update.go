package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// Mặt HTTP của cập nhật một chạm trên trang. Logic tải xuống/kiểm tra/thay thế thật sự nằm cả trong gói selfupdate,
// ở đây chỉ chịu trách nhiệm ranh giới xác thực, loại trừ tương tranh, phát tiến độ, và báo cho main biết "đã đến lúc thoát".
//
// Việc khởi động lại không do tiến trình này thực hiện: sau khi tạm lưu phiên bản mới, tiến trình thoát với selfupdate.ExitRestart,
// rồi script daemon (start.sh / start.bat, trong Docker là ENTRYPOINT) kéo lên lại.

// restartCh được đóng khi nâng cấp sẵn sàng hoặc khôi phục (rollback) hoàn tất, main nhận được thì thoát với ExitRestart.
var (
	restartOnce sync.Once
	restartCh   = make(chan struct{})
)

// RestartRequested trả về một channel được đóng khi "hãy thoát và để daemon kéo tôi lên lại".
func RestartRequested() <-chan struct{} { return restartCh }

func requestRestart() { restartOnce.Do(func() { close(restartCh) }) }

// bootState là kết luận của selfupdate.Bootstrap khi khởi động lần này (nâng cấp thành công / vừa khôi phục /
// phần tạm lưu bị loại bỏ), do main tiêm vào, để /api/update/check báo đúng cho frontend về kết cục của lần nâng cấp trước.
var (
	bootStateMu sync.Mutex
	bootState   selfupdate.State
)

// SetBootUpdateState được main gọi một lần khi khởi động.
func SetBootUpdateState(st selfupdate.State) {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	bootState = st
}

func bootUpdateState() selfupdate.State {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	return bootState
}

// releaseCache lưu kết quả truy vấn phiên bản mới nhất của GitHub.
//
// Gợi ý "có phiên bản mới" trên thanh trên sẽ truy vấn một lần mỗi lần tải toàn trang, mà GitHub API không xác thực chỉ cho
// 60 lần/IP/giờ —— nếu không cache, mở thêm vài tab hoặc refresh vài lần là cạn quota,
// đến khi thật sự muốn cập nhật lại không truy vấn được. Khi người dùng bấm "kiểm tra cập nhật" tường minh có thể dùng force để bỏ qua cache.
type releaseCache struct {
	mu  sync.Mutex
	rel *selfupdate.Release
	err error
	at  time.Time
	// fetch là hàm lấy dữ liệu, chỉ là điểm tiêm cho test; khi nil thì đi truy vấn GitHub thật.
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// Kết quả thất bại cũng cache một lát, nếu không khi GitHub không truy cập được thì mỗi lần tải trang đều phải chờ timeout;
	// nhưng TTL phải ngắn, để mạng phục hồi là nhanh chóng tự lành.
	releaseErrTTL = 2 * time.Minute
	// Timeout dùng cho truy vấn. Timeout 30 phút của NewClient là để tải trọn gói, truy vấn phiên bản không thể chờ lâu như vậy.
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get trả về Release mới nhất, trúng cache thì không truy cập mạng.
//
// Giữ lock trong suốt quá trình lấy dữ liệu: các request đồng thời sẽ xếp hàng chờ kết quả của cùng một truy vấn, thay vì tự gọi GitHub từng cái
// (lúc trang vừa tải mà nhiều tab cùng truy vấn, chính là thời điểm dễ bị giới hạn tốc độ nhất).
func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force {
		ttl := releaseTTL
		if c.err != nil {
			ttl = releaseErrTTL
		}
		if !c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch := c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatest
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err := fetch(ctx, client)
	// Request bị hủy (người dùng đóng tab) không có nghĩa GitHub có vấn đề, đừng ghi nó vào cache,
	// nếu không khách truy cập kế tiếp sẽ nhận một lỗi "đã hủy" khó hiểu.
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}

// updateProgress là một bản tin tiến độ đẩy cho frontend.
type updateProgress struct {
	Phase   selfupdate.Phase `json:"phase"`
	Percent int              `json:"percent"` // chỉ có ý nghĩa ở giai đoạn tải xuống; còn lại là -1
	Message string           `json:"message"`
	Version string           `json:"version,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// updateHub giữ tiến độ của một lần nâng cấp và phát cho các subscriber SSE.
//
// running đồng thời đóng vai trò mutex: trong lúc nâng cấp nếu lại POST /api/update/apply thì trả 409,
// tránh hai goroutine cùng ghi vào một file artex.new.
type updateHub struct {
	mu      sync.Mutex
	running bool
	cur     updateProgress
	subs    map[chan updateProgress]struct{}
}

var updHub = &updateHub{
	cur:  updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1},
	subs: map[chan updateProgress]struct{}{},
}

// begin chiếm quyền nâng cấp, nếu đang diễn ra thì trả về false.
func (h *updateHub) begin(version string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	h.cur = updateProgress{Phase: selfupdate.PhaseDownload, Percent: 0, Message: "Đang chuẩn bị…", Version: version}
	h.fanout(h.cur)
	return true
}

// finish kết thúc một lần nâng cấp. err là nil nghĩa là tạm lưu thành công, chờ khởi động lại.
func (h *updateHub) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	if err != nil {
		h.cur = updateProgress{Phase: selfupdate.PhaseFailed, Percent: -1, Message: "Cập nhật thất bại", Error: err.Error(), Version: h.cur.Version}
	} else {
		h.cur = updateProgress{Phase: selfupdate.PhaseStaged, Percent: 100, Message: "Phiên bản mới đã sẵn sàng, đang khởi động lại…", Version: h.cur.Version}
	}
	h.fanout(h.cur)
}

func (h *updateHub) publish(ph selfupdate.Phase, pct int, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = updateProgress{Phase: ph, Percent: pct, Message: msg, Version: h.cur.Version}
	h.fanout(h.cur)
}

// fanout phải được gọi khi đang giữ h.mu. Channel của subscriber có buffer, đầy thì bỏ ——
// tiến độ là thông tin tạm thời có thể bỏ, tuyệt đối không thể để một kết nối SSE bị treo chặn cả quá trình nâng cấp.
func (h *updateHub) fanout(p updateProgress) {
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func (h *updateHub) snapshot() (updateProgress, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur, h.running
}

func (h *updateHub) subscribe() (<-chan updateProgress, func()) {
	ch := make(chan updateProgress, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// updateCheck truy vấn phiên bản chính thức mới nhất trên GitHub và so sánh với phiên bản hiện tại.
//
// Frontend cũng kết nối trực tiếp api.github.com (CORS của GitHub là *), nhưng **lấy giao diện này làm chuẩn**:
// việc tải xuống do backend thực hiện, chỉ khi backend truy cập được GitHub mới nói đến cập nhật. Trường hợp trình duyệt kết nối được
// mà máy chủ không kết nối được rất phổ biến (máy chủ ở mạng nội bộ, hoặc proxy chỉ cấu hình trên trình duyệt), lúc đó bấm cập nhật chắc chắn thất bại,
// thà báo lỗi đúng ngay từ bước kiểm tra.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion
	mode := "binary"
	if selfupdate.InDocker() {
		mode = "docker"
	}
	boot := bootUpdateState()
	out := map[string]any{
		"current":     current,
		"mode":        mode,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"has_backup":  selfupdate.HasBackup(),
		"repo":        selfupdate.Repo,
		"boot_notice": boot.Detail,
		"rolled_back": boot.RolledBack,
	}

	// Gợi ý thanh trên đi qua cache (mặc định); khi người dùng bấm "kiểm tra cập nhật" thì kèm force=1 để buộc về nguồn.
	force := r.URL.Query().Get("force") != ""
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, force)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}

	latest := rel.TagName
	out["latest"] = latest
	out["notes"] = rel.Body
	out["html_url"] = rel.HTMLURL
	if !rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Format(time.RFC3339)
	}

	asset := selfupdate.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	out["asset"] = asset
	if a, ok := rel.FindAsset(asset); ok {
		out["asset_available"] = true
		out["size"] = a.Size
	} else {
		out["asset_available"] = false
	}

	cmp, comparable := selfupdate.CompareVersions(current, latest)
	out["comparable"] = comparable
	out["has_update"] = comparable && cmp < 0
	if !comparable {
		// Build phát triển (dev / git describe có hậu tố) không có số phiên bản để so sánh. Cho qua chỉ sẽ
		// dùng bản chính thức ghi đè lên binary đang debug ở local, nên trực tiếp không cho cập nhật.
		out["reason"] = fmt.Sprintf("phiên bản hiện tại %q không phải bản phát hành chính thức, đã tắt cập nhật một chạm", current)
	}
	writeJSON(w, 200, out)
}

// updateApply tải xuống và tạm lưu phiên bản mới, xong thì để tiến trình thoát và giao cho script daemon khởi động lại.
//
// Trả về 202 ngay, công việc thật chạy trên goroutine nền: tải trọn gói có thể mất vài phút,
// treo trên request sẽ bị reverse proxy cắt vì timeout. Tiến độ đi qua /api/update/stream.
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	current := BuildVersion

	// Đi qua cache: đảm bảo cái được cài chính là phiên bản người dùng đã thấy và xác nhận trên giao diện.
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if !comparable {
		writeErr(w, 400, fmt.Sprintf("phiên bản hiện tại %q không phải bản phát hành chính thức, đã tắt cập nhật một chạm", current))
		return
	}
	if cmp >= 0 {
		writeErr(w, 400, fmt.Sprintf("hiện đã là phiên bản mới nhất %s", current))
		return
	}
	if !updHub.begin(rel.TagName) {
		writeErr(w, 409, "Đã có một tiến trình cập nhật đang chạy")
		return
	}

	go func() {
		// Cố ý dùng s.ctx thay vì ctx của request: HTTP response trả về là request kết thúc,
		// treo tải xuống lên nó sẽ bị hủy ngay.
		err := selfupdate.Stage(s.ctx, client, rel, current, func(ph selfupdate.Phase, pct int, msg string) {
			updHub.publish(ph, pct, msg)
		})
		updHub.finish(err)
		if err != nil {
			log.Printf("[update] cập nhật thất bại: %v", err)
			return
		}
		log.Printf("[update] %s → %s đã tạm lưu, sắp thoát để hoàn tất thay thế", current, rel.TagName)
		// Chừa một chút thời gian để đẩy bản tin tiến độ cuối cùng cho frontend, rồi kích hoạt thoát.
		time.Sleep(1500 * time.Millisecond)
		requestRestart()
	}()

	writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName})
}

// updateRollback chủ động quay về phiên bản trước (artex.old đã backup trước khi thay thế).
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	if _, running := updHub.snapshot(); running {
		writeErr(w, 409, "Đang có cập nhật diễn ra, không thể khôi phục")
		return
	}
	if err := selfupdate.Rollback(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[update] đã khôi phục thủ công về phiên bản trước, sắp thoát để hoàn tất chuyển đổi")
	writeJSON(w, 202, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		requestRestart()
	}()
}

// updateStream đẩy tiến độ cập nhật bằng SSE.
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := updHub.subscribe()
	defer unsub()

	send := func(p updateProgress) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// Gửi bổ sung trạng thái hiện tại trước, để sau khi refresh trang vẫn thấy ngay nâng cấp đang diễn ra.
	cur, _ := updHub.snapshot()
	send(cur)

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			send(p)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
