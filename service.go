package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"http-proxy/core"
	"http-proxy/server"
)

type availableCache struct {
	proxies    []server.AvailableProxy
	time       time.Time
	poolConfig poolRuntimeConfig
}

type poolRuntimeConfig struct {
	upstream string
	username string
	password string
}

type service struct {
	subMgr *core.SubscriptionManager
	store  *core.Store
	tester *core.Tester
	pool   *core.ProxyPool
	config core.AppConfig
	logs   *core.DiagnosticLog
	reqLog *core.RequestLogStore

	// L0: exclusive test+swap gate
	l0       atomic.Bool
	cache    *availableCache
	cacheMu  sync.RWMutex
	configMu sync.RWMutex
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
	now := time.Now()
	log.Printf("[service] GetAvailableProxies(count=%d)", count)

	poolConfig := s.currentPoolRuntimeConfig()

	// L0: fast path — serve cached result if fresh
	s.cacheMu.RLock()
	if s.cache != nil && s.cache.poolConfig == poolConfig && now.Sub(s.cache.time) < core.CacheTTL {
		cp := s.cache
		s.cacheMu.RUnlock()
		log.Printf("[service] cache hit (%d proxies, age=%v)", len(cp.proxies), now.Sub(s.cache.time))
		return cp.proxies, nil
	}
	s.cacheMu.RUnlock()

	// L0: try exclusive test+swap
	if !s.l0.CompareAndSwap(false, true) {
		log.Printf("[service] L0 busy, returning error")
		return nil, fmt.Errorf("proxy refresh in progress")
	}
	defer s.l0.Store(false)

	poolConfig = s.currentPoolRuntimeConfig()

	// Recheck cache after acquiring lock
	s.cacheMu.RLock()
	if s.cache != nil && s.cache.poolConfig == poolConfig && now.Sub(s.cache.time) < core.CacheTTL {
		cp := s.cache
		s.cacheMu.RUnlock()
		log.Printf("[service] cache hit after L0 (%d proxies)", len(cp.proxies))
		return cp.proxies, nil
	}
	s.cacheMu.RUnlock()

	log.Printf("[service] upstream=%q", poolConfig.upstream)

	proxies := s.subMgr.GetAllProxies()
	log.Printf("[service] total proxies from subs: %d", len(proxies))
	if len(proxies) == 0 {
		log.Printf("[service] no proxies, returning nil")
		return nil, nil
	}

	log.Printf("[service] testing proxies...")
	validProxies, err := s.syncTester()
	if err != nil || s.tester == nil {
		if err == nil {
			err = fmt.Errorf("tester unavailable")
		}
		s.addLog(core.DiagnosticEvent{
			Level:   "error",
			Scope:   "tester",
			Stage:   "sync",
			Message: "批量测速前同步测速器失败",
			Detail:  err.Error(),
		})
		return nil, fmt.Errorf("tester unavailable: %w", err)
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
	s.addLog(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "tester",
		Stage:   "batch",
		Message: fmt.Sprintf("开始批量测速：%d 个有效节点", len(validProxies)),
	})
	results := s.tester.GetOrRefreshResults(validProxies)
	log.Printf("[service] tested, %d results", len(results))
	if len(results) == 0 {
		log.Printf("[service] no test results")
		return nil, nil
	}

	selected, latencies := core.SelectProxies(validProxies, results, count)
	log.Printf("[service] selected %d healthy proxies", len(selected))
	s.addLog(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "pool",
		Stage:   "select",
		Message: fmt.Sprintf("代理池选择完成：%d 个健康节点", len(selected)),
	})
	if len(selected) == 0 {
		log.Printf("[service] no healthy proxies")
		return nil, nil
	}

	log.Printf("[service] hot-swapping %d instances...", len(selected))
	s.configMu.RLock()
	cfg := s.config
	poolConfig = poolRuntimeConfigFromConfig(cfg)
	s.pool.SetRuntimeConfig(poolConfig.upstream, poolConfig.username, poolConfig.password)
	s.pool.HotSwap(selected, latencies)
	tags := make([]string, len(selected))
	for i, p := range selected {
		tags[i] = p.Tag
	}
	ports := s.pool.GetPorts(tags)

	out := make([]server.AvailableProxy, 0, len(selected))
	for _, p := range selected {
		port, ok := ports[p.Tag]
		if !ok {
			continue
		}
		out = append(out, server.AvailableProxy{
			HTTP:     core.FormatHTTPProxyURL(port, cfg.PoolProxyUsername, cfg.PoolProxyPassword),
			Name:     p.Name,
			Tag:      p.Tag,
			Latency:  latencies[p.Tag],
			Protocol: p.Protocol,
		})
	}
	s.configMu.RUnlock()

	// Cache result
	s.cacheMu.Lock()
	s.cache = &availableCache{proxies: out, time: now, poolConfig: poolConfig}
	s.cacheMu.Unlock()
	log.Printf("[service] returning %d proxies (cached for %v)", len(out), core.CacheTTL)

	return out, nil
}

func (s *service) currentPoolRuntimeConfig() poolRuntimeConfig {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return poolRuntimeConfigFromConfig(s.config)
}

func poolRuntimeConfigFromConfig(cfg core.AppConfig) poolRuntimeConfig {
	return poolRuntimeConfig{
		upstream: strings.TrimSpace(cfg.UpstreamProxy),
		username: strings.TrimSpace(cfg.PoolProxyUsername),
		password: strings.TrimSpace(cfg.PoolProxyPassword),
	}
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
	// Invalidate cache on config change
	s.cacheMu.Lock()
	s.cache = nil
	s.cacheMu.Unlock()
	s.addLog(core.DiagnosticEvent{
		Level:   "info",
		Scope:   "config",
		Stage:   "save",
		Message: "运行设置已保存",
		Detail: fmt.Sprintf(
			"target=%s timeout=%ds upstream=%q admin_token=%t available_token=%t pool_auth=%t",
			cfg.TestTarget,
			cfg.TestTimeoutSeconds,
			cfg.UpstreamProxy,
			cfg.AdminToken != "",
			cfg.AvailableToken != "",
			cfg.PoolProxyUsername != "" && cfg.PoolProxyPassword != "",
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
