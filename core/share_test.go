package core

import (
	"strings"
	"testing"
)

func TestBuildShareURLRoundTripsSupportedProtocols(t *testing.T) {
	tests := []*ProxyInfo{
		{
			Protocol:   "ss",
			Type:       "shadowsocks",
			Server:     "example.com",
			ServerPort: 8388,
			Method:     "aes-256-gcm",
			Password:   "secret",
			Name:       "SS One",
		},
		{
			Protocol:       "vless",
			Type:           "vless",
			Server:         "vless.example.com",
			ServerPort:     443,
			UUID:           "0828a0bc-8acf-422c-9fac-8ad885a0dbaf",
			Flow:           "xtls-rprx-vision",
			PacketEncoding: "xudp",
			TLS: &TLSConfig{
				Enabled:    true,
				ServerName: "www.example.com",
				UTLS:       &UTLSConfig{Enabled: true, Fingerprint: "chrome"},
				Reality:    &RealityConfig{Enabled: true, PublicKey: "public-key", ShortID: "abcd"},
			},
			Transport: &TransportConfig{
				Type: "grpc",
				Path: "movie",
			},
			Name: "VLESS One",
		},
		{
			Protocol:   "trojan",
			Type:       "trojan",
			Server:     "trojan.example.com",
			ServerPort: 443,
			Password:   "pass/word",
			TLS: &TLSConfig{
				Enabled:    true,
				ServerName: "trojan.example.com",
			},
			Transport: &TransportConfig{
				Type:    "ws",
				Path:    "/socket",
				Headers: map[string]string{"Host": "cdn.example.com"},
			},
			Name: "Trojan One",
		},
	}

	for _, original := range tests {
		t.Run(original.Protocol, func(t *testing.T) {
			shareURL := BuildShareURL(original)
			if shareURL == "" {
				t.Fatalf("share URL is empty")
			}
			parsed := ParseProxyURL(shareURL)
			if parsed == nil {
				t.Fatalf("could not parse share URL: %s", shareURL)
			}
			if parsed.Protocol != original.Protocol || parsed.Server != original.Server || parsed.ServerPort != original.ServerPort {
				t.Fatalf("parsed = %+v, want protocol/server/port from %+v", parsed, original)
			}
			if !strings.Contains(shareURL, "#") {
				t.Fatalf("share URL missing name fragment: %s", shareURL)
			}
		})
	}
}
