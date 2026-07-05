package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"http-proxy/core"
	"http-proxy/server"
)

type availableCandidate struct {
	proxy     *core.ProxyInfo
	port      int
	latency   int
	updatedAt time.Time
}

type availableCandidatePool struct {
	entries    []availableCandidate
	updatedAt  time.Time
	poolConfig poolRuntimeConfig
	generation uint64
}

type poolRuntimeConfig struct {
	upstream string
	username string
	password string
}

type availableRuntimeSettings struct {
	cacheTTL              time.Duration
	testResultTTL         time.Duration
	quickProbeBudget      time.Duration
	quickConcurrency      int
	backgroundConcurrency int
	minWarmPoolSize       int
}

type service struct {
	subMgr *core.SubscriptionManager
	store  *core.Store
	tester *core.Tester
	pool   *core.ProxyPool
	config core.AppConfig
	logs   *core.DiagnosticLog
	reqLog *core.RequestLogStore

	quickRefreshInProgress atomic.Bool
	refreshInProgress      atomic.Bool
	availableGeneration    atomic.Uint64
	availablePool          *availableCandidatePool
	availableMu            sync.RWMutex
	availableStatusMu      sync.RWMutex
	availableLastRefreshAt time.Time
	availableLastError     string
	configMu               sync.RWMutex
}

func NewService(subMgr *core.SubscriptionManager) server.Service {
	store := subMgr.Store()
	allProxies := subMgr.GetAllProxies()
	diagnostics := core.NewDiagnosticLog(core.DefaultDiagnosticLogCapacity)
	cfg := core.AppConfig{}
	cfg.Default()
	if store != nil {
		loaded, err := store.LoadConfig()
		if err != nil {
			log.Printf("Warning: could not load config: %v", err)
		} else {
			cfg = loaded
		}
	}

	validProxies, invalidResults := filterSupportedProxies(allProxies, diagnostics)
	tester, err := core.NewTesterWithStore(validProxies, strings.TrimSpace(cfg.UpstreamProxy), store)
	if err != nil {
		log.Printf("Warning: could not create tester: %v", err)
		diagnostics.Add(core.DiagnosticEvent{
			Level:   "error",
			Scope:   "tester",
			Stage:   "build",
			Message: "测速器初始化失败",
			Detail:  err.Error(),
		})
	}
	if tester != nil && cfg.TestTarget != "" {
		tester.SetTestTarget(cfg.TestTarget)
		tester.SetTestTimeout(core.TestTimeoutDuration(cfg.TestTimeoutSeconds))
		tester.RecordResults(invalidResults)
	} else if store != nil && len(invalidResults) > 0 {
		if err := store.SaveTestResults(invalidResults); err != nil {
			log.Printf("Warning: could not persist invalid proxy results: %v", err)
		}
	}
	pool := core.NewProxyPool()
	pool.SetRuntimeConfig(cfg.UpstreamProxy, cfg.PoolProxyUsername, cfg.PoolProxyPassword)
	requestLogs := core.NewRequestLogStore(core.DefaultRequestLogDir)
	pool.SetRequestLogRecorder(requestLogs)
	log.Printf("[service] initialized with tester (%d proxy outbounds, %d valid)", len(allProxies), len(validProxies))
	diagnostics.Add(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "service",
		Stage:   "init",
		Message: fmt.Sprintf("服务初始化完成：总节点 %d，有效节点 %d", len(allProxies), len(validProxies)),
	})
	return &service{
		subMgr: subMgr,
		store:  store,
		tester: tester,
		pool:   pool,
		config: cfg,
		logs:   diagnostics,
		reqLog: requestLogs,
	}
}

func (s *service) ListSubscriptions() []*core.Subscription {
	return s.subMgr.List()
}

func (s *service) AddSubscription(url string) (*core.Subscription, error) {
	sub, err := s.subMgr.Add(url)
	if err != nil {
		s.addLog(core.DiagnosticEvent{
			Level:   "error",
			Scope:   "subscription",
			Stage:   "add",
			Message: "添加订阅失败",
			Detail:  err.Error(),
		})
		return nil, err
	}
	s.addLog(core.DiagnosticEvent{
		Level:    "info",
		Scope:    "subscription",
		SourceID: sub.ID,
		Stage:    "add",
		Message:  fmt.Sprintf("订阅已添加：%d 个节点", len(sub.Proxies)),
		Detail:   sub.URL,
	})
	s.invalidateAvailableCandidates()
	if _, err := s.syncTester(); err != nil {
		s.addLog(core.DiagnosticEvent{
			Level:   "error",
			Scope:   "tester",
			Stage:   "sync",
			Message: "添加订阅后同步测速器失败",
			Detail:  err.Error(),
		})
	}
	return sub, nil
}

func (s *service) RemoveSubscription(id string) error {
	err := s.subMgr.Remove(id)
	if err != nil {
		s.addLog(core.DiagnosticEvent{
			Level:    "error",
			Scope:    "subscription",
			SourceID: id,
			Stage:    "delete",
			Message:  "删除订阅失败",
			Detail:   err.Error(),
		})
		return err
	}
	s.addLog(core.DiagnosticEvent{
		Level:    "info",
		Scope:    "subscription",
		SourceID: id,
		Stage:    "delete",
		Message:  "订阅已删除",
	})
	s.invalidateAvailableCandidates()
	if _, err := s.syncTester(); err != nil {
		s.addLog(core.DiagnosticEvent{
			Level:   "error",
			Scope:   "tester",
			Stage:   "sync",
			Message: "删除订阅后同步测速器失败",
			Detail:  err.Error(),
		})
	}
	return nil
}

