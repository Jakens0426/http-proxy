package core

import "time"

const (
	DefaultProxyCount                     = 10
	DefaultTestTarget                     = "https://www.gstatic.com/generate_204"
	DefaultTestTimeoutSeconds             = 3
	MinTestTimeoutSeconds                 = 1
	MaxTestTimeoutSeconds                 = 60
	MaxLatencyMs                          = 500
	DefaultAvailableCacheTTLSeconds       = 30
	MinAvailableCacheTTLSeconds           = 10
	MaxAvailableCacheTTLSeconds           = 3600
	DefaultTestResultTTLMinutes           = 120
	MinTestResultTTLMinutes               = 5
	MaxTestResultTTLMinutes               = 1440
	DefaultAvailableQuickProbeSeconds     = 1
	MinAvailableQuickProbeSeconds         = 1
	MaxAvailableQuickProbeSeconds         = 10
	DefaultAvailableQuickConcurrency      = 10
	MinAvailableQuickConcurrency          = 1
	MaxAvailableQuickConcurrency          = 50
	DefaultAvailableBackgroundConcurrency = 3
	MinAvailableBackgroundConcurrency     = 1
	MaxAvailableBackgroundConcurrency     = 20
	DefaultAvailableMinWarmPoolSize       = 20
	MinAvailableMinWarmPoolSize           = 1
	MaxAvailableMinWarmPoolSize           = 100
	TestConcurrency                       = DefaultAvailableBackgroundConcurrency
	QuickTestConcurrency                  = DefaultAvailableQuickConcurrency
	TestResultTTL                         = time.Duration(DefaultTestResultTTLMinutes) * time.Minute
	PoolPortStart                         = 10000
	PoolPortEnd                           = 10099
	ListenPort                            = 9090
	DatabaseFile                          = "data/http-proxy.db"
	CacheTTL                              = time.Duration(DefaultAvailableCacheTTLSeconds) * time.Second
)

type AppConfig struct {
	UpstreamProxy                  string `json:"upstream_proxy"`
	TestTarget                     string `json:"test_target"`
	TestTimeoutSeconds             int    `json:"test_timeout_seconds"`
	AdminToken                     string `json:"admin_token"`
	AvailableToken                 string `json:"available_token"`
	PoolProxyUsername              string `json:"pool_proxy_username"`
	PoolProxyPassword              string `json:"pool_proxy_password"`
	AvailableCacheTTLSeconds       int    `json:"available_cache_ttl_seconds"`
	TestResultTTLMinutes           int    `json:"test_result_ttl_minutes"`
	AvailableQuickProbeSeconds     int    `json:"available_quick_probe_seconds"`
	AvailableQuickConcurrency      int    `json:"available_quick_concurrency"`
	AvailableBackgroundConcurrency int    `json:"available_background_concurrency"`
	AvailableMinWarmPoolSize       int    `json:"available_min_warm_pool_size"`
}

func (c *AppConfig) Default() {
	if c.TestTarget == "" {
		c.TestTarget = DefaultTestTarget
	}
	if c.TestTimeoutSeconds == 0 {
		c.TestTimeoutSeconds = DefaultTestTimeoutSeconds
	}
	if c.AvailableCacheTTLSeconds == 0 {
		c.AvailableCacheTTLSeconds = DefaultAvailableCacheTTLSeconds
	}
	if c.TestResultTTLMinutes == 0 {
		c.TestResultTTLMinutes = DefaultTestResultTTLMinutes
	}
	if c.AvailableQuickProbeSeconds == 0 {
		c.AvailableQuickProbeSeconds = DefaultAvailableQuickProbeSeconds
	}
	if c.AvailableQuickConcurrency == 0 {
		c.AvailableQuickConcurrency = DefaultAvailableQuickConcurrency
	}
	if c.AvailableBackgroundConcurrency == 0 {
		c.AvailableBackgroundConcurrency = DefaultAvailableBackgroundConcurrency
	}
	if c.AvailableMinWarmPoolSize == 0 {
		c.AvailableMinWarmPoolSize = DefaultAvailableMinWarmPoolSize
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

func NormalizeAvailableCacheTTLSeconds(seconds int, fallback int) int {
	return normalizeIntRange(seconds, fallback, DefaultAvailableCacheTTLSeconds, MinAvailableCacheTTLSeconds, MaxAvailableCacheTTLSeconds)
}

func AvailableCacheTTLDuration(seconds int) time.Duration {
	return time.Duration(NormalizeAvailableCacheTTLSeconds(seconds, DefaultAvailableCacheTTLSeconds)) * time.Second
}

func NormalizeTestResultTTLMinutes(minutes int, fallback int) int {
	return normalizeIntRange(minutes, fallback, DefaultTestResultTTLMinutes, MinTestResultTTLMinutes, MaxTestResultTTLMinutes)
}

func TestResultTTLDuration(minutes int) time.Duration {
	return time.Duration(NormalizeTestResultTTLMinutes(minutes, DefaultTestResultTTLMinutes)) * time.Minute
}

func NormalizeAvailableQuickProbeSeconds(seconds int, fallback int) int {
	return normalizeIntRange(seconds, fallback, DefaultAvailableQuickProbeSeconds, MinAvailableQuickProbeSeconds, MaxAvailableQuickProbeSeconds)
}

func AvailableQuickProbeDuration(seconds int) time.Duration {
	return time.Duration(NormalizeAvailableQuickProbeSeconds(seconds, DefaultAvailableQuickProbeSeconds)) * time.Second
}

func NormalizeAvailableQuickConcurrency(value int, fallback int) int {
	return normalizeIntRange(value, fallback, DefaultAvailableQuickConcurrency, MinAvailableQuickConcurrency, MaxAvailableQuickConcurrency)
}

func NormalizeAvailableBackgroundConcurrency(value int, fallback int) int {
	return normalizeIntRange(value, fallback, DefaultAvailableBackgroundConcurrency, MinAvailableBackgroundConcurrency, MaxAvailableBackgroundConcurrency)
}

func NormalizeAvailableMinWarmPoolSize(value int, fallback int) int {
	return normalizeIntRange(value, fallback, DefaultAvailableMinWarmPoolSize, MinAvailableMinWarmPoolSize, MaxAvailableMinWarmPoolSize)
}

func normalizeIntRange(value int, fallback int, defaultValue int, minValue int, maxValue int) int {
	if value >= minValue && value <= maxValue {
		return value
	}
	if fallback >= minValue && fallback <= maxValue {
		return fallback
	}
	return defaultValue
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
