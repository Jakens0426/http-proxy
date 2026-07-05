package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"http-proxy/core"
	"http-proxy/server"
)

func newTestStore(t *testing.T) *core.Store {
	t.Helper()
	return newTestStoreAt(t, filepath.Join(t.TempDir(), "http-proxy.db"))
}

func newTestStoreAt(t *testing.T, path string) *core.Store {
	t.Helper()
	store := core.NewStore(path)
	if err := store.Load(); err != nil {
		t.Fatalf("load test store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close test store: %v", err)
		}
	})
	return store
}

func TestConfigAPIUsesHTTPURLTestTarget(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	getConfig := func() core.AppConfig {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/config status = %d, want 200", rec.Code)
		}
		var cfg core.AppConfig
		if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
			t.Fatalf("decode config: %v", err)
		}
		return cfg
	}

	cfg := getConfig()
	if got := cfg.TestTarget; got != core.DefaultTestTarget {
		t.Fatalf("default test target = %q, want %q", got, core.DefaultTestTarget)
	}
	if got := cfg.TestTimeoutSeconds; got != core.DefaultTestTimeoutSeconds {
		t.Fatalf("default test timeout = %d, want %d", got, core.DefaultTestTimeoutSeconds)
	}

	const customTarget = "http://example.com:8080/health?x=1"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"test_target":"`+customTarget+`","test_timeout_seconds":7}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT valid config status = %d, want 200", rec.Code)
	}
	cfg = getConfig()
	if got := cfg.TestTarget; got != customTarget {
		t.Fatalf("custom test target = %q, want %q", got, customTarget)
	}
	if got := cfg.TestTimeoutSeconds; got != 7 {
		t.Fatalf("custom test timeout = %d, want 7", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"test_target":"www.gstatic.com:80","test_timeout_seconds":61}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT legacy config status = %d, want 200", rec.Code)
	}
	cfg = getConfig()
	if got := cfg.TestTarget; got != customTarget {
		t.Fatalf("legacy test target changed config to %q, want %q", got, customTarget)
	}
	if got := cfg.TestTimeoutSeconds; got != 7 {
		t.Fatalf("invalid test timeout changed config to %d, want 7", got)
	}
}

func TestConfigAPIRejectsPartialPoolProxyAuth(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"pool_proxy_username":"user"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT partial pool auth status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestConfigAPIPersistsAcrossServiceRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "http-proxy.db")
	store := newTestStoreAt(t, dbPath)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	const target = "http://example.com/probe"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"upstream_proxy":"socks5://127.0.0.1:1080","test_target":"`+target+`","test_timeout_seconds":9,"admin_token":"admin-token","available_token":"available-token","pool_proxy_username":"pool-user","pool_proxy_password":"pool-pass","available_cache_ttl_seconds":60,"test_result_ttl_minutes":300,"available_quick_probe_seconds":3,"available_quick_concurrency":15,"available_background_concurrency":5,"available_min_warm_pool_size":25}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened := newTestStoreAt(t, dbPath)
	svc = NewService(core.NewSubscriptionManager(reopened))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv = server.NewServer(svc)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set("X-Admin-Token", "admin-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config status = %d, want 200", rec.Code)
	}
	var cfg core.AppConfig
	if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.UpstreamProxy != "socks5://127.0.0.1:1080" || cfg.TestTarget != target || cfg.TestTimeoutSeconds != 9 {
		t.Fatalf("config = %+v, want persisted values", cfg)
	}
	if cfg.AdminToken != "admin-token" || cfg.AvailableToken != "available-token" || cfg.PoolProxyUsername != "pool-user" || cfg.PoolProxyPassword != "pool-pass" {
		t.Fatalf("auth config = %+v, want persisted auth values", cfg)
	}
	if cfg.AvailableCacheTTLSeconds != 60 || cfg.TestResultTTLMinutes != 300 || cfg.AvailableQuickProbeSeconds != 3 || cfg.AvailableQuickConcurrency != 15 || cfg.AvailableBackgroundConcurrency != 5 || cfg.AvailableMinWarmPoolSize != 25 {
		t.Fatalf("available config = %+v, want persisted available values", cfg)
	}
}

