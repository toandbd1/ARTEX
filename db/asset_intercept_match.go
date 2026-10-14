package db

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Tầng khớp/thực thi của quy tắc chặn tài sản. asset_intercept.go chỉ chịu trách nhiệm
// lưu trữ quy tắc, ở đây chịu trách nhiệm khớp tên miền/IP/URL của "tài sản mục tiêu"
// với các quy tắc đang bật. Được các công cụ agent (add_intent, insert_assets) gọi
// trước khi phát ý đồ / chèn tài sản, nếu trúng thì từ chối.

// AssetInterceptKindLabel trả về nhãn tiếng Việt của kind, dùng cho thông báo giải thích gửi agent.
func AssetInterceptKindLabel(kind string) string {
	switch kind {
	case "exact_domain":
		return "tên miền (khớp tuyệt đối)"
	case "exact_ip":
		return "IP (khớp tuyệt đối)"
	case "exact_url":
		return "URL (khớp tuyệt đối)"
	case "fuzzy_domain":
		return "tên miền (mờ)"
	case "fuzzy_ip":
		return "IP (mờ)"
	case "fuzzy_url":
		return "URL (mờ)"
	case "cidr":
		return "dải mạng CIDR"
	}
	return kind
}

// Reason trả về lý do trúng dễ đọc, dạng: trúng quy tắc chặn tài sản [tên miền (mờ): .gov.cn] (ghi chú).
func (r AssetInterceptRule) Reason() string {
	s := fmt.Sprintf("trúng quy tắc chặn tài sản [%s: %s]", AssetInterceptKindLabel(r.Kind), r.Pattern)
	if note := strings.TrimSpace(r.Note); note != "" {
		s += " (" + note + ")"
	}
	return s
}

// matchOne xét một quy tắc đang bật có trúng chuỗi ứng viên tên miền/IP/URL đã cho không, trả về giá trị cụ thể trúng.
func matchOne(r AssetInterceptRule, domains, ips, urls []string) (string, bool) {
	p := strings.TrimSpace(r.Pattern)
	if p == "" {
		return "", false
	}
	switch r.Kind {
	case "exact_domain":
		for _, d := range domains {
			if strings.EqualFold(strings.TrimSpace(d), p) {
				return d, true
			}
		}
	case "exact_ip":
		for _, ip := range ips {
			if strings.TrimSpace(ip) == p {
				return ip, true
			}
		}
	case "exact_url":
		for _, u := range urls {
			if strings.TrimSpace(u) == p {
				return u, true
			}
		}
	case "fuzzy_domain":
		lp := strings.ToLower(p)
		for _, d := range domains {
			if d != "" && strings.Contains(strings.ToLower(d), lp) {
				return d, true
			}
		}
	case "fuzzy_ip":
		for _, ip := range ips {
			if ip != "" && strings.Contains(ip, p) {
				return ip, true
			}
		}
	case "fuzzy_url":
		lp := strings.ToLower(p)
		for _, u := range urls {
			if u != "" && strings.Contains(strings.ToLower(u), lp) {
				return u, true
			}
		}
	case "cidr":
		_, ipnet, err := net.ParseCIDR(p)
		if err != nil {
			return "", false
		}
		for _, ip := range ips {
			if pip := net.ParseIP(strings.TrimSpace(ip)); pip != nil && ipnet.Contains(pip) {
				return ip, true
			}
		}
	}
	return "", false
}

// MatchAssetInterceptRules trả về quy tắc đang bật đầu tiên trúng chuỗi ứng viên tên miền/IP/URL đã cho, và giá trị cụ thể trúng.
// Dùng cho insert_assets khớp với đầu vào thô (assetInputItem chưa ghi vào DB).
func MatchAssetInterceptRules(rules []AssetInterceptRule, domains, ips, urls []string) (AssetInterceptRule, string, bool) {
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if v, ok := matchOne(r, domains, ips, urls); ok {
			return r, v, true
		}
	}
	return AssetInterceptRule{}, "", false
}

