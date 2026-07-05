package core

import "time"

const (
	DefaultProxyCount         = 10
	DefaultTestTarget         = "https://www.gstatic.com/generate_204"
	DefaultTestTimeoutSeconds = 3
	MinTestTimeoutSeconds     = 1
	MaxTestTimeoutSeconds     = 60
	MaxLatencyMs              = 500
	TestConcurrency           = 3
	TestResultTTL             = 2 * time.Hour
	PoolPortStart             = 10000
	PoolPortEnd               = 10099
	ListenPort                = 9090
	DatabaseFile              = "data/http-proxy.db"
	CacheTTL                  = 30 * time.Second
)

type AppConfig struct {
	UpstreamProxy      string `json:"upstream_proxy"`
	TestTarget         string `json:"test_target"`
	TestTimeoutSeconds int    `json:"test_timeout_seconds"`
	AdminToken         string `json:"admin_token"`
	AvailableToken     string `json:"available_token"`
	PoolProxyUsername  string `json:"pool_proxy_username"`
	PoolProxyPassword  string `json:"pool_proxy_password"`
}

func (c *AppConfig) Default() {
	if c.TestTarget == "" {
		c.TestTarget = DefaultTestTarget
	}
	if c.TestTimeoutSeconds == 0 {
		c.TestTimeoutSeconds = DefaultTestTimeoutSeconds
	}
}

func IsValidTestTimeoutSeconds(seconds int) bool {
	return seconds >= MinTestTimeoutSeconds && seconds <= MaxTestTimeoutSeconds
}

func NormalizeTestTimeoutSeconds(seconds int, fallback int) int {
	if IsValidTestTimeoutSeconds(seconds) {
		return seconds
	}
	if IsValidTestTimeoutSeconds(fallback) {
		return fallback
	}
	return DefaultTestTimeoutSeconds
}

func TestTimeoutDuration(seconds int) time.Duration {
	return time.Duration(NormalizeTestTimeoutSeconds(seconds, DefaultTestTimeoutSeconds)) * time.Second
}

type ProxyInfo struct {
	Type           string           `json:"type"`
	Server         string           `json:"server"`
	ServerPort     int              `json:"server_port"`
	Password       string           `json:"password,omitempty"`
	Method         string           `json:"method,omitempty"`
	UUID           string           `json:"uuid,omitempty"`
	Flow           string           `json:"flow,omitempty"`
	PacketEncoding string           `json:"packet_encoding,omitempty"`
	TLS            *TLSConfig       `json:"tls,omitempty"`
	Transport      *TransportConfig `json:"transport,omitempty"`
	Name           string           `json:"_name"`
	Tag            string           `json:"_tag"`
	Protocol       string           `json:"_protocol"`
	SourceID       string           `json:"_source_id"`
}

type TLSConfig struct {
	Enabled    bool           `json:"enabled"`
	ServerName string         `json:"server_name,omitempty"`
	Insecure   bool           `json:"insecure,omitempty"`
	ALPN       []string       `json:"alpn,omitempty"`
	UTLS       *UTLSConfig    `json:"utls,omitempty"`
	Reality    *RealityConfig `json:"reality,omitempty"`
}

type UTLSConfig struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

type RealityConfig struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
}

type TransportConfig struct {
	Type    string            `json:"type"`
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type TestResult struct {
	Tag       string    `json:"tag"`
	Latency   int       `json:"latency"`
	Err       string    `json:"err,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type Subscription struct {
	ID        string       `json:"id"`
	URL       string       `json:"url"`
	Name      string       `json:"name"`
	Enabled   bool         `json:"enabled"`
	AddedAt   time.Time    `json:"added_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Proxies   []*ProxyInfo `json:"proxies"`
}