func TestAvailableCandidatesIgnoreStalePoolAuthConfig(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	impl := svc.(*service)
	if impl.tester != nil {
		defer impl.tester.Close()
	}

	impl.availableMu.Lock()
	impl.availablePool = &availableCandidatePool{
		entries: []availableCandidate{{
			proxy: &core.ProxyInfo{
				Name:     "Stale",
				Tag:      "stale",
				Protocol: "direct",
			},
			port:      10000,
			latency:   1,
			updatedAt: time.Now(),
		}},
		poolConfig: poolRuntimeConfig{
			username: "old-user",
			password: "old-pass",
		},
		generation: impl.availableGeneration.Load(),
	}
	impl.availableMu.Unlock()

	proxies, err := impl.GetAvailableProxies(1)
	if err != nil {
		t.Fatalf("GetAvailableProxies() error = %v", err)
	}
	if len(proxies) != 0 {
		t.Fatalf("GetAvailableProxies() = %+v, want stale cache ignored", proxies)
	}
}

func TestAvailableCandidatesNeedRefreshUsesConfiguredCacheTTL(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	impl := svc.(*service)
	if impl.tester != nil {
		defer impl.tester.Close()
	}

	poolConfig := impl.currentPoolRuntimeConfig()
	impl.availableMu.Lock()
	impl.availablePool = &availableCandidatePool{
		entries: []availableCandidate{{
			proxy:     &core.ProxyInfo{Name: "Fresh", Tag: "fresh", Protocol: "direct"},
			port:      10000,
			latency:   1,
			updatedAt: time.Now(),
		}},
		updatedAt:  time.Now().Add(-20 * time.Second),
		poolConfig: poolConfig,
		generation: impl.availableGeneration.Load(),
	}
	impl.availableMu.Unlock()

	now := time.Now()
	shortTTL := availableRuntimeSettings{cacheTTL: 10 * time.Second, testResultTTL: 2 * time.Hour}
	if !impl.availableCandidatesNeedRefresh(poolConfig, now, shortTTL) {
		t.Fatalf("availableCandidatesNeedRefresh with short TTL = false, want true")
	}
	longTTL := availableRuntimeSettings{cacheTTL: 60 * time.Second, testResultTTL: 2 * time.Hour}
	if impl.availableCandidatesNeedRefresh(poolConfig, now, longTTL) {
		t.Fatalf("availableCandidatesNeedRefresh with long TTL = true, want false")
	}
}

func TestAvailableFreshSelectionUsesConfiguredTestResultTTL(t *testing.T) {
	proxy := &core.ProxyInfo{Name: "Proxy", Tag: "proxy", Protocol: "direct"}
	results := map[string]*core.TestResult{
		proxy.Tag: {
			Tag:       proxy.Tag,
			Latency:   50,
			Timestamp: time.Now().Add(-10 * time.Minute),
		},
	}

	selected, _, _ := selectFreshHealthyProxies([]*core.ProxyInfo{proxy}, results, 1, time.Now(), 5*time.Minute)
	if len(selected) != 0 {
		t.Fatalf("selected with short TTL = %d, want 0", len(selected))
	}
	selected, _, _ = selectFreshHealthyProxies([]*core.ProxyInfo{proxy}, results, 1, time.Now(), 15*time.Minute)
	if len(selected) != 1 {
		t.Fatalf("selected with long TTL = %d, want 1", len(selected))
	}

	toTest := proxiesNeedingTest([]*core.ProxyInfo{proxy}, results, time.Now(), 5*time.Minute)
	if len(toTest) != 1 {
		t.Fatalf("toTest with short TTL = %d, want 1", len(toTest))
	}
	toTest = proxiesNeedingTest([]*core.ProxyInfo{proxy}, results, time.Now(), 15*time.Minute)
	if len(toTest) != 0 {
		t.Fatalf("toTest with long TTL = %d, want 0", len(toTest))
	}
}