func (s *service) RefreshSubscription(id string) (*core.Subscription, error) {
	sub, err := s.subMgr.Refresh(id)
	if err != nil {
		s.addLog(core.DiagnosticEvent{
			Level:    "error",
			Scope:    "subscription",
			SourceID: id,
			Stage:    "refresh",
			Message:  "刷新订阅失败",
			Detail:   err.Error(),
		})
		return nil, err
	}
	s.addLog(core.DiagnosticEvent{
		Level:    "info",
		Scope:    "subscription",
		SourceID: sub.ID,
		Stage:    "refresh",
		Message:  fmt.Sprintf("订阅已刷新：%d 个节点", len(sub.Proxies)),
		Detail:   sub.URL,
	})
	s.invalidateAvailableCandidates()
	if _, err := s.syncTester(); err != nil {
		s.addLog(core.DiagnosticEvent{
			Level:   "error",
			Scope:   "tester",
			Stage:   "sync",
			Message: "刷新订阅后同步测速器失败",
			Detail:  err.Error(),
		})
	}
	return sub, nil
}

func (s *service) GetAllProxies() []*core.ProxyInfo {
	return s.subMgr.GetAllProxies()
}

func (s *service) GetAvailableProxies(count int) ([]server.AvailableProxy, error) {
	count = normalizeAvailableCount(count)
	now := time.Now()
	log.Printf("[service] GetAvailableProxies(count=%d)", count)

	poolConfig, settings := s.currentAvailableRuntime()
	if out, size := s.sampleAvailableCandidates(count, poolConfig, now, settings); availablePoolHasEnough(size, count) {
		log.Printf("[service] available candidate hit (%d proxies)", size)
		generation := s.availableGeneration.Load()
		if s.availableCandidatesNeedRefresh(poolConfig, now, settings) {
			s.startAvailableRefreshFromCurrent(count, poolConfig, generation, settings)
		}
		return out, nil
	}

	log.Printf("[service] upstream=%q", poolConfig.upstream)

	proxies := s.subMgr.GetAllProxies()
	log.Printf("[service] total proxies from subs: %d", len(proxies))
	if len(proxies) == 0 {
		log.Printf("[service] no proxies, returning nil")
		return nil, nil
	}

	validProxies, invalidResults := filterSupportedProxies(proxies, s.logs)
	if s.tester != nil {
		s.tester.RecordResults(invalidResults)
	} else if s.store != nil && len(invalidResults) > 0 {
		if err := s.store.SaveTestResults(invalidResults); err != nil {
			log.Printf("[service] could not persist invalid proxy results: %v", err)
		}
	}
	if len(validProxies) == 0 {
		s.addLog(core.DiagnosticEvent{
			Level:   "warn",
			Scope:   "tester",
			Stage:   "validate",
			Message: "没有可测速的有效节点",
		})
		return nil, nil
	}

	generation := s.availableGeneration.Load()
	var fallback []server.AvailableProxy
	if s.rebuildAvailableCandidatesFromResults(validProxies, s.currentTestResults(), count, poolConfig, generation, settings) {
		if out, size := s.sampleAvailableCandidates(count, poolConfig, time.Now(), settings); availablePoolHasEnough(size, count) {
			return out, nil
		} else if len(out) > 0 {
			fallback = out
		}
	}

	if err := s.prepareAvailableTester(validProxies); err != nil {
		if len(fallback) > 0 && s.availableGeneration.Load() == generation && s.currentPoolRuntimeConfig() == poolConfig {
			return fallback, nil
		}
		return nil, err
	}

	s.quickRefreshAvailableCandidates(validProxies, count, poolConfig, generation, settings)
	if out, size := s.sampleAvailableCandidates(count, poolConfig, time.Now(), settings); availablePoolHasEnough(size, count) || len(out) > 0 {
		if !availablePoolHasEnough(size, count) {
			s.startAvailableRefresh(validProxies, count, poolConfig, generation, settings)
		}
		return out, nil
	}
	if len(fallback) > 0 && s.availableGeneration.Load() == generation && s.currentPoolRuntimeConfig() == poolConfig {
		s.startAvailableRefresh(validProxies, count, poolConfig, generation, settings)
		return fallback, nil
	}

	s.startAvailableRefresh(validProxies, count, poolConfig, generation, settings)

	log.Printf("[service] no healthy proxies")
	return nil, nil
}

