package core

import (
	"encoding/base64"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func BuildTag(server string, port int) string {
	clean := strings.NewReplacer(".", "_", ":", "_", "-", "_")
	s := clean.Replace(server)
	if len(s) > 30 {
		s = s[:30]
	}
	return "proxy_" + s + "_" + strconv.Itoa(port)
}

func extractName(rawURL string) string {
	hashIdx := strings.LastIndex(rawURL, "#")
	if hashIdx >= 0 {
		decoded, err := url.QueryUnescape(rawURL[hashIdx+1:])
		if err == nil {
			return decoded
		}
		return rawURL[hashIdx+1:]
	}
	return ""
}

func ParseProxyURL(rawURL string) *ProxyInfo {
	if !strings.Contains(rawURL, "://") {
		return nil
	}
	proto := strings.SplitN(rawURL, "://", 2)[0]
	rest := rawURL[len(proto)+3:]

	var hashIdx = strings.Index(rest, "#")
	var data string
	if hashIdx >= 0 {
		data = rest[:hashIdx]
	} else {
		data = rest
	}

	switch proto {
	case "vless":
		return parseVless(data, rawURL)
	case "trojan":
		return parseTrojan(data, rawURL)
	case "ss":
		return parseSs(data, rawURL)
	case "vmess":
		return nil
	}
	return nil
}

func parseVless(data, rawURL string) *ProxyInfo {
	atIdx := strings.Index(data, "@")
	if atIdx < 0 {
		return nil
	}
	uuid := data[:atIdx]
	hostPort := data[atIdx+1:]
	qIdx := strings.Index(hostPort, "?")
	hostPart := hostPort
	var queryParams string
	if qIdx >= 0 {
		hostPart = hostPort[:qIdx]
		queryParams = hostPort[qIdx+1:]
	}
	parts := strings.SplitN(hostPart, ":", 2)
	if len(parts) != 2 {
		return nil
	}
	server := parts[0]
	port, _ := strconv.Atoi(parts[1])
	if port == 0 {
		port = 443
	}

	p := &ProxyInfo{
		Type:           "vless",
		Server:         server,
		ServerPort:     port,
		UUID:           uuid,
		PacketEncoding: "xudp",
		Protocol:       "vless",
	}

	if queryParams != "" {
		q, _ := url.ParseQuery(queryParams)
		security := q.Get("security")
		switch security {
		case "tls", "reality":
			ensureTLS(p)
		}
		switch q.Get("type") {
		case "ws", "websocket":
			ensureTransport(p, "ws")
		case "grpc":
			ensureTransport(p, "grpc")
		}
		if flow := q.Get("flow"); flow != "" {
			p.Flow = flow
		}
		if fp := q.Get("fp"); fp != "" {
			ensureTLS(p)
			p.TLS.UTLS = &UTLSConfig{
				Enabled:     true,
				Fingerprint: fp,
			}
		}
		if pbk := q.Get("pbk"); pbk != "" {
			ensureTLS(p)
			p.TLS.Reality = &RealityConfig{
				Enabled:   true,
				PublicKey: pbk,
				ShortID:   q.Get("sid"),
			}
		}
		if sni := q.Get("sni"); sni != "" {
			ensureTLS(p)
			p.TLS.ServerName = sni
		}
		if alpn := q.Get("alpn"); alpn != "" {
			ensureTLS(p)
			p.TLS.ALPN = strings.Split(strings.ReplaceAll(alpn, "%2C", ","), ",")
		}
		if q.Get("allowInsecure") == "1" || q.Get("allowInsecure") == "true" {
			ensureTLS(p)
			p.TLS.Insecure = true
		}
		if security == "none" || security == "" {
			if p.TLS != nil && (p.TLS.Reality == nil || !p.TLS.Reality.Enabled) {
				p.TLS = nil
			}
		}
		if host := q.Get("host"); host != "" {
			if p.Transport == nil {
				ensureTransport(p, "ws")
			}
			if p.Transport.Headers == nil {
				p.Transport.Headers = make(map[string]string)
			}
			p.Transport.Headers["Host"] = host
		}
		if path := q.Get("path"); path != "" {
			if p.Transport == nil {
				ensureTransport(p, "ws")
			}
			p.Transport.Path = path
		}
		if serviceName := q.Get("serviceName"); serviceName != "" {
			ensureTransport(p, "grpc")
			p.Transport.Path = serviceName
		}
	}

	name := extractName(rawURL)
	if name == "" {
		name = "VLESS-" + server + ":" + strconv.Itoa(port)
	}
	p.Name = name
	p.Tag = BuildTag(server, port)
	return p
}

func parseTrojan(data, rawURL string) *ProxyInfo {
	atIdx := strings.Index(data, "@")
	if atIdx < 0 {
		return nil
	}
	password := data[:atIdx]
	hostPort := data[atIdx+1:]
	qIdx := strings.Index(hostPort, "?")
	hostPart := hostPort
	var queryParams string
	if qIdx >= 0 {
		hostPart = hostPort[:qIdx]
		queryParams = hostPort[qIdx+1:]
	}
	parts := strings.SplitN(hostPart, ":", 2)
	if len(parts) != 2 {
		return nil
	}
	server := parts[0]
	port, _ := strconv.Atoi(parts[1])
	if port == 0 {
		port = 443
	}

	p := &ProxyInfo{
		Type:       "trojan",
		Server:     server,
		ServerPort: port,
		Password:   password,
		Protocol:   "trojan",
		TLS:        &TLSConfig{Enabled: true},
	}

	if queryParams != "" {
		q, _ := url.ParseQuery(queryParams)
		switch q.Get("type") {
		case "ws", "websocket":
			ensureTransport(p, "ws")
		case "grpc":
			ensureTransport(p, "grpc")
		}
		if sni := q.Get("sni"); sni != "" {
			p.TLS.ServerName = sni
		}
		if alpn := q.Get("alpn"); alpn != "" {
			alpn = strings.ReplaceAll(alpn, "%2C", ",")
			p.TLS.ALPN = strings.Split(alpn, ",")
		}
		if q.Get("allowInsecure") == "1" || q.Get("allowInsecure") == "true" {
			p.TLS.Insecure = true
		}
		if fp := q.Get("fp"); fp != "" {
			p.TLS.UTLS = &UTLSConfig{
				Enabled:     true,
				Fingerprint: fp,
			}
		}
		if host := q.Get("host"); host != "" {
			if p.Transport == nil {
				ensureTransport(p, "ws")
			}
			if p.Transport.Headers == nil {
				p.Transport.Headers = make(map[string]string)
			}
			p.Transport.Headers["Host"] = host
		}
		if path := q.Get("path"); path != "" {
			if p.Transport == nil {
				ensureTransport(p, "ws")
			}
			p.Transport.Path = path
		}
		if serviceName := q.Get("serviceName"); serviceName != "" {
			ensureTransport(p, "grpc")
			p.Transport.Path = serviceName
		}
	}

	name := extractName(rawURL)
	if name == "" {
		name = "Trojan-" + server + ":" + strconv.Itoa(port)
	}
	p.Name = name
	p.Tag = BuildTag(server, port)
	return p
}

func ensureTLS(p *ProxyInfo) {
	if p.TLS == nil {
		p.TLS = &TLSConfig{}
	}
	p.TLS.Enabled = true
}

func ensureTransport(p *ProxyInfo, transportType string) {
	if transportType == "websocket" {
		transportType = "ws"
	}
	if p.Transport == nil {
		p.Transport = &TransportConfig{}
	}
	p.Transport.Type = transportType
}

func parseSs(data, rawURL string) *ProxyInfo {
	data = strings.TrimSpace(data)
	if data == "" {
		return nil
	}

	if decoded, ok := decodeBase64Flexible(data); ok {
		if p := parseSsPlain(string(decoded), rawURL); p != nil {
			return p
		}
	}
	return parseSsPlain(data, rawURL)
}

func parseSsPlain(str, rawURL string) *ProxyInfo {
	atIdx := strings.Index(str, "@")
	if atIdx < 0 {
		return nil
	}
	methodPass := str[:atIdx]
	hostPort := str[atIdx+1:]
	if decoded, ok := decodeBase64Flexible(methodPass); ok {
		methodPass = string(decoded)
	}
	if decoded, err := url.QueryUnescape(methodPass); err == nil {
		methodPass = decoded
	}
	colonIdx := strings.Index(methodPass, ":")
	if colonIdx < 0 {
		return nil
	}
	method := methodPass[:colonIdx]
	password := methodPass[colonIdx+1:]
	if method == "" || password == "" {
		return nil
	}
	server, port, ok := parseSSHostPort(hostPort)
	if !ok {
		return nil
	}

	p := &ProxyInfo{
		Type:       "shadowsocks",
		Server:     server,
		ServerPort: port,
		Method:     method,
		Password:   password,
		Protocol:   "ss",
	}

	name := extractName(rawURL)
	if name == "" {
		name = "SS-" + server + ":" + strconv.Itoa(port)
	}
	p.Name = name
	p.Tag = BuildTag(server, port)
	return p
}

func decodeBase64Flexible(data string) ([]byte, bool) {
	data = strings.TrimSpace(data)
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(data)
		if err == nil {
			return decoded, true
		}
	}
	return nil, false
}