func TestAvailableStatusReportsProgressAndConfig(t *testing.T) {
	store := newTestStore(t)
	proxies := addDirectTestSubscription(t, store, 4)
	now := time.Now().UTC()
	results := []*core.TestResult{
		{Tag: proxies[0].Tag, Latency: 50, Timestamp: now},
		{Tag: proxies[1].Tag, Err: "timeout", Timestamp: now},
		{Tag: proxies[2].Tag, Latency: 60, Timestamp: now.Add(-10 * time.Minute)},
	}
	for _, result := range results {
		if err := store.SaveTestResult(result); err != nil {
			t.Fatalf("save result: %v", err)
		}
	}
	if err := store.SaveConfig(core.AppConfig{
		TestTarget:                     core.DefaultTestTarget,
		TestTimeoutSeconds:             core.DefaultTestTimeoutSeconds,
		AvailableCacheTTLSeconds:       40,
		TestResultTTLMinutes:           5,
		AvailableQuickProbeSeconds:     2,
		AvailableQuickConcurrency:      11,
		AvailableBackgroundConcurrency: 4,
		AvailableMinWarmPoolSize:       12,
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	impl := svc.(*service)
	if impl.tester != nil {
		defer impl.tester.Close()
	}
	poolConfig := impl.currentPoolRuntimeConfig()
	impl.availableMu.Lock()
	impl.availablePool = &availableCandidatePool{
		entries: []availableCandidate{{
			proxy:     proxies[0],
			port:      10000,
			latency:   50,
			updatedAt: now,
		}},
		updatedAt:  now,
		poolConfig: poolConfig,
		generation: impl.availableGeneration.Load(),
	}
	impl.availableMu.Unlock()
	impl.refreshInProgress.Store(true)
	defer impl.refreshInProgress.Store(false)

	status := impl.GetAvailableStatus()
	if status.Stage != "background" || !status.BackgroundRefreshing {
		t.Fatalf("status stage = %q background=%t, want background", status.Stage, status.BackgroundRefreshing)
	}
	if status.CandidateCount != 1 || status.Total != 4 || status.Pending != 2 || status.Tested != 2 || status.Healthy != 1 || status.Failed != 1 {
		t.Fatalf("status counts = %+v, want candidate=1 total=4 pending=2 tested=2 healthy=1 failed=1", status)
	}
	if status.AvailableCacheTTLSeconds != 40 || status.TestResultTTLSeconds != 300 {
		t.Fatalf("status ttl = %+v, want configured ttl", status)
	}
	if status.CandidateUpdatedAt == nil {
		t.Fatalf("candidate updated at nil, want timestamp")
	}
}

func TestProxiesAPIReportsTestResultTTLFields(t *testing.T) {
	store := newTestStore(t)
	proxies := addDirectTestSubscription(t, store, 4)
	now := time.Now().UTC()
	if err := store.SaveConfig(core.AppConfig{
		TestTarget:           core.DefaultTestTarget,
		TestTimeoutSeconds:   core.DefaultTestTimeoutSeconds,
		TestResultTTLMinutes: 5,
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	for _, result := range []*core.TestResult{
		{Tag: proxies[0].Tag, Latency: 50, Timestamp: now.Add(-1 * time.Minute)},
		{Tag: proxies[1].Tag, Latency: 60, Timestamp: now.Add(-10 * time.Minute)},
		{Tag: proxies[2].Tag, Err: "timeout", Timestamp: now.Add(-1 * time.Minute)},
	} {
		if err := store.SaveTestResult(result); err != nil {
			t.Fatalf("save result: %v", err)
		}
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxies", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET proxies status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rows []struct {
		Tag                     string     `json:"tag"`
		Err                     string     `json:"err"`
		TestTimestamp           *time.Time `json:"test_timestamp"`
		TestAgeSeconds          int        `json:"test_age_seconds"`
		TestTTLSeconds          int        `json:"test_ttl_seconds"`
		TestTTLRemainingSeconds int        `json:"test_ttl_remaining_seconds"`
		TestExpired             bool       `json:"test_expired"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decode proxies: %v", err)
	}
	byTag := make(map[string]struct {
		Err                     string
		TestTimestamp           *time.Time
		TestAgeSeconds          int
		TestTTLSeconds          int
		TestTTLRemainingSeconds int
		TestExpired             bool
	}, len(rows))
	for _, row := range rows {
		byTag[row.Tag] = struct {
			Err                     string
			TestTimestamp           *time.Time
			TestAgeSeconds          int
			TestTTLSeconds          int
			TestTTLRemainingSeconds int
			TestExpired             bool
		}{
			Err:                     row.Err,
			TestTimestamp:           row.TestTimestamp,
			TestAgeSeconds:          row.TestAgeSeconds,
			TestTTLSeconds:          row.TestTTLSeconds,
			TestTTLRemainingSeconds: row.TestTTLRemainingSeconds,
			TestExpired:             row.TestExpired,
		}
	}

	fresh := byTag[proxies[0].Tag]
	if fresh.TestTimestamp == nil || fresh.TestTTLSeconds != 300 || fresh.TestExpired || fresh.TestTTLRemainingSeconds <= 0 {
		t.Fatalf("fresh ttl = %+v, want valid remaining ttl", fresh)
	}
	expired := byTag[proxies[1].Tag]
	if expired.TestTimestamp == nil || !expired.TestExpired || expired.TestTTLRemainingSeconds != 0 {
		t.Fatalf("expired ttl = %+v, want expired", expired)
	}
	failed := byTag[proxies[2].Tag]
	if failed.TestTimestamp == nil || failed.Err == "" || failed.TestExpired || failed.TestTTLRemainingSeconds <= 0 {
		t.Fatalf("failed ttl = %+v, want failed cached ttl", failed)
	}
	untested := byTag[proxies[3].Tag]
	if untested.TestTimestamp != nil || untested.TestTTLSeconds != 300 || untested.TestExpired {
		t.Fatalf("untested ttl = %+v, want no timestamp and configured ttl", untested)
	}
}

func TestAvailableProxiesUseFreshPersistedResultsForWarmPool(t *testing.T) {
	store := newTestStore(t)
	proxies := addDirectTestSubscription(t, store, 5)
	now := time.Now().UTC()
	for i, proxy := range proxies {
		if err := store.SaveTestResult(&core.TestResult{
			Tag:       proxy.Tag,
			Latency:   50 + i,
			Timestamp: now,
		}); err != nil {
			t.Fatalf("save test result: %v", err)
		}
	}

	svc := NewService(core.NewSubscriptionManager(store))
	impl := svc.(*service)
	if impl.tester != nil {
		defer impl.tester.Close()
	}
	defer impl.pool.Stop()

	out, err := impl.GetAvailableProxies(2)
	if err != nil {
		t.Fatalf("GetAvailableProxies() error = %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("available count = %d, want 2: %+v", len(out), out)
	}
	for _, proxy := range out {
		if proxy.Latency < 50 || proxy.Latency > 54 {
			t.Fatalf("available proxy latency = %d, want persisted latency: %+v", proxy.Latency, proxy)
		}
	}

	status := impl.pool.GetStatus()
	if status.Count != len(proxies) {
		t.Fatalf("pool status count = %d, want warm pool size %d", status.Count, len(proxies))
	}
	ports := make(map[string]bool, len(status.Instances))
	for _, instance := range status.Instances {
		ports[instance.HTTP] = true
	}
	for _, proxy := range out {
		if !ports[proxy.HTTP] {
			t.Fatalf("available proxy %q was not bound in pool status %+v", proxy.HTTP, status.Instances)
		}
	}
}

func TestAvailableProxiesSampleDifferentCombinationsFromWarmPool(t *testing.T) {
	store := newTestStore(t)
	proxies := addDirectTestSubscription(t, store, 6)
	now := time.Now().UTC()
	for i, proxy := range proxies {
		if err := store.SaveTestResult(&core.TestResult{
			Tag:       proxy.Tag,
			Latency:   40 + i,
			Timestamp: now,
		}); err != nil {
			t.Fatalf("save test result: %v", err)
		}
	}

	svc := NewService(core.NewSubscriptionManager(store))
	impl := svc.(*service)
	if impl.tester != nil {
		defer impl.tester.Close()
	}
	defer impl.pool.Stop()

	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		out, err := impl.GetAvailableProxies(2)
		if err != nil {
			t.Fatalf("GetAvailableProxies() error = %v", err)
		}
		if len(out) != 2 {
			t.Fatalf("available count = %d, want 2: %+v", len(out), out)
		}
		tags := []string{out[0].Tag, out[1].Tag}
		sort.Strings(tags)
		seen[strings.Join(tags, ",")] = true
		if len(seen) > 1 {
			return
		}
	}
	t.Fatalf("available samples did not vary across requests: %+v", seen)
}

func TestClearingPoolProxyAuthRestartsPoolWithoutAuth(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{{
		Name:       "Direct OK",
		Tag:        "direct-ok",
		Protocol:   "direct",
		Server:     "127.0.0.1",
		ServerPort: 1,
	}}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	impl := svc.(*service)
	if impl.tester != nil {
		defer impl.tester.Close()
	}
	defer impl.pool.Stop()
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"test_target":"`+target.URL+`","available_token":"available-token","pool_proxy_username":"pool-user","pool_proxy_password":"pool-pass"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT auth config status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/proxies/available?count=1&token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET available with auth status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Proxies []server.AvailableProxy `json:"proxies"`
		Count   int                     `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode available: %v", err)
	}
	if body.Count != 1 || len(body.Proxies) != 1 {
		t.Fatalf("available response = %+v, want one proxy", body)
	}
	authProxyURL := body.Proxies[0].HTTP
	if !strings.Contains(authProxyURL, "pool-user:pool-pass@") {
		t.Fatalf("auth proxy url = %q, want credentials", authProxyURL)
	}
	status := impl.pool.GetStatus()
	if status.Count != 1 || len(status.Instances) != 1 {
		t.Fatalf("pool status with auth = %+v, want one instance", status)
	}
	if !strings.Contains(status.Instances[0].HTTP, "pool-user:pool-pass@") {
		t.Fatalf("pool status http with auth = %q, want credentials", status.Instances[0].HTTP)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"test_target":"`+target.URL+`","available_token":"available-token","pool_proxy_username":"","pool_proxy_password":""}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT cleared auth config status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	status = impl.pool.GetStatus()
	if status.Count != 1 || len(status.Instances) != 1 {
		t.Fatalf("pool status immediately after clearing auth = %+v, want one instance", status)
	}
	if strings.Contains(status.Instances[0].HTTP, "@") {
		t.Fatalf("pool status http immediately after clearing auth = %q, should not include credentials", status.Instances[0].HTTP)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/proxies/available.txt?count=1&token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET available.txt after clearing auth status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	hostPort := strings.TrimSpace(rec.Body.String())
	if hostPort == "" {
		t.Fatalf("available.txt body empty, want host:port")
	}
	if strings.Contains(hostPort, "@") {
		t.Fatalf("available.txt body = %q, should not include credentials", hostPort)
	}

	status = impl.pool.GetStatus()
	if status.Count != 1 || len(status.Instances) != 1 {
		t.Fatalf("pool status after clearing auth = %+v, want one instance", status)
	}
	if strings.Contains(status.Instances[0].HTTP, "@") {
		t.Fatalf("pool status http after clearing auth = %q, should not include credentials", status.Instances[0].HTTP)
	}
}

func TestProxyTestAPINotFound(t *testing.T) {
	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{{
		Name:     "Proxy One",
		Tag:      "proxy-one",
		Protocol: "direct",
	}}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proxies/missing/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST missing proxy status = %d, want 404", rec.Code)
	}
}

func TestProxyTestAPINoProxiesUnavailable(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proxies/anything/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST with no proxies status = %d, want 503", rec.Code)
	}
}

func TestLogsAPIListsAndClearsDiagnostics(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/logs?limit=10", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET logs status = %d, want 200", rec.Code)
	}
	var logs []core.DiagnosticEvent
	if err := json.NewDecoder(rec.Body).Decode(&logs); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatalf("logs empty, want init event")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/logs/clear", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST clear logs status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET logs after clear status = %d, want 200", rec.Code)
	}
	if err := json.NewDecoder(rec.Body).Decode(&logs); err != nil {
		t.Fatalf("decode cleared logs: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("logs after clear = %d, want 0", len(logs))
	}
}