func (s *service) GetAvailableStatus() server.AvailableStatus {
	now := time.Now()
	poolConfig, settings := s.currentAvailableRuntime()
	candidateCount, candidateUpdatedAt := s.availableCandidateStatus(poolConfig, now, settings)

	results := s.currentTestResults()
	proxies := s.subMgr.GetAllProxies()
	total := 0
	pending := 0
	tested := 0
	healthy := 0
	failed := 0
	for _, proxy := range proxies {
		if proxy == nil || proxy.Tag == "" {
			continue
		}
		total++
		result, ok := results[proxy.Tag]
		if !ok || result == nil || result.Timestamp.IsZero() || now.Sub(result.Timestamp) > settings.testResultTTL {
			pending++
			continue
		}
		tested++
		if result.Err == "" && result.Latency > 0 && result.Latency < core.MaxLatencyMs {
			healthy++
		} else {
			failed++
		}
	}

	quickRefreshing := s.quickRefreshInProgress.Load()
	backgroundRefreshing := s.refreshInProgress.Load()
	stage := "idle"
	if quickRefreshing {
		stage = "quick"
	} else if backgroundRefreshing {
		stage = "background"
	}

	s.availableStatusMu.RLock()
	lastRefreshAt := timePointer(s.availableLastRefreshAt)
	lastError := s.availableLastError
	s.availableStatusMu.RUnlock()

	return server.AvailableStatus{
		Stage:                    stage,
		CandidateCount:           candidateCount,
		CandidateUpdatedAt:       candidateUpdatedAt,
		QuickRefreshing:          quickRefreshing,
		BackgroundRefreshing:     backgroundRefreshing,
		Total:                    total,
		Pending:                  pending,
		Tested:                   tested,
		Healthy:                  healthy,
		Failed:                   failed,
		AvailableCacheTTLSeconds: int(settings.cacheTTL / time.Second),
		TestResultTTLSeconds:     int(settings.testResultTTL / time.Second),
		LastRefreshAt:            lastRefreshAt,
		LastError:                lastError,
	}
}

func (s *service) availableCandidateStatus(poolConfig poolRuntimeConfig, now time.Time, settings availableRuntimeSettings) (int, *time.Time) {
	s.availableMu.RLock()
	defer s.availableMu.RUnlock()

	if s.availablePool == nil || s.availablePool.poolConfig != poolConfig {
		return 0, nil
	}
	count := 0
	for _, candidate := range s.availablePool.entries {
		if candidate.proxy == nil || candidate.port == 0 || candidate.updatedAt.IsZero() {
			continue
		}
		if now.Sub(candidate.updatedAt) > settings.testResultTTL {
			continue
		}
		count++
	}
	return count, timePointer(s.availablePool.updatedAt)
}

func timePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func normalizeAvailableCount(count int) int {
	if count <= 0 {
		return core.DefaultProxyCount
	}
	return count
}

func availablePoolCapacity() int {
	return core.PoolPortEnd - core.PoolPortStart + 1
}

func availablePoolHasEnough(size int, count int) bool {
	needed := normalizeAvailableCount(count)
	if capacity := availablePoolCapacity(); needed > capacity {
		needed = capacity
	}
	return size >= needed
}

func availableWarmPoolTarget(count int, total int, minWarmPoolSize int) int {
	count = normalizeAvailableCount(count)
	target := count * 3
	if target < minWarmPoolSize {
		target = minWarmPoolSize
	}
	if capacity := availablePoolCapacity(); target > capacity {
		target = capacity
	}
	if target > total {
		target = total
	}
	return target
}

func (s *service) sampleAvailableCandidates(count int, poolConfig poolRuntimeConfig, now time.Time, settings availableRuntimeSettings) ([]server.AvailableProxy, int) {
	count = normalizeAvailableCount(count)

	s.availableMu.RLock()
	defer s.availableMu.RUnlock()

	if s.availablePool == nil || s.availablePool.poolConfig != poolConfig {
		return nil, 0
	}

	candidates := make([]availableCandidate, 0, len(s.availablePool.entries))
	for _, candidate := range s.availablePool.entries {
		if candidate.proxy == nil || candidate.port == 0 || candidate.updatedAt.IsZero() {
			continue
		}
		if now.Sub(candidate.updatedAt) > settings.testResultTTL {
			continue
		}
		candidates = append(candidates, candidate)
	}
	size := len(candidates)
	if size == 0 {
		return nil, 0
	}

	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	if len(candidates) > count {
		candidates = candidates[:count]
	}

	out := make([]server.AvailableProxy, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, availableProxyFromCandidate(candidate, poolConfig))
	}
	return out, size
}

func availableProxyFromCandidate(candidate availableCandidate, poolConfig poolRuntimeConfig) server.AvailableProxy {
	return server.AvailableProxy{
		HTTP:     core.FormatHTTPProxyURL(candidate.port, poolConfig.username, poolConfig.password),
		Name:     candidate.proxy.Name,
		Tag:      candidate.proxy.Tag,
		Latency:  candidate.latency,
		Protocol: candidate.proxy.Protocol,
	}
}

func (s *service) currentTestResults() map[string]*core.TestResult {
	if s.tester != nil {
		return s.tester.GetResults()
	}
	if s.store == nil {
		return nil
	}
	results, err := s.store.LoadTestResults()
	if err != nil {
		log.Printf("[service] could not load test results: %v", err)
		return nil
	}
	return results
}

func (s *service) rebuildAvailableCandidatesFromResults(proxies []*core.ProxyInfo, results map[string]*core.TestResult, count int, poolConfig poolRuntimeConfig, generation uint64, settings availableRuntimeSettings) bool {
	target := availableWarmPoolTarget(count, len(proxies), settings.minWarmPoolSize)
	selected, latencies, timestamps := selectFreshHealthyProxies(proxies, results, target, time.Now(), settings.testResultTTL)
	if len(selected) == 0 {
		return false
	}
	return s.bindAvailableCandidates(selected, latencies, timestamps, poolConfig, generation)
}

