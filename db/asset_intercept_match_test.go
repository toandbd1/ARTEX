package db

import "testing"

func rule(kind, pattern string, enabled bool) AssetInterceptRule {
	return AssetInterceptRule{Kind: kind, Pattern: pattern, Enabled: enabled}
}

func TestMatchAssetInterceptRules(t *testing.T) {
	cases := []struct {
		name    string
		rules   []AssetInterceptRule
		domains []string
		ips     []string
		urls    []string
		want    bool
		wantVal string
	}{
		{"tên miền chính phủ mờ tích hợp sẵn trúng", []AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)},
			[]string{"www.beijing.gov.cn"}, nil, nil, true, "www.beijing.gov.cn"},
		{"tên miền giáo dục mờ trúng", []AssetInterceptRule{rule("fuzzy_domain", ".edu", true)},
			[]string{"mit.edu"}, nil, nil, true, "mit.edu"},
		{"tên miền khớp tuyệt đối trúng, không phân biệt hoa thường", []AssetInterceptRule{rule("exact_domain", "Example.com", true)},
			[]string{"example.com"}, nil, nil, true, "example.com"},
		{"tên miền khớp tuyệt đối không trúng tên miền con", []AssetInterceptRule{rule("exact_domain", "example.com", true)},
			[]string{"a.example.com"}, nil, nil, false, ""},
		{"IP khớp tuyệt đối trúng", []AssetInterceptRule{rule("exact_ip", "203.0.113.5", true)},
			nil, []string{"203.0.113.5"}, nil, true, "203.0.113.5"},
		{"tiền tố IP mờ trúng", []AssetInterceptRule{rule("fuzzy_ip", "203.0.113.", true)},
			nil, []string{"203.0.113.99"}, nil, true, "203.0.113.99"},
		{"CIDR trúng", []AssetInterceptRule{rule("cidr", "192.168.0.0/16", true)},
			nil, []string{"192.168.5.20"}, nil, true, "192.168.5.20"},
		{"CIDR không trúng", []AssetInterceptRule{rule("cidr", "192.168.0.0/16", true)},
			nil, []string{"10.0.0.1"}, nil, false, ""},
		{"URL khớp tuyệt đối trúng", []AssetInterceptRule{rule("exact_url", "https://a.gov.cn/login", true)},
			nil, nil, []string{"https://a.gov.cn/login"}, true, "https://a.gov.cn/login"},
		{"URL mờ trúng đường dẫn", []AssetInterceptRule{rule("fuzzy_url", "/admin", true)},
			nil, nil, []string{"https://x.com/admin/panel"}, true, "https://x.com/admin/panel"},
		{"quy tắc đã tắt không trúng", []AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", false)},
			[]string{"www.gov.cn"}, nil, nil, false, ""},
		{"không có quy tắc không trúng", nil, []string{"www.gov.cn"}, nil, nil, false, ""},
		{"pattern rỗng không trúng", []AssetInterceptRule{rule("fuzzy_domain", "  ", true)},
			[]string{"www.gov.cn"}, nil, nil, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, val, ok := MatchAssetInterceptRules(c.rules, c.domains, c.ips, c.urls)
			if ok != c.want {
				t.Fatalf("trúng = %v, mong đợi %v (rule=%+v)", ok, c.want, r)
			}
			if ok && val != c.wantVal {
				t.Fatalf("giá trị trúng = %q, mong đợi %q", val, c.wantVal)
			}
		})
	}
}

func TestEvaluateAssetGate(t *testing.T) {
	block := []AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)}
	allow := []AssetInterceptRule{rule("fuzzy_domain", "example.com", true)}

	// 1. Trúng quy tắc chặn → từ chối (ưu tiên lý do chặn).
	if d := EvaluateAssetGate(block, allow, []string{"www.gov.cn"}, nil, nil); d.Allowed {
		t.Fatal("trúng quy tắc chặn phải bị từ chối")
	}

	// 2. Không trúng chặn, có quy tắc cho phép nhưng không trúng → từ chối (không được phép).
	d := EvaluateAssetGate(block, allow, []string{"foo.other.com"}, nil, nil)
	if d.Allowed {
		t.Fatal("có danh sách trắng mà không trúng phải bị từ chối")
	}
	if d.Reason == "" {
		t.Fatal("từ chối phải kèm lý do")
	}

	// 3. Không trúng chặn, trúng quy tắc cho phép → cho qua.
	if d := EvaluateAssetGate(block, allow, []string{"api.example.com"}, nil, nil); !d.Allowed {
		t.Fatal("trúng danh sách trắng phải được cho qua")
	}

	// 4. Không có quy tắc cho phép (chưa bật danh sách trắng) → không trúng chặn thì cho qua.
	if d := EvaluateAssetGate(block, nil, []string{"foo.other.com"}, nil, nil); !d.Allowed {
		t.Fatal("khi không có danh sách trắng, không trúng chặn phải được cho qua")
	}

	// 5. Tất cả quy tắc cho phép đều tắt → xem như chưa bật danh sách trắng, cho qua.
	disabledAllow := []AssetInterceptRule{rule("fuzzy_domain", "example.com", false)}
	if d := EvaluateAssetGate(nil, disabledAllow, []string{"foo.other.com"}, nil, nil); !d.Allowed {
		t.Fatal("khi danh sách trắng tắt hết phải được cho qua")
	}

	// 6. Chặn ưu tiên hơn cho phép: cùng một mục tiêu vừa trúng chặn vừa trúng cho phép → từ chối.
	if d := EvaluateAssetGate(
		[]AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)},
		[]AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)},
		[]string{"www.gov.cn"}, nil, nil,
	); d.Allowed {
		t.Fatal("chặn phải được ưu tiên hơn cho phép")
	}
}

func TestAssetInterceptCandidates(t *testing.T) {
	// Tài sản dịch vụ chỉ có URL: host phải được tách ra và xếp vào ứng viên tên miền, nhờ đó bị fuzzy_domain bắt trúng.
	a := &Asset{Type: "service", URL: "https://portal.beijing.gov.cn:8443/app"}
	domains, _, urls := a.interceptCandidates()
	if len(urls) != 1 || urls[0] != a.URL {
		t.Fatalf("urls = %v", urls)
	}
	found := false
	for _, d := range domains {
		if d == "portal.beijing.gov.cn" {
			found = true
		}
	}
	if !found {
		t.Fatalf("URL host chưa được tách vào ứng viên tên miền: %v", domains)
	}
	r, _, ok := MatchAssetInterceptRules([]AssetInterceptRule{rule("fuzzy_domain", ".gov.cn", true)}, domains, nil, urls)
	if !ok {
		t.Fatalf("tài sản dịch vụ chính phủ chỉ có URL phải bị fuzzy_domain bắt trúng, rule=%+v", r)
	}

	// Khi URL host là IP thì phải xếp vào ứng viên IP, có thể bị CIDR bắt trúng.
	b := &Asset{Type: "service", URL: "http://10.1.2.3/x"}
	_, ips, _ := b.interceptCandidates()
	if r, _, ok := MatchAssetInterceptRules([]AssetInterceptRule{rule("cidr", "10.0.0.0/8", true)}, nil, ips, nil); !ok {
		t.Fatalf("IP trong URL phải bị CIDR bắt trúng, ips=%v rule=%+v", ips, r)
	}
}