func TestProxyTestAPIUsesConfiguredUpstreamForTester(t *testing.T) {
	upstreamURL, connectCount := startCountingHTTPConnectProxy(t)

	const tag = "trojan-one"
	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{{
		Name:       "Trojan One",
		Tag:        tag,
		Protocol:   "trojan",
		Server:     "127.0.0.1",
		ServerPort: 443,
		Password:   "secret",
	}}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"upstream_proxy":"`+upstreamURL+`","test_target":"http://example.com/probe"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/proxies/"+url.PathEscape(tag)+"/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST proxy test status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := connectCount.Load(); got == 0 {
		t.Fatalf("upstream CONNECT count = %d, want > 0", got)
	}
}

func TestProxyTestAPIIgnoresUnsupportedFlowOnOtherProxy(t *testing.T) {
	const ssTag = "ss-one"
	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{
		{
			Name:       "Bad VLESS",
			Tag:        "bad-vless",
			Protocol:   "vless",
			Type:       "vless",
			Server:     "example.com",
			ServerPort: 443,
			UUID:       "00000000-0000-0000-0000-000000000000",
			Flow:       "xtls-rprx-vision-udp443",
		},
		{
			Name:       "SS One",
			Tag:        ssTag,
			Protocol:   "ss",
			Type:       "shadowsocks",
			Server:     "127.0.0.1",
			ServerPort: 1,
			Method:     "aes-256-cfb",
			Password:   "secret",
		},
	}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proxies/"+url.PathEscape(ssTag)+"/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST ss test status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "xtls-rprx-vision-udp443") {
		t.Fatalf("SS test response = %s, should not include other proxy flow", body)
	}
	var result core.TestResult
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Tag != ssTag {
		t.Fatalf("result tag = %q, want %q", result.Tag, ssTag)
	}
}