func selectFreshHealthyProxies(proxies []*core.ProxyInfo, results map[string]*core.TestResult, count int, now time.Time, testResultTTL time.Duration) ([]*core.ProxyInfo, map[string]int, map[string]time.Time) {
	if count <= 0 || len(proxies) == 0 || len(results) == 0 {
		return nil, nil, nil
	}

	type scored struct {
		proxy     *core.ProxyInfo
		latency   int
		timestamp time.Time
	}
	healthy := make([]scored, 0, len(proxies))
	for _, proxy := range proxies {
		if proxy == nil || proxy.Tag == "" {
			continue
		}
		result, ok := results[proxy.Tag]
		if !ok || result == nil {
			continue
		}
		if result.Err != "" || result.Latency <= 0 || result.Latency >= core.MaxLatencyMs {
			continue
		}
		if result.Timestamp.IsZero() || now.Sub(result.Timestamp) > testResultTTL {
			continue
		}
		healthy = append(healthy, scored{
			proxy:     proxy,
			latency:   result.Latency,
			timestamp: result.Timestamp,
		})
	}
	if len(healthy) == 0 {
		return nil, nil, nil
	}

	sort.Slice(healthy, func(i, j int) bool {
		return healthy[i].latency < healthy[j].latency
	})
	window := len(healthy)
	if maxWindow := count * 3; window > maxWindow {
		window = maxWindow
	}
	candidates := append([]scored(nil), healthy[:window]...)
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	if len(candidates) > count {
		candidates = candidates[:count]
	}

	selected := make([]*core.ProxyInfo, 0, len(candidates))
	latencies := make(map[string]int, len(candidates))
	timestamps := make(map[string]time.Time, len(candidates))
	for _, candidate := range candidates {
		selected = append(selected, candidate.proxy)
		latencies[candidate.proxy.Tag] = candidate.latency
		timestamps[candidate.proxy.Tag] = candidate.timestamp
	}
	return selected, latencies, timestamps
}

func (s *service) bindAvailableCandidates(proxies []*core.ProxyInfo, latencies map[string]int, timestamps map[string]time.Time, poolConfig poolRuntimeConfig, generation uint64) bool {
	if len(proxies) == 0 {
		return false
	}

	s.configMu.RLock()
	defer s.configMu.RUnlock()

	cfg := s.config
	currentPoolConfig := poolRuntimeConfigFromConfig(cfg)
	if currentPoolConfig != poolConfig || s.availableGeneration.Load() != generation {
		return false
	}

	s.availableMu.Lock()
	defer s.availableMu.Unlock()
	if s.availableGeneration.Load() != generation {
		return false
	}

	log.Printf("[service] hot-swapping warm pool (%d instances)...", len(proxies))
	s.pool.SetRuntimeConfig(poolConfig.upstream, poolConfig.username, poolConfig.password)
	s.pool.HotSwap(proxies, latencies)

	tags := make([]string, len(proxies))
	for i, proxy := range proxies {
		tags[i] = proxy.Tag
	}
	ports := s.pool.GetPorts(tags)

	entries := make([]availableCandidate, 0, len(proxies))
	for _, proxy := range proxies {
		port, ok := ports[proxy.Tag]
		if !ok {
			continue
		}
		updatedAt := timestamps[proxy.Tag]
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		entries = append(entries, availableCandidate{
			proxy:     proxy,
			port:      port,
			latency:   latencies[proxy.Tag],
			updatedAt: updatedAt,
		})
	}
	if len(entries) == 0 {
		return false
	}

	s.availablePool = &availableCandidatePool{
		entries:    entries,
		updatedAt:  time.Now().UTC(),
		poolConfig: poolConfig,
		generation: generation,
	}
	log.Printf("[service] warm pool ready: %d candidates", len(entries))
	s.addLog(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "pool",
		Stage:   "select",
		Message: fmt.Sprintf("代理池候选池更新：%d 个健康节点", len(entries)),
	})
	return true
}

func (s *service) prepareAvailableTester(validProxies []*core.ProxyInfo) error {
	log.Printf("[service] syncing tester for available refresh...")
	syncedProxies, err := s.syncTester()
	if err != nil || s.tester == nil {
		if err == nil {
			err = fmt.Errorf("tester unavailable")
		}
		s.addLog(core.DiagnosticEvent{
			Level:   "error",
			Scope:   "tester",
			Stage:   "sync",
			Message: "测速器同步失败",
			Detail:  err.Error(),
		})
		return fmt.Errorf("tester unavailable: %w", err)
	}
	if len(syncedProxies) == 0 && len(validProxies) > 0 {
		return fmt.Errorf("tester unavailable: no valid proxies")
	}
	return nil
}

