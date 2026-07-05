package core

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func newSQLiteTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store := NewStore(path)
	if err := store.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	return store
}

func TestStorePersistsSubscriptionsConfigAndResults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "http-proxy.db")
	store := newSQLiteTestStore(t, dbPath)

	now := time.Now().UTC().Truncate(time.Second)
	sub := &Subscription{
		ID:        "sub-one",
		URL:       "https://example.com/sub",
		Name:      "Example",
		Enabled:   true,
		AddedAt:   now,
		UpdatedAt: now,
		Proxies: []*ProxyInfo{{
			Type:       "vless",
			Server:     "example.com",
			ServerPort: 443,
			UUID:       "00000000-0000-0000-0000-000000000000",
			Name:       "Proxy One",
			Tag:        "proxy-one",
			Protocol:   "vless",
			TLS: &TLSConfig{
				Enabled:    true,
				ServerName: "example.com",
				Reality: &RealityConfig{
					Enabled:   true,
					PublicKey: "public-key",
					ShortID:   "short-id",
				},
			},
			Transport: &TransportConfig{
				Type: "websocket",
				Path: "/ws",
				Headers: map[string]string{
					"Host": "example.com",
				},
			},
		}},
	}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}
	if err := store.SaveConfig(AppConfig{
		UpstreamProxy:                  "socks5://127.0.0.1:1080",
		TestTarget:                     "http://example.com/probe",
		TestTimeoutSeconds:             7,
		AdminToken:                     "admin-token",
		AvailableToken:                 "available-token",
		PoolProxyUsername:              "pool-user",
		PoolProxyPassword:              "pool-pass",
		AvailableCacheTTLSeconds:       45,
		TestResultTTLMinutes:           240,
		AvailableQuickProbeSeconds:     2,
		AvailableQuickConcurrency:      12,
		AvailableBackgroundConcurrency: 4,
		AvailableMinWarmPoolSize:       30,
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := store.SaveTestResult(&TestResult{
		Tag:       "proxy-one",
		Latency:   123,
		Timestamp: now,
	}); err != nil {
		t.Fatalf("save test result: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened := newSQLiteTestStore(t, dbPath)
	subs := reopened.GetSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("subscription count = %d, want 1", len(subs))
	}
	if got := subs[0].Proxies[0].Transport.Headers["Host"]; got != "example.com" {
		t.Fatalf("proxy header = %q, want example.com", got)
	}
	if subs[0].Proxies[0].TLS == nil || subs[0].Proxies[0].TLS.Reality == nil || !subs[0].Proxies[0].TLS.Reality.Enabled {
		t.Fatalf("proxy reality TLS was not restored")
	}

	cfg, err := reopened.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.UpstreamProxy != "socks5://127.0.0.1:1080" || cfg.TestTarget != "http://example.com/probe" || cfg.TestTimeoutSeconds != 7 {
		t.Fatalf("config = %+v, want persisted values", cfg)
	}
	if cfg.AdminToken != "admin-token" || cfg.AvailableToken != "available-token" || cfg.PoolProxyUsername != "pool-user" || cfg.PoolProxyPassword != "pool-pass" {
		t.Fatalf("auth config = %+v, want persisted auth values", cfg)
	}
	if cfg.AvailableCacheTTLSeconds != 45 || cfg.TestResultTTLMinutes != 240 || cfg.AvailableQuickProbeSeconds != 2 || cfg.AvailableQuickConcurrency != 12 || cfg.AvailableBackgroundConcurrency != 4 || cfg.AvailableMinWarmPoolSize != 30 {
		t.Fatalf("available config = %+v, want persisted available values", cfg)
	}

	results, err := reopened.LoadTestResults()
	if err != nil {
		t.Fatalf("load test results: %v", err)
	}
	if got := results["proxy-one"]; got == nil || got.Latency != 123 || got.Err != "" {
		t.Fatalf("test result = %+v, want persisted latency", got)
	}
}

func TestStoreLoadConfigDefaultsForEmptyDatabase(t *testing.T) {
	store := newSQLiteTestStore(t, filepath.Join(t.TempDir(), "http-proxy.db"))
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.TestTarget != DefaultTestTarget {
		t.Fatalf("default test target = %q, want %q", cfg.TestTarget, DefaultTestTarget)
	}
	if cfg.TestTimeoutSeconds != DefaultTestTimeoutSeconds {
		t.Fatalf("default timeout = %d, want %d", cfg.TestTimeoutSeconds, DefaultTestTimeoutSeconds)
	}
	if cfg.AvailableCacheTTLSeconds != DefaultAvailableCacheTTLSeconds ||
		cfg.TestResultTTLMinutes != DefaultTestResultTTLMinutes ||
		cfg.AvailableQuickProbeSeconds != DefaultAvailableQuickProbeSeconds ||
		cfg.AvailableQuickConcurrency != DefaultAvailableQuickConcurrency ||
		cfg.AvailableBackgroundConcurrency != DefaultAvailableBackgroundConcurrency ||
		cfg.AvailableMinWarmPoolSize != DefaultAvailableMinWarmPoolSize {
		t.Fatalf("available defaults = %+v, want defaults", cfg)
	}
}

func TestStoreMigratesAppConfigAvailableColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "http-proxy.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE app_config (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		upstream_proxy TEXT NOT NULL DEFAULT '',
		test_target TEXT NOT NULL DEFAULT '',
		test_timeout_seconds INTEGER NOT NULL DEFAULT 0,
		admin_token TEXT NOT NULL DEFAULT '',
		available_token TEXT NOT NULL DEFAULT '',
		pool_proxy_username TEXT NOT NULL DEFAULT '',
		pool_proxy_password TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	)`); err != nil {
		db.Close()
		t.Fatalf("create old app_config: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO app_config (
		id, upstream_proxy, test_target, test_timeout_seconds, admin_token,
		available_token, pool_proxy_username, pool_proxy_password, updated_at
	) VALUES (1, '', '', 0, '', '', '', '', ?)`, formatDBTime(time.Now().UTC())); err != nil {
		db.Close()
		t.Fatalf("insert old app_config: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	store := newSQLiteTestStore(t, dbPath)
	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load migrated config: %v", err)
	}
	if cfg.AvailableCacheTTLSeconds != DefaultAvailableCacheTTLSeconds ||
		cfg.TestResultTTLMinutes != DefaultTestResultTTLMinutes ||
		cfg.AvailableQuickProbeSeconds != DefaultAvailableQuickProbeSeconds ||
		cfg.AvailableQuickConcurrency != DefaultAvailableQuickConcurrency ||
		cfg.AvailableBackgroundConcurrency != DefaultAvailableBackgroundConcurrency ||
		cfg.AvailableMinWarmPoolSize != DefaultAvailableMinWarmPoolSize {
		t.Fatalf("migrated config = %+v, want available defaults", cfg)
	}
}

func TestTesterReusesPersistedFreshResults(t *testing.T) {
	store := newSQLiteTestStore(t, filepath.Join(t.TempDir(), "http-proxy.db"))
	proxy := &ProxyInfo{
		Name:     "Persisted Direct",
		Tag:      "persisted-direct",
		Protocol: "direct",
	}
	if err := store.SaveTestResult(&TestResult{
		Tag:       proxy.Tag,
		Latency:   111,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save test result: %v", err)
	}

	tester, err := NewTesterWithStore([]*ProxyInfo{proxy}, "", store)
	if err != nil {
		t.Fatalf("new tester: %v", err)
	}
	defer tester.Close()

	results := tester.GetOrRefreshResults([]*ProxyInfo{proxy})
	if got := results[proxy.Tag]; got == nil || got.Latency != 111 || got.Err != "" {
		t.Fatalf("result = %+v, want persisted fresh result", got)
	}
}