func TestProxyTestAPIInvalidUpstreamReturnsUnavailable(t *testing.T) {
	const tag = "direct-one"
	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{{
		Name:     "Direct One",
		Tag:      tag,
		Protocol: "direct",
	}}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"upstream_proxy":"ftp://example.com:21"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/proxies/"+url.PathEscape(tag)+"/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST proxy test with invalid upstream status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

func TestProxyTestAPIUpdatesCacheVisibleInProxyList(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	const tag = "proxy one"
	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{{
		Name:       "Proxy One",
		Tag:        tag,
		Protocol:   "direct",
		Server:     "127.0.0.1",
		ServerPort: 1,
	}}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"test_target":"`+target.URL+`"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/proxies/"+url.PathEscape(tag)+"/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST test status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result core.TestResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode test result: %v", err)
	}
	if result.Tag != tag {
		t.Fatalf("result tag = %q, want %q", result.Tag, tag)
	}
	if result.Err != "" {
		t.Fatalf("result err = %q, want empty", result.Err)
	}
	if result.Latency <= 0 {
		t.Fatalf("result latency = %d, want > 0", result.Latency)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/proxies", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET proxies status = %d, want 200", rec.Code)
	}
	var proxies []struct {
		Tag     string `json:"tag"`
		Latency int    `json:"latency"`
		Err     string `json:"err"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&proxies); err != nil {
		t.Fatalf("decode proxies: %v", err)
	}
	if len(proxies) != 1 {
		t.Fatalf("proxy count = %d, want 1", len(proxies))
	}
	if proxies[0].Latency != result.Latency {
		t.Fatalf("cached latency = %d, want %d", proxies[0].Latency, result.Latency)
	}
	if proxies[0].Err != "" {
		t.Fatalf("cached err = %q, want empty", proxies[0].Err)
	}
}

func TestProxyTestAPIFailureResultVisibleInProxyList(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	target := "http://" + listener.Addr().String()
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		conn.Close()
	}()
	defer func() {
		listener.Close()
		<-accepted
	}()

	const tag = "proxy-fails"
	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{{
		Name:     "Proxy Fails",
		Tag:      tag,
		Protocol: "direct",
	}}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"test_target":"`+target+`"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/proxies/"+url.PathEscape(tag)+"/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST failed test status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result core.TestResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode test result: %v", err)
	}
	if result.Err == "" {
		t.Fatalf("result err empty, want failure")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/proxies", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET proxies status = %d, want 200", rec.Code)
	}
	var proxies []struct {
		Latency int    `json:"latency"`
		Err     string `json:"err"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&proxies); err != nil {
		t.Fatalf("decode proxies: %v", err)
	}
	if len(proxies) != 1 {
		t.Fatalf("proxy count = %d, want 1", len(proxies))
	}
	if proxies[0].Err == "" {
		t.Fatalf("cached err empty, want failure")
	}
	if proxies[0].Latency != 0 {
		t.Fatalf("failed latency = %d, want 0", proxies[0].Latency)
	}
}

