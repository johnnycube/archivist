package rest

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	const peer = "203.0.113.9" // the TCP peer in every case
	for _, tc := range []struct {
		name    string
		proxies int
		xff     []string // header lines, in order
		want    string
	}{
		{"no proxy: header ignored", 0, []string{"1.2.3.4"}, peer},
		{"no proxy, no header", 0, nil, peer},
		{"one proxy", 1, []string{"198.51.100.7"}, "198.51.100.7"},
		{"one proxy, no header", 1, nil, peer},
		{"one proxy, empty header", 1, []string{""}, peer},
		{"spoofed left entry is ignored", 1, []string{"1.2.3.4, 198.51.100.7"}, "198.51.100.7"},
		{"many spoofed entries", 1, []string{"1.1.1.1, 2.2.2.2, 3.3.3.3, 198.51.100.7"}, "198.51.100.7"},
		{"two proxies", 2, []string{"1.2.3.4, 198.51.100.7, 10.0.0.2"}, "198.51.100.7"},
		{"chain shorter than the proxy count", 2, []string{"198.51.100.7"}, peer},
		{"chain exactly the proxy count", 2, []string{"198.51.100.7, 10.0.0.2"}, "198.51.100.7"},
		{"repeated header lines form one chain", 2, []string{"1.2.3.4", "198.51.100.7", "10.0.0.2"}, "198.51.100.7"},
		{"spoofed line before the proxy's line", 1, []string{"1.2.3.4, 5.6.7.8", "198.51.100.7"}, "198.51.100.7"},
		{"mixed lines and lists", 2, []string{"1.2.3.4, 198.51.100.7", "10.0.0.2"}, "198.51.100.7"},
		{"whitespace", 1, []string{"  1.2.3.4 ,\t198.51.100.7  "}, "198.51.100.7"},
		{"ipv6", 1, []string{"1.2.3.4, 2001:db8::1"}, "2001:db8::1"},
		{"ipv6 is canonicalized", 1, []string{"2001:0DB8:0000:0000:0000:0000:0000:0001"}, "2001:db8::1"},
		{"ipv4-mapped ipv6 is unmapped", 1, []string{"::ffff:198.51.100.7"}, "198.51.100.7"},
		{"ipv6 zone is dropped", 1, []string{"fe80::1%eth0"}, "fe80::1"},
		{"ipv4 with port", 1, []string{"198.51.100.7:4711"}, "198.51.100.7"},
		{"ipv6 with port", 1, []string{"[2001:db8::1]:4711"}, "2001:db8::1"},
		{"garbage", 1, []string{"not-an-ip"}, peer},
		{"garbage in the trusted position", 1, []string{"198.51.100.7, <script>"}, peer},
		{"garbage left of the trusted position", 1, []string{"<script>, 198.51.100.7"}, "198.51.100.7"},
		{"empty trusted entry", 1, []string{"198.51.100.7, "}, peer},
		{"hostname", 1, []string{"evil.example.com"}, peer},
		{"log injection attempt", 1, []string{"1.2.3.4\" user=\"admin"}, peer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = peer + ":55555"
			for _, line := range tc.xff {
				r.Header.Add("X-Forwarded-For", line)
			}
			s := &Server{TrustedProxies: tc.proxies}
			if got := s.clientIP(r); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientIPRemoteAddrForms(t *testing.T) {
	for remote, want := range map[string]string{
		"203.0.113.9:1234":          "203.0.113.9",
		"[2001:db8::1]:1234":        "2001:db8::1",
		"[::ffff:203.0.113.9]:1234": "203.0.113.9",
		"@":                         "@", // e.g. a unix socket: passed through
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if got := (&Server{}).clientIP(r); got != want {
			t.Errorf("clientIP(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestIPRateKey(t *testing.T) {
	for ip, want := range map[string]string{
		"198.51.100.7":         "198.51.100.7",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2::1":      "2001:db8:1:2::/64",
		"::ffff:198.51.100.7":  "::ffff:198.51.100.7",
		"@":                    "@",
	} {
		if got := ipRateKey(ip); got != want {
			t.Errorf("ipRateKey(%q) = %q, want %q", ip, got, want)
		}
	}
}
