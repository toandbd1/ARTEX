package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Repo là nguồn phát hành. Cố định thay vì làm thành mục cấu hình: nguồn cập nhật
// mà cấu hình được thì bất kỳ ai sửa được cấu hình đều có một kênh thực thi mã từ xa;
// với một nền tảng kiểm thử xâm nhập, cánh cửa này không thể mở.
const Repo = "Hinln/ARTEX"

// GitHubTokenEnv chỉ do tiến trình backend đọc, không truyền token qua frontend hay
// API cấu hình.
const GitHubTokenEnv = "ARTEX_UPDATE_GITHUB_TOKEN"

// latestURL là API "bản chính thức mới nhất" của GitHub. Nó tự động bỏ qua
// prerelease và draft.
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts giới hạn các tên miền mà chuỗi nâng cấp được truy cập. Kết hợp với
// checkRedirect bên dưới, bất kỳ bước nhảy nào bị chuyển hướng tới host ngoài danh
// sách đều thất bại ngay — đây là cổng kiểm đầu tiên chống đầu độc DNS / kẻ trung
// gian thay binary, cổng thứ hai là đối chiếu SHA256SUMS.
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // object storage nơi asset của release thực sự đặt
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// Release là các trường chúng ta quan tâm trong GitHub Release.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset là một file đính kèm trên Release.
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	APIURL string `json:"url"`
	Size   int64  `json:"size"`
}

// File đính kèm của repo riêng tư cần tải xuống qua Release Asset API kèm xác thực.
func (a Asset) downloadURL() string {
	if a.APIURL != "" {
		return a.APIURL
	}
	return a.URL
}

// setUpdateAuth chỉ gửi token tới HTTPS API của repo phát hành cố định. Khi file đính
// kèm chuyển hướng sang object storage của GitHub thì dùng URL có chữ ký, không được
// chuyển tiếp Authorization theo.
func setUpdateAuth(req *http.Request) {
	req.Header.Del("Authorization")
	u := req.URL
	if u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "api.github.com") ||
		(u.Port() != "" && u.Port() != "443") || u.User != nil ||
		!strings.HasPrefix(strings.ToLower(u.EscapedPath()), strings.ToLower("/repos/"+Repo+"/releases/")) {
		return
	}
	if token := strings.TrimSpace(os.Getenv(GitHubTokenEnv)); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// NewClient dựng một HTTP client chỉ chấp nhận tên miền GitHub. proxy rỗng thì kết nối trực tiếp.
//
// Cố ý không tái sử dụng Transport mặc định: chuỗi nâng cấp bắt buộc đi TLS và kiểm
// tra chứng chỉ, không thể bị ảnh hưởng bởi InsecureSkipVerify hay thứ tương tự được
// đặt ở nơi khác.
func NewClient(proxy string) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Minute, // tải xuống cả gói, không thể dùng timeout mức request mà chết đứng
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("quá nhiều lần chuyển hướng")
			}
			if err := checkURL(req.URL); err != nil {
				return err
			}
			setUpdateAuth(req)
			return nil
		},
	}
}

// checkURL bắt buộc https + danh sách trắng tên miền.
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("từ chối địa chỉ không phải HTTPS: %s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("từ chối tên miền không phải GitHub: %s", u.Hostname())
	}
	return nil
}

// FetchLatest truy vấn bản chính thức mới nhất.
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "artex-selfupdate")
	setUpdateAuth(req)

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("truy cập GitHub thất bại (có thể cấu hình proxy toàn cục trong cài đặt hệ thống): %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("xác thực GitHub thất bại, hãy kiểm tra biến môi trường backend %s", GitHubTokenEnv)
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("GitHub từ chối truy cập: hãy kiểm tra quyền đọc Contents của repo trên token hoặc hạn mức API")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("API GitHub đang bị giới hạn tốc độ, vui lòng thử lại sau")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("repo %s không truy cập được hoặc chưa phát hành bản chính thức; với repo riêng tư hãy đặt %s ở backend (cần quyền đọc Contents)", Repo, GitHubTokenEnv)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub trả về %d", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("phân tích Release thất bại: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("Release thiếu tag")
	}
	return &rel, nil
}

// AssetName trả về tên gói phát hành tương ứng nền tảng hiện tại, khớp với
// package_binary trong build.sh: artex-<phiên bản>-<os>-<arch>.zip (số phiên bản
// không có tiền tố v).
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset tìm asset theo tên trong Release.
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