func TestProxyTestAPIUsesConfiguredTimeout(t *testing.T) {
	target := startSlowHTTPServer(t, 1500*time.Millisecond)

	const tag = "proxy-timeout"
	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{{
		Name:       "Proxy Timeout",
		Tag:        tag,
		Protocol:   "direct",
		Server:     "127.0.0.1",
		ServerPort: 1,
	}}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"test_target":"`+target+`","test_timeout_seconds":1,"available_token":"available-token"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/proxies/"+url.PathEscape(tag)+"/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST test status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result core.TestResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode test result: %v", err)
	}
	if result.Err == "" {
		t.Fatalf("result err empty, want timeout")
	}
	if result.Latency != 0 {
		t.Fatalf("timeout latency = %d, want 0", result.Latency)
	}
}

func TestAvailableProxiesAPIUsesConfiguredTimeout(t *testing.T) {
	target := startSlowHTTPServer(t, 1500*time.Millisecond)

	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{{
		Name:       "Proxy Timeout",
		Tag:        "proxy-timeout",
		Protocol:   "direct",
		Server:     "127.0.0.1",
		ServerPort: 1,
	}}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"test_target":"`+target+`","test_timeout_seconds":1,"available_token":"available-token"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/proxies/available?count=1&token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET available status = %d, want 503: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/proxies", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET proxies status = %d, want 200", rec.Code)
	}
	var proxies []struct {
		Err     string `json:"err"`
		Latency int    `json:"latency"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&proxies); err != nil {
		t.Fatalf("decode proxies: %v", err)
	}
	if len(proxies) != 1 {
		t.Fatalf("proxy count = %d, want 1", len(proxies))
	}
	if proxies[0].Err == "" {
		t.Fatalf("cached err empty, want timeout")
	}
	if proxies[0].Latency != 0 {
		t.Fatalf("timeout latency = %d, want 0", proxies[0].Latency)
	}
}

func TestAvailableProxiesAPISkipsUnsupportedFlowProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{
		{
			Name:       "Bad VLESS",
			Tag:        "bad-vless",
			Protocol:   "vless",
			Type:       "vless",
			Server:     "example.com",
			ServerPort: 443,
			UUID:       "00000000-0000-0000-0000-000000000000",
			Flow:       "xtls-rprx-vision-udp443",
		},
		{
			Name:       "Direct OK",
			Tag:        "direct-ok",
			Protocol:   "direct",
			Server:     "127.0.0.1",
			ServerPort: 1,
		},
	}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"test_target":"`+target.URL+`","available_token":"available-token"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/proxies/available?count=1&token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET available status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Proxies []server.AvailableProxy `json:"proxies"`
		Count   int                     `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode available: %v", err)
	}
	if body.Count != 1 || len(body.Proxies) != 1 || body.Proxies[0].Tag != "direct-ok" {
		t.Fatalf("available response = %+v, want direct-ok only", body)
	}
}