func (s *service) quickRefreshAvailableCandidates(validProxies []*core.ProxyInfo, count int, poolConfig poolRuntimeConfig, generation uint64, settings availableRuntimeSettings) {
	if s.tester == nil {
		return
	}
	if !s.quickRefreshInProgress.CompareAndSwap(false, true) {
		return
	}
	defer s.quickRefreshInProgress.Store(false)

	if s.availableGeneration.Load() != generation || s.currentPoolRuntimeConfig() != poolConfig {
		return
	}
	toTest := proxiesNeedingTest(validProxies, s.currentTestResults(), time.Now(), settings.testResultTTL)
	if len(toTest) == 0 {
		return
	}
	rand.Shuffle(len(toTest), func(i, j int) {
		toTest[i], toTest[j] = toTest[j], toTest[i]
	})

	s.addLog(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "tester",
		Stage:   "quick",
		Message: fmt.Sprintf("开始快速探测：最多 %s", settings.quickProbeBudget),
	})
	results := s.tester.TestProxiesWithBudgetAndConcurrency(toTest, settings.quickProbeBudget, settings.quickConcurrency)
	s.markAvailableRefreshResult(nil)
	log.Printf("[service] quick probe complete: %d results", len(results))
	if len(results) == 0 {
		return
	}
	s.rebuildAvailableCandidatesFromResults(validProxies, s.currentTestResults(), count, poolConfig, generation, settings)
}

func proxiesNeedingTest(proxies []*core.ProxyInfo, results map[string]*core.TestResult, now time.Time, testResultTTL time.Duration) []*core.ProxyInfo {
	toTest := make([]*core.ProxyInfo, 0, len(proxies))
	for _, proxy := range proxies {
		if proxy == nil || proxy.Tag == "" {
			continue
		}
		result, ok := results[proxy.Tag]
		if !ok || result == nil || result.Timestamp.IsZero() || now.Sub(result.Timestamp) > testResultTTL {
			toTest = append(toTest, proxy)
		}
	}
	return toTest
}

func (s *service) startAvailableRefresh(validProxies []*core.ProxyInfo, count int, poolConfig poolRuntimeConfig, generation uint64, settings availableRuntimeSettings) {
	if len(validProxies) == 0 {
		return
	}
	if !s.refreshInProgress.CompareAndSwap(false, true) {
		return
	}
	proxies := append([]*core.ProxyInfo(nil), validProxies...)
	go func() {
		defer s.refreshInProgress.Store(false)
		s.refreshAvailableCandidates(proxies, count, poolConfig, generation, settings)
	}()
}

func (s *service) refreshAvailableCandidates(validProxies []*core.ProxyInfo, count int, poolConfig poolRuntimeConfig, generation uint64, settings availableRuntimeSettings) {
	if s.availableGeneration.Load() != generation || s.currentPoolRuntimeConfig() != poolConfig {
		return
	}
	if err := s.prepareAvailableTester(validProxies); err != nil {
		log.Printf("[service] background available refresh skipped: %v", err)
		s.markAvailableRefreshResult(err)
		return
	}

	target := availableWarmPoolTarget(count, len(validProxies), settings.minWarmPoolSize)
	toTest := proxiesNeedingTest(validProxies, s.currentTestResults(), time.Now(), settings.testResultTTL)
	rand.Shuffle(len(toTest), func(i, j int) {
		toTest[i], toTest[j] = toTest[j], toTest[i]
	})
	if len(toTest) == 0 {
		s.rebuildAvailableCandidatesFromResults(validProxies, s.currentTestResults(), count, poolConfig, generation, settings)
		s.markAvailableRefreshResult(nil)
		return
	}

	s.addLog(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "tester",
		Stage:   "background",
		Message: fmt.Sprintf("后台刷新候选池：%d 个待测节点", len(toTest)),
	})
	for start := 0; start < len(toTest); start += settings.backgroundConcurrency {
		if s.availableGeneration.Load() != generation || s.currentPoolRuntimeConfig() != poolConfig {
			return
		}
		end := start + settings.backgroundConcurrency
		if end > len(toTest) {
			end = len(toTest)
		}
		results := s.tester.TestProxiesWithConcurrency(toTest[start:end], settings.backgroundConcurrency)
		log.Printf("[service] background available batch complete: %d results", len(results))
		s.rebuildAvailableCandidatesFromResults(validProxies, s.currentTestResults(), count, poolConfig, generation, settings)
		if s.availableCandidateSize(poolConfig, time.Now(), settings) >= target {
			s.markAvailableRefreshResult(nil)
			return
		}
	}
	s.markAvailableRefreshResult(nil)
}

func (s *service) availableCandidateSize(poolConfig poolRuntimeConfig, now time.Time, settings availableRuntimeSettings) int {
	_, size := s.sampleAvailableCandidates(availablePoolCapacity(), poolConfig, now, settings)
	return size
}

func (s *service) availableCandidatesNeedRefresh(poolConfig poolRuntimeConfig, now time.Time, settings availableRuntimeSettings) bool {
	s.availableMu.RLock()
	defer s.availableMu.RUnlock()

	if s.availablePool == nil || s.availablePool.poolConfig != poolConfig {
		return false
	}
	return now.Sub(s.availablePool.updatedAt) > settings.cacheTTL
}

func (s *service) startAvailableRefreshFromCurrent(count int, poolConfig poolRuntimeConfig, generation uint64, settings availableRuntimeSettings) {
	if !s.refreshInProgress.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.refreshInProgress.Store(false)
		if s.availableGeneration.Load() != generation || s.currentPoolRuntimeConfig() != poolConfig {
			return
		}
		proxies := s.subMgr.GetAllProxies()
		validProxies, invalidResults := filterSupportedProxies(proxies, s.logs)
		if s.tester != nil {
			s.tester.RecordResults(invalidResults)
		} else if s.store != nil && len(invalidResults) > 0 {
			if err := s.store.SaveTestResults(invalidResults); err != nil {
				log.Printf("[service] could not persist invalid proxy results: %v", err)
			}
		}
		s.refreshAvailableCandidates(validProxies, count, poolConfig, generation, settings)
	}()
}

