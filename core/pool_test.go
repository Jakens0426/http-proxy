package core

import (
	"encoding/base64"
	"testing"

	C "github.com/sagernet/sing-box/constant"
)

func TestBuildUpstreamOutboundSupportedProtocols(t *testing.T) {
	ssURL := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:secret@example.com:8388"))
	tests := []struct {
		name     string
		raw      string
		wantType string
	}{
		{
			name:     "http",
			raw:      "http://user:pass@example.com:8080",
			wantType: C.TypeHTTP,
		},
		{
			name:     "socks5",
			raw:      "socks5://user:pass@example.com:1080",
			wantType: C.TypeSOCKS,
		},
		{
			name:     "ss",
			raw:      ssURL,
			wantType: C.TypeShadowsocks,
		},
		{
			name:     "vless",
			raw:      "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls#vless",
			wantType: C.TypeVLESS,
		},
		{
			name:     "trojan",
			raw:      "trojan://secret@example.com:443?sni=example.com#trojan",
			wantType: C.TypeTrojan,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outbound, err := buildUpstreamOutbound(tt.raw)
			if err != nil {
				t.Fatalf("buildUpstreamOutbound() error = %v", err)
			}
			if outbound.Tag != "upstream" {
				t.Fatalf("tag = %q, want upstream", outbound.Tag)
			}
			if outbound.Type != tt.wantType {
				t.Fatalf("type = %q, want %q", outbound.Type, tt.wantType)
			}
		})
	}
}

func TestBuildUpstreamOutboundRejectsInvalidInput(t *testing.T) {
	tests := []string{
		"",
		"ftp://example.com:21",
		"http://:8080",
		"http://example.com",
		"socks5://example.com:bad",
		"ss://not-a-valid-share",
		"vless://00000000-0000-0000-0000-000000000000@example.com",
	}

	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if outbound, err := buildUpstreamOutbound(raw); err == nil {
				t.Fatalf("buildUpstreamOutbound() = %+v, want error", outbound)
			}
		})
	}
}