func TestAvailableProxiesAPIInvalidUpstreamReturnsUnavailable(t *testing.T) {
	store := newTestStore(t)
	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = []*core.ProxyInfo{{
		Name:     "Direct One",
		Tag:      "direct-one",
		Protocol: "direct",
	}}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}

	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"upstream_proxy":"ftp://example.com:21","available_token":"available-token"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/proxies/available?count=1&token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET available with invalid upstream status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

func TestUpstreamTestAPIUsesSavedConfigForEmptyBody(t *testing.T) {
	upstreamURL := startFakeHTTPConnectProxy(t)

	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"upstream_proxy":"`+upstreamURL+`","test_target":"http://example.com/probe"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/config/upstream/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST upstream test status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result core.TestResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Tag != "upstream" {
		t.Fatalf("result tag = %q, want upstream", result.Tag)
	}
	if result.Err != "" {
		t.Fatalf("result err = %q, want empty", result.Err)
	}
}

func TestUpstreamTestAPIUsesDefaultTimeout(t *testing.T) {
	upstreamURL := startDelayedHTTPConnectProxy(t, 1500*time.Millisecond)

	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"upstream_proxy":"`+upstreamURL+`","test_target":"http://example.com/probe","test_timeout_seconds":1}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/config/upstream/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST upstream test status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result core.TestResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Err != "" {
		t.Fatalf("result err = %q, want empty", result.Err)
	}
}

