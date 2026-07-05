package core

import (
	"encoding/base64"
	"testing"
)

func TestParseSsSupportsFullBase64(t *testing.T) {
	raw := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:secret@example.com:8388")) + "#full"
	p := ParseProxyURL(raw)
	if p == nil {
		t.Fatal("expected ss proxy to parse")
	}
	if p.Protocol != "ss" {
		t.Fatalf("protocol = %q, want ss", p.Protocol)
	}
	if p.Method != "aes-128-gcm" {
		t.Fatalf("method = %q, want aes-128-gcm", p.Method)
	}
	if p.Password != "secret" {
		t.Fatalf("password = %q, want secret", p.Password)
	}
	if p.Server != "example.com" {
		t.Fatalf("server = %q, want example.com", p.Server)
	}
	if p.ServerPort != 8388 {
		t.Fatalf("port = %d, want 8388", p.ServerPort)
	}
	if p.Name != "full" {
		t.Fatalf("name = %q, want full", p.Name)
	}
}

func TestParseSsSupportsSIP002PlainText(t *testing.T) {
	p := ParseProxyURL("ss://aes-256-gcm:secret@example.com:443#plain")
	if p == nil {
		t.Fatal("expected ss proxy to parse")
	}
	if p.Method != "aes-256-gcm" {
		t.Fatalf("method = %q, want aes-256-gcm", p.Method)
	}
	if p.Password != "secret" {
		t.Fatalf("password = %q, want secret", p.Password)
	}
	if p.Server != "example.com" {
		t.Fatalf("server = %q, want example.com", p.Server)
	}
	if p.ServerPort != 443 {
		t.Fatalf("port = %d, want 443", p.ServerPort)
	}
	if p.Name != "plain" {
		t.Fatalf("name = %q, want plain", p.Name)
	}
}

func TestParseSsSupportsSIP002EncodedUserInfo(t *testing.T) {
	userInfo := base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:p@ss"))
	p := ParseProxyURL("ss://" + userInfo + "@example.net:8443#encoded")
	if p == nil {
		t.Fatal("expected ss proxy to parse")
	}
	if p.Method != "chacha20-ietf-poly1305" {
		t.Fatalf("method = %q, want chacha20-ietf-poly1305", p.Method)
	}
	if p.Password != "p@ss" {
		t.Fatalf("password = %q, want p@ss", p.Password)
	}
	if p.Server != "example.net" {
		t.Fatalf("server = %q, want example.net", p.Server)
	}
	if p.ServerPort != 8443 {
		t.Fatalf("port = %d, want 8443", p.ServerPort)
	}
}