func (s *service) invalidateAvailableCandidates() {
	s.availableMu.Lock()
	defer s.availableMu.Unlock()
	s.availableGeneration.Add(1)
	s.availablePool = nil
}

func (s *service) currentPoolRuntimeConfig() poolRuntimeConfig {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return poolRuntimeConfigFromConfig(s.config)
}

func (s *service) currentAvailableRuntime() (poolRuntimeConfig, availableRuntimeSettings) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	cfg := s.config
	return poolRuntimeConfigFromConfig(cfg), availableRuntimeSettingsFromConfig(cfg)
}

func poolRuntimeConfigFromConfig(cfg core.AppConfig) poolRuntimeConfig {
	return poolRuntimeConfig{
		upstream: strings.TrimSpace(cfg.UpstreamProxy),
		username: strings.TrimSpace(cfg.PoolProxyUsername),
		password: strings.TrimSpace(cfg.PoolProxyPassword),
	}
}

func availableRuntimeSettingsFromConfig(cfg core.AppConfig) availableRuntimeSettings {
	return availableRuntimeSettings{
		cacheTTL:              core.AvailableCacheTTLDuration(cfg.AvailableCacheTTLSeconds),
		testResultTTL:         core.TestResultTTLDuration(cfg.TestResultTTLMinutes),
		quickProbeBudget:      core.AvailableQuickProbeDuration(cfg.AvailableQuickProbeSeconds),
		quickConcurrency:      core.NormalizeAvailableQuickConcurrency(cfg.AvailableQuickConcurrency, core.DefaultAvailableQuickConcurrency),
		backgroundConcurrency: core.NormalizeAvailableBackgroundConcurrency(cfg.AvailableBackgroundConcurrency, core.DefaultAvailableBackgroundConcurrency),
		minWarmPoolSize:       core.NormalizeAvailableMinWarmPoolSize(cfg.AvailableMinWarmPoolSize, core.DefaultAvailableMinWarmPoolSize),
	}
}

func (s *service) markAvailableRefreshResult(err error) {
	s.availableStatusMu.Lock()
	defer s.availableStatusMu.Unlock()
	s.availableLastRefreshAt = time.Now().UTC()
	if err != nil {
		s.availableLastError = err.Error()
		return
	}
	s.availableLastError = ""
}

func (s *service) GetConfig() core.AppConfig {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
}

func (s *service) SetConfig(cfg core.AppConfig) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	cfg.UpstreamProxy = strings.TrimSpace(cfg.UpstreamProxy)
	cfg.TestTarget = strings.TrimSpace(cfg.TestTarget)
	cfg.AdminToken = strings.TrimSpace(cfg.AdminToken)
	cfg.AvailableToken = strings.TrimSpace(cfg.AvailableToken)
	cfg.PoolProxyUsername = strings.TrimSpace(cfg.PoolProxyUsername)
	cfg.PoolProxyPassword = strings.TrimSpace(cfg.PoolProxyPassword)
	if (cfg.PoolProxyUsername == "") != (cfg.PoolProxyPassword == "") {
		return server.NewStatusError(http.StatusBadRequest, "pool proxy username and password must be configured together")
	}
	if cfg.TestTarget == "" {
		cfg.TestTarget = core.DefaultTestTarget
	}
	if !core.IsValidTestTarget(cfg.TestTarget) {
		cfg.TestTarget = s.config.TestTarget
		if cfg.TestTarget == "" {
			cfg.TestTarget = core.DefaultTestTarget
		}
	}
	cfg.TestTimeoutSeconds = core.NormalizeTestTimeoutSeconds(cfg.TestTimeoutSeconds, s.config.TestTimeoutSeconds)
	cfg.AvailableCacheTTLSeconds = core.NormalizeAvailableCacheTTLSeconds(cfg.AvailableCacheTTLSeconds, s.config.AvailableCacheTTLSeconds)
	cfg.TestResultTTLMinutes = core.NormalizeTestResultTTLMinutes(cfg.TestResultTTLMinutes, s.config.TestResultTTLMinutes)
	cfg.AvailableQuickProbeSeconds = core.NormalizeAvailableQuickProbeSeconds(cfg.AvailableQuickProbeSeconds, s.config.AvailableQuickProbeSeconds)
	cfg.AvailableQuickConcurrency = core.NormalizeAvailableQuickConcurrency(cfg.AvailableQuickConcurrency, s.config.AvailableQuickConcurrency)
	cfg.AvailableBackgroundConcurrency = core.NormalizeAvailableBackgroundConcurrency(cfg.AvailableBackgroundConcurrency, s.config.AvailableBackgroundConcurrency)
	cfg.AvailableMinWarmPoolSize = core.NormalizeAvailableMinWarmPoolSize(cfg.AvailableMinWarmPoolSize, s.config.AvailableMinWarmPoolSize)
	if s.store != nil {
		if err := s.store.SaveConfig(cfg); err != nil {
			return err
		}
	}
	if s.tester != nil {
		s.tester.SetTestTarget(cfg.TestTarget)
		s.tester.SetTestTimeout(core.TestTimeoutDuration(cfg.TestTimeoutSeconds))
	}
	if s.pool != nil {
		s.pool.ReloadRuntimeConfig(cfg.UpstreamProxy, cfg.PoolProxyUsername, cfg.PoolProxyPassword)
	}
	s.config = cfg
	s.invalidateAvailableCandidates()
	s.addLog(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "config",
		Stage:   "save",
		Message: "运行设置已保存",
		Detail: fmt.Sprintf(
			"target=%s timeout=%ds upstream=%q admin_token=%t available_token=%t pool_auth=%t available_cache_ttl=%ds test_result_ttl=%dm quick_probe=%ds quick_concurrency=%d background_concurrency=%d min_warm_pool=%d",
			cfg.TestTarget,
			cfg.TestTimeoutSeconds,
			cfg.UpstreamProxy,
			cfg.AdminToken != "",
			cfg.AvailableToken != "",
			cfg.PoolProxyUsername != "" && cfg.PoolProxyPassword != "",
			cfg.AvailableCacheTTLSeconds,
			cfg.TestResultTTLMinutes,
			cfg.AvailableQuickProbeSeconds,
			cfg.AvailableQuickConcurrency,
			cfg.AvailableBackgroundConcurrency,
			cfg.AvailableMinWarmPoolSize,
		),
	})
	return nil
}