func TestUpstreamTestAPITemporaryBodyDoesNotModifyConfig(t *testing.T) {
	upstreamURL := startFakeHTTPConnectProxy(t)
	const savedUpstream = "http://saved.example:8080"

	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"upstream_proxy":"`+savedUpstream+`"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/config/upstream/test", strings.NewReader(`{"upstream_proxy":"`+upstreamURL+`","test_target":"http://example.com/probe"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST upstream test status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var result core.TestResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Err != "" {
		t.Fatalf("result err = %q, want empty", result.Err)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config status = %d, want 200", rec.Code)
	}
	var cfg core.AppConfig
	if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.UpstreamProxy != savedUpstream {
		t.Fatalf("upstream_proxy = %q, want %q", cfg.UpstreamProxy, savedUpstream)
	}
}

func TestUpstreamTestAPIInvalidUpstreamBadRequest(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/upstream/test", strings.NewReader(`{"upstream_proxy":"ftp://example.com:21"}`))
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid upstream status = %d, want 400", rec.Code)
	}
}

func TestUpstreamTestAPINoUpstreamBadRequest(t *testing.T) {
	store := newTestStore(t)
	svc := NewService(core.NewSubscriptionManager(store))
	if impl, ok := svc.(*service); ok && impl.tester != nil {
		defer impl.tester.Close()
	}
	srv := server.NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/upstream/test", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST no upstream status = %d, want 400", rec.Code)
	}
}

func addDirectTestSubscription(t *testing.T, store *core.Store, count int) []*core.ProxyInfo {
	t.Helper()

	sub := core.NewSubscription("http://example.invalid/sub")
	sub.Proxies = make([]*core.ProxyInfo, 0, count)
	for i := 0; i < count; i++ {
		tag := fmt.Sprintf("direct-%02d", i)
		sub.Proxies = append(sub.Proxies, &core.ProxyInfo{
			Name:       fmt.Sprintf("Direct %02d", i),
			Tag:        tag,
			Protocol:   "direct",
			Server:     "127.0.0.1",
			ServerPort: 1,
		})
	}
	if err := store.AddSubscription(sub); err != nil {
		t.Fatalf("add subscription: %v", err)
	}
	return sub.Proxies
}

func startSlowHTTPServer(t *testing.T, delay time.Duration) string {
	t.Helper()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(target.Close)
	return target.URL
}

func startFakeHTTPConnectProxy(t *testing.T) string {
	return startDelayedHTTPConnectProxy(t, 0)
}

func startDelayedHTTPConnectProxy(t *testing.T, delay time.Duration) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake upstream: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveDelayedHTTPConnectProxyConn(conn, delay)
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-done
	})
	return "http://" + listener.Addr().String()
}

func startCountingHTTPConnectProxy(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen counting upstream: %v", err)
	}
	var connectCount atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveCountingHTTPConnectProxyConn(conn, &connectCount)
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-done
	})
	return "http://" + listener.Addr().String(), &connectCount
}

func serveFakeHTTPConnectProxyConn(conn net.Conn) {
	serveDelayedHTTPConnectProxyConn(conn, 0)
}

func serveDelayedHTTPConnectProxyConn(conn net.Conn, delay time.Duration) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "CONNECT ") {
		return
	}
	if !drainHTTPHeaders(reader) {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	line, err = reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "GET ") {
		return
	}
	if !drainHTTPHeaders(reader) {
		return
	}
	time.Sleep(delay)
	conn.Write([]byte("HTTP/1.1 204 No Content\r\n\r\n"))
}

func serveCountingHTTPConnectProxyConn(conn net.Conn, connectCount *atomic.Int32) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "CONNECT ") {
		return
	}
	connectCount.Add(1)
	if !drainHTTPHeaders(reader) {
		return
	}
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
}

func drainHTTPHeaders(reader *bufio.Reader) bool {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		if line == "\r\n" {
			return true
		}
	}
}