func parseSSHostPort(raw string) (string, int, bool) {
	if idx := strings.Index(raw, "?"); idx >= 0 {
		raw = raw[:idx]
	}
	if idx := strings.Index(raw, "/"); idx >= 0 {
		raw = raw[:idx]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, false
	}

	host, portStr, err := net.SplitHostPort(raw)
	if err != nil {
		idx := strings.LastIndex(raw, ":")
		if idx < 0 {
			return "", 0, false
		}
		host = raw[:idx]
		portStr = raw[idx+1:]
		host = strings.Trim(host, "[]")
	}
	if host == "" || portStr == "" {
		return "", 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, false
	}
	return host, port, true
}

func ParseSubscription(text string) []*ProxyInfo {
	var results []*ProxyInfo
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "vmess://") || strings.HasPrefix(line, "vless://") ||
			strings.HasPrefix(line, "trojan://") || strings.HasPrefix(line, "ss://") ||
			strings.HasPrefix(line, "ssr://") {
			if parsed := ParseProxyURL(line); parsed != nil {
				results = append(results, parsed)
			}
		} else {
			decoded, err := base64.StdEncoding.DecodeString(line)
			if err != nil {
				continue
			}
			subLines := strings.Split(string(decoded), "\n")
			for _, sl := range subLines {
				sl = strings.TrimSpace(sl)
				if sl == "" {
					continue
				}
				if strings.HasPrefix(sl, "vmess://") || strings.HasPrefix(sl, "vless://") ||
					strings.HasPrefix(sl, "trojan://") || strings.HasPrefix(sl, "ss://") ||
					strings.HasPrefix(sl, "ssr://") {
					if parsed := ParseProxyURL(sl); parsed != nil {
						results = append(results, parsed)
					}
				}
			}
		}
	}
	return results
}