func (s *service) GetTestResults() map[string]*core.TestResult {
	if s.tester == nil {
		if s.store == nil {
			return nil
		}
		results, err := s.store.LoadTestResults()
		if err != nil {
			log.Printf("[service] could not load test results: %v", err)
			return nil
		}
		return results
	}
	return s.tester.GetResults()
}

func (s *service) TestProxy(tag string) (*core.TestResult, error) {
	proxies := s.subMgr.GetAllProxies()
	if len(proxies) == 0 {
		return nil, server.NewStatusError(http.StatusServiceUnavailable, "no proxies available")
	}

	var target *core.ProxyInfo
	for _, p := range proxies {
		if p.Tag == tag {
			target = p
			break
		}
	}
	if target == nil {
		return nil, server.NewStatusError(http.StatusNotFound, "proxy not found")
	}

	if err := core.ValidateProxyForSingBox(target); err != nil {
		result := core.UnsupportedProxyTestResult(target, err)
		s.recordTestResult(result)
		s.addLog(proxyDiagnosticEvent("warn", "tester", "validate", "节点配置不受支持，已跳过测试", err.Error(), target))
		return result, nil
	}

	s.configMu.RLock()
	upstream := strings.TrimSpace(s.config.UpstreamProxy)
	targetURL := s.config.TestTarget
	timeoutSeconds := s.config.TestTimeoutSeconds
	s.configMu.RUnlock()

	s.addLog(proxyDiagnosticEvent("info", "tester", "single", "开始单节点测速", "", target))
	tempTester, err := core.NewTesterWithStore([]*core.ProxyInfo{target}, upstream, s.store)
	if err != nil {
		if upstream != "" && strings.Contains(err.Error(), "upstream") {
			s.addLog(proxyDiagnosticEvent("error", "tester", "single", "单节点测速器创建失败", err.Error(), target))
			return nil, server.NewStatusError(http.StatusServiceUnavailable, fmt.Sprintf("tester unavailable: %v", err))
		}
		result := &core.TestResult{
			Tag:       target.Tag,
			Err:       err.Error(),
			Timestamp: time.Now().UTC(),
		}
		s.recordTestResult(result)
		s.addLog(proxyDiagnosticEvent("error", "tester", "single", "节点测试失败", err.Error(), target))
		return result, nil
	}
	defer tempTester.Close()
	if targetURL != "" {
		tempTester.SetTestTarget(targetURL)
	}
	tempTester.SetTestTimeout(core.TestTimeoutDuration(timeoutSeconds))

	result := tempTester.TestOne(tag)
	s.recordTestResult(result)
	if result.Err != "" {
		s.addLog(proxyDiagnosticEvent("error", "tester", "single", "节点测试失败", result.Err, target))
	} else {
		s.addLog(proxyDiagnosticEvent("info", "tester", "single", fmt.Sprintf("节点测试成功：%dms", result.Latency), "", target))
	}
	return result, nil
}

func (s *service) TestUpstream(raw string, targetURL string) (*core.TestResult, error) {
	s.configMu.RLock()
	cfg := s.config
	s.configMu.RUnlock()

	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = strings.TrimSpace(cfg.UpstreamProxy)
	}
	if raw == "" {
		return nil, server.NewStatusError(http.StatusBadRequest, "upstream proxy is required")
	}

	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		targetURL = cfg.TestTarget
	}
	if targetURL == "" {
		targetURL = core.DefaultTestTarget
	}
	if !core.IsValidTestTarget(targetURL) {
		return nil, server.NewStatusError(http.StatusBadRequest, "invalid test target")
	}

	s.addLog(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "upstream",
		Stage:   "test",
		Message: "开始测试上游代理",
		Detail:  raw,
	})
	result, err := core.TestUpstream(raw, targetURL)
	if err != nil {
		s.addLog(core.DiagnosticEvent{
			Level:   "error",
			Scope:   "upstream",
			Stage:   "test",
			Message: "上游代理测试失败",
			Detail:  err.Error(),
		})
		return nil, server.NewStatusError(http.StatusBadRequest, err.Error())
	}
	if result.Err != "" {
		s.addLog(core.DiagnosticEvent{
			Level:   "error",
			Scope:   "upstream",
			Stage:   "test",
			Message: "上游代理测试失败",
			Detail:  result.Err,
		})
	} else {
		s.addLog(core.DiagnosticEvent{
			Level:   "info",
			Scope:   "upstream",
			Stage:   "test",
			Message: fmt.Sprintf("上游代理测试成功：%dms", result.Latency),
		})
	}
	return result, nil
}