// interceptCandidates trích xuất các chuỗi ứng viên tên miền/IP/URL của một tài sản đã ghi vào DB để khớp chặn.
// Host của URL được tách ra và phân loại, để tài sản dạng dịch vụ "chỉ có URL" cũng bị quy tắc tên miền/IP bắt trúng.
func (a *Asset) interceptCandidates() (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, a.Domain)
	add(&domains, a.RootDomain)
	for _, d := range a.BoundDomains {
		add(&domains, d)
	}
	add(&ips, a.IP)
	add(&urls, a.URL)
	if a.URL != "" {
		if u, err := url.Parse(a.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// InterceptLabel trả về định danh ngắn gọn của tài sản, dùng cho thông báo giải thích gửi agent.
func (a *Asset) InterceptLabel() string {
	var target string
	switch {
	case a.Domain != "":
		target = a.Domain
	case a.URL != "":
		target = a.URL
	case a.IP != "":
		target = a.IP
	default:
		target = fmt.Sprintf("#%d", a.ID)
	}
	return fmt.Sprintf("tài sản#%d[%s] %s", a.ID, a.Type, target)
}

// hasEnabledRule kiểm tra trong tập quy tắc có tồn tại quy tắc đang bật nào không.
func hasEnabledRule(rules []AssetInterceptRule) bool {
	for _, r := range rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// AssetGateDecision là kết quả phán định của cổng "chặn trước, cho phép sau" đối với một nhóm chuỗi ứng viên.
type AssetGateDecision struct {
	Allowed bool
	Reason  string // lý do bị từ chối (không gồm định danh tài sản); rỗng khi Allowed=true
}

// EvaluateAssetGate thực hiện phán định cổng cấp tác vụ:
//  1. Trúng bất kỳ blockRules nào đang bật → từ chối (lý do chặn).
//  2. Ngược lại, nếu allowRules có mục đang bật và không mục nào trúng → từ chối (không nằm trong phạm vi cho phép).
//  3. Ngược lại thì cho qua.
//
// Khi allowRules rỗng/không có mục nào bật, cổng cho phép không có hiệu lực (tức không bật danh sách trắng, cho qua tất cả),
// tránh việc "chưa cấu hình quy tắc cho phép" chặn hết mọi tài sản.
func EvaluateAssetGate(blockRules, allowRules []AssetInterceptRule, domains, ips, urls []string) AssetGateDecision {
	if rule, _, ok := MatchAssetInterceptRules(blockRules, domains, ips, urls); ok {
		return AssetGateDecision{Allowed: false, Reason: rule.Reason()}
	}
	if hasEnabledRule(allowRules) {
		if _, _, ok := MatchAssetInterceptRules(allowRules, domains, ips, urls); !ok {
			return AssetGateDecision{Allowed: false, Reason: "không nằm trong phạm vi cho phép (danh sách trắng) của tác vụ, không được phép kiểm thử"}
		}
	}
	return AssetGateDecision{Allowed: true}
}

// AssetInterceptHit mô tả một tài sản bị cổng từ chối (trúng chặn hoặc không nằm trong phạm vi cho phép).
type AssetInterceptHit struct {
	Asset  *Asset
	Reason string // lý do dễ đọc
}

// Describe trả về một mô tả dễ đọc: thông tin tài sản + lý do.
func (h AssetInterceptHit) Describe() string {
	return fmt.Sprintf("%s → %s", h.Asset.InterceptLabel(), h.Reason)
}

// ListAssetInterceptRules là bản chuyển tiếp của phương thức cùng tên trên *DB, để phía gọi
// chỉ giữ AssetStore (như công cụ agent) cũng đọc được quy tắc.
func (s *AssetStore) ListAssetInterceptRules() ([]AssetInterceptRule, error) {
	return s.db.ListAssetInterceptRules()
}

// CheckAssetsIntercept nạp tài sản theo id, thực hiện phán định cổng "chặn trước, cho phép sau" từng cái một,
// trả về tất cả tài sản bị từ chối. Quy tắc chặn = toàn cục ∪ block cấp tác vụ; quy tắc cho phép = allow cấp tác vụ (chỉ tác vụ này).
// Không có id thì trả về nhanh. Dùng GetByIDs toàn cục (không bị lọc theo phạm vi tác vụ) để đảm bảo chặn không bị scope làm suy yếu.
func (s *AssetStore) CheckAssetsIntercept(taskID int64, ids []int64) ([]AssetInterceptHit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	blockRules, err := s.db.ListAssetInterceptRules()
	if err != nil {
		return nil, err
	}
	var allowRules []AssetInterceptRule
	if taskID > 0 {
		tb, ta, err := s.TaskInterceptRulesSplit(taskID)
		if err != nil {
			return nil, err
		}
		blockRules = append(blockRules, tb...)
		allowRules = ta
	}
	// Không có quy tắc chặn, cũng không có quy tắc cho phép nào bật → không cần phán định, cho qua tất cả.
	if len(blockRules) == 0 && !hasEnabledRule(allowRules) {
		return nil, nil
	}
	assets, err := s.GetByIDs(ids)
	if err != nil {
		return nil, err
	}
	var hits []AssetInterceptHit
	for _, a := range assets {
		domains, ips, urls := a.interceptCandidates()
		if d := EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
			hits = append(hits, AssetInterceptHit{Asset: a, Reason: d.Reason})
		}
	}
	return hits, nil
}
