package core

import (
	"encoding/base64"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func BuildShareURL(p *ProxyInfo) string {
	if p == nil {
		return ""
	}
	switch strings.ToLower(p.Protocol) {
	case "ss", "shadowsocks":
		return buildSSShareURL(p)
	case "vless":
		return buildVLESSShareURL(p)
	case "trojan":
		return buildTrojanShareURL(p)
	default:
		return ""
	}
}

func buildSSShareURL(p *ProxyInfo) string {
	if p.Method == "" || p.Password == "" || p.Server == "" || p.ServerPort <= 0 {
		return ""
	}
	userInfo := base64.RawURLEncoding.EncodeToString([]byte(p.Method + ":" + p.Password))
	return "ss://" + userInfo + "@" + shareHostPort(p) + shareNameFragment(p.Name)
}

func buildVLESSShareURL(p *ProxyInfo) string {
	if p.UUID == "" || p.Server == "" || p.ServerPort <= 0 {
		return ""
	}

	q := url.Values{}
	q.Set("encryption", "none")
	if p.Flow != "" {
		q.Set("flow", p.Flow)
	}
	if p.PacketEncoding != "" {
		q.Set("packetEncoding", p.PacketEncoding)
	}
	addTLSQuery(q, p)
	addTransportQuery(q, p)

	return "vless://" + url.PathEscape(p.UUID) + "@" + shareHostPort(p) + shareQuery(q) + shareNameFragment(p.Name)
}

func buildTrojanShareURL(p *ProxyInfo) string {
	if p.Password == "" || p.Server == "" || p.ServerPort <= 0 {
		return ""
	}

	q := url.Values{}
	addTLSQuery(q, p)
	addTransportQuery(q, p)

	return "trojan://" + url.PathEscape(p.Password) + "@" + shareHostPort(p) + shareQuery(q) + shareNameFragment(p.Name)
}

func addTLSQuery(q url.Values, p *ProxyInfo) {
	if p.TLS == nil || !p.TLS.Enabled {
		q.Set("security", "none")
		return
	}
	if p.TLS.Reality != nil && p.TLS.Reality.Enabled {
		q.Set("security", "reality")
		q.Set("pbk", p.TLS.Reality.PublicKey)
		if p.TLS.Reality.ShortID != "" {
			q.Set("sid", p.TLS.Reality.ShortID)
		}
	} else {
		q.Set("security", "tls")
	}
	if p.TLS.ServerName != "" {
		q.Set("sni", p.TLS.ServerName)
	}
	if p.TLS.Insecure {
		q.Set("allowInsecure", "1")
	}
	if p.TLS.UTLS != nil && p.TLS.UTLS.Enabled && p.TLS.UTLS.Fingerprint != "" {
		q.Set("fp", p.TLS.UTLS.Fingerprint)
	}
	if len(p.TLS.ALPN) > 0 {
		q.Set("alpn", strings.Join(p.TLS.ALPN, ","))
	}
}

func addTransportQuery(q url.Values, p *ProxyInfo) {
	if p.Transport == nil {
		return
	}
	switch strings.ToLower(p.Transport.Type) {
	case "ws", "websocket":
		q.Set("type", "ws")
		if p.Transport.Path != "" {
			q.Set("path", p.Transport.Path)
		}
		if p.Transport.Headers != nil && p.Transport.Headers["Host"] != "" {
			q.Set("host", p.Transport.Headers["Host"])
		}
	case "grpc":
		q.Set("type", "grpc")
		if p.Transport.Path != "" {
			q.Set("serviceName", p.Transport.Path)
		}
	}
}

func shareHostPort(p *ProxyInfo) string {
	return net.JoinHostPort(p.Server, strconv.Itoa(p.ServerPort))
}

func shareQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func shareNameFragment(name string) string {
	if name == "" {
		return ""
	}
	return "#" + url.QueryEscape(name)
}