func (s *service) GetPoolStatus() *core.PoolStatus {
	return s.pool.GetStatus()
}

func (s *service) StopPool() {
	s.pool.Stop()
	s.invalidateAvailableCandidates()
	s.addLog(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "pool",
		Stage:   "stop",
		Message: "代理池已停止",
	})
}

func (s *service) GetDiagnosticLogs(limit int) []core.DiagnosticEvent {
	return s.logs.List(limit)
}

func (s *service) ClearDiagnosticLogs() {
	s.logs.Clear()
}

func (s *service) GetRequestLogDates() ([]string, error) {
	if s.reqLog == nil {
		return []string{}, nil
	}
	return s.reqLog.ListDates()
}

func (s *service) GetRequestLogs(date string, limit int) ([]core.RequestLogEntry, error) {
	if s.reqLog == nil {
		return []core.RequestLogEntry{}, nil
	}
	return s.reqLog.List(date, limit)
}

func (s *service) ClearRequestLogs(date string) error {
	if s.reqLog == nil {
		return nil
	}
	date = strings.TrimSpace(date)
	if date == "" {
		return s.reqLog.DeleteAll()
	}
	return s.reqLog.DeleteDate(date)
}

func (s *service) syncTester() ([]*core.ProxyInfo, error) {
	proxies := s.subMgr.GetAllProxies()
	s.configMu.RLock()
	upstream := strings.TrimSpace(s.config.UpstreamProxy)
	target := s.config.TestTarget
	timeoutSeconds := s.config.TestTimeoutSeconds
	s.configMu.RUnlock()
	timeout := core.TestTimeoutDuration(timeoutSeconds)
	validProxies, invalidResults := filterSupportedProxies(proxies, s.logs)

	log.Printf("[service] syncTester: %d proxies, %d valid, upstream=%q", len(proxies), len(validProxies), upstream)
	if s.tester == nil {
		var err error
		s.tester, err = core.NewTesterWithStore(validProxies, upstream, s.store)
		if err != nil {
			log.Printf("[service] could not create tester: %v", err)
			return validProxies, err
		}
		if target != "" {
			s.tester.SetTestTarget(target)
		}
		s.tester.SetTestTimeout(timeout)
		s.tester.RecordResults(invalidResults)
		return validProxies, nil
	}
	if err := s.tester.RebuildWithUpstream(validProxies, upstream); err != nil {
		log.Printf("[service] could not sync tester: %v", err)
		return validProxies, err
	}
	if target != "" {
		s.tester.SetTestTarget(target)
	}
	s.tester.SetTestTimeout(timeout)
	s.tester.RecordResults(invalidResults)
	return validProxies, nil
}

func (s *service) recordTestResult(result *core.TestResult) {
	if result == nil || result.Tag == "" {
		return
	}
	if result.Timestamp.IsZero() {
		result.Timestamp = time.Now().UTC()
	}
	if s.tester != nil {
		s.tester.RecordResult(result)
		return
	}
	if s.store != nil {
		if err := s.store.SaveTestResult(result); err != nil {
			log.Printf("[service] could not persist test result for %s: %v", result.Tag, err)
		}
	}
}

func (s *service) addLog(event core.DiagnosticEvent) {
	if s.logs == nil {
		return
	}
	s.logs.Add(event)
}

func filterSupportedProxies(proxies []*core.ProxyInfo, diagnostics *core.DiagnosticLog) ([]*core.ProxyInfo, map[string]*core.TestResult) {
	valid := make([]*core.ProxyInfo, 0, len(proxies))
	invalidResults := make(map[string]*core.TestResult)
	for _, proxy := range proxies {
		if err := core.ValidateProxyForSingBox(proxy); err != nil {
			if proxy != nil && proxy.Tag != "" {
				invalidResults[proxy.Tag] = core.UnsupportedProxyTestResult(proxy, err)
			}
			if diagnostics != nil {
				diagnostics.Add(proxyDiagnosticEvent("warn", "tester", "validate", "跳过不支持的节点配置", err.Error(), proxy))
			}
			continue
		}
		valid = append(valid, proxy)
	}
	return valid, invalidResults
}

func proxyDiagnosticEvent(level string, scope string, stage string, message string, detail string, proxy *core.ProxyInfo) core.DiagnosticEvent {
	event := core.DiagnosticEvent{
		Level:   level,
		Scope:   scope,
		Stage:   stage,
		Message: message,
		Detail:  detail,
	}
	if proxy != nil {
		event.Tag = proxy.Tag
		event.Name = proxy.Name
		event.SourceID = proxy.SourceID
	}
	return event
}
