package core

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
)

var defaultTestTimeout = TestTimeoutDuration(DefaultTestTimeoutSeconds)

const outboundProbeInterval = 100 * time.Millisecond

type Tester struct {
	mu          sync.RWMutex
	ctx         context.Context
	box         *box.Box
	cache       map[string]*TestResult
	resultStore *Store
	testTarget  httpTestTarget
	testTimeout time.Duration
}

type httpTestTarget struct {
	addr       M.Socksaddr
	scheme     string
	serverName string
	host       string
	path       string
}

type outboundDialer interface {
	DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error)
}

func NewTester(proxies []*ProxyInfo) (*Tester, error) {
	return NewTesterWithUpstream(proxies, "")
}

func NewTesterWithUpstream(proxies []*ProxyInfo, upstream string) (*Tester, error) {
	return NewTesterWithStore(proxies, upstream, nil)
}

func NewTesterWithStore(proxies []*ProxyInfo, upstream string, store *Store) (*Tester, error) {
	log.Printf("[tester] creating tester with %d initial proxies", len(proxies))
	cache := make(map[string]*TestResult)
	if store != nil {
		results, err := store.LoadTestResults()
		if err != nil {
			log.Printf("[tester] could not load persisted test results: %v", err)
		} else {
			cache = results
			log.Printf("[tester] loaded %d persisted test results", len(cache))
		}
	}
	t := &Tester{
		cache:       cache,
		resultStore: store,
		testTarget:  defaultTestTarget(),
		testTimeout: defaultTestTimeout,
	}
	t.ctx = include.Context(context.Background())
	if err := t.RebuildWithUpstream(proxies, upstream); err != nil {
		log.Printf("[tester] create failed: %v", err)
		return nil, err
	}
	log.Printf("[tester] created successfully")
	return t, nil
}

func (t *Tester) SetTestTarget(rawURL string) bool {
	target, ok := parseHTTPTestTarget(rawURL)
	if !ok {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.testTarget = target
	return true
}

func (t *Tester) SetTestTimeout(timeout time.Duration) bool {
	if timeout < time.Duration(MinTestTimeoutSeconds)*time.Second || timeout > time.Duration(MaxTestTimeoutSeconds)*time.Second {
		return false
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.testTimeout = timeout
	return true
}

func (t *Tester) testTimeoutLocked() time.Duration {
	if t.testTimeout <= 0 {
		return defaultTestTimeout
	}
	return t.testTimeout
}

func IsValidTestTarget(rawURL string) bool {
	_, ok := parseHTTPTestTarget(rawURL)
	return ok
}

func defaultTestTarget() httpTestTarget {
	target, ok := parseHTTPTestTarget(DefaultTestTarget)
	if !ok {
		panic("invalid default test target")
	}
	return target
}

func parseHTTPTestTarget(rawURL string) (httpTestTarget, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return httpTestTarget{}, false
	}

	host := u.Hostname()
	if host == "" {
		return httpTestTarget{}, false
	}

	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if portStr := u.Port(); portStr != "" {
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return httpTestTarget{}, false
		}
	}
	if err != nil || port <= 0 || port > 65535 {
		return httpTestTarget{}, false
	}

	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}

	return httpTestTarget{
		addr:       M.ParseSocksaddrHostPort(host, uint16(port)),
		scheme:     u.Scheme,
		serverName: host,
		host:       formatHostHeader(host, port, u.Scheme),
		path:       path,
	}, true
}

func formatHostHeader(host string, port int, scheme string) string {
	if (scheme == "http" && port != 80) || (scheme == "https" && port != 443) {
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}

func (target httpTestTarget) request() string {
	return fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: http-proxy-healthcheck/1.0\r\n\r\n", target.path, target.host)
}

func (target httpTestTarget) requestTarget() string {
	return target.host + target.path
}

func (target httpTestTarget) address() string {
	return net.JoinHostPort(target.addr.AddrString(), strconv.Itoa(int(target.addr.Port)))
}

func (t *Tester) GetResults() map[string]*TestResult {
	t.mu.RLock()
	defer t.mu.RUnlock()

	results := make(map[string]*TestResult, len(t.cache))
	for k, v := range t.cache {
		results[k] = v
	}
	return results
}

func (t *Tester) TestOne(tag string) *TestResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.box == nil {
		result := &TestResult{
			Tag:       tag,
			Err:       "tester unavailable",
			Timestamp: time.Now(),
		}
		t.cache[tag] = result
		t.persistResult(result)
		return result
	}

	target := t.testTarget
	timeout := t.testTimeoutLocked()
	latency, err := t.testTag(tag, target, timeout)
	result := &TestResult{
		Tag:       tag,
		Timestamp: time.Now(),
	}
	if err != nil {
		result.Err = err.Error()
	} else {
		result.Latency = latency
	}
	t.cache[tag] = result
	t.persistResult(result)
	return result
}

func (t *Tester) Rebuild(proxies []*ProxyInfo) error {
	return t.RebuildWithUpstream(proxies, "")
}

func (t *Tester) RebuildWithUpstream(proxies []*ProxyInfo, upstream string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	log.Printf("[tester] rebuilding box with %d outbounds, upstream=%q", len(proxies), upstream)

	outbounds, err := buildTesterOutbounds(proxies, upstream)
	if err != nil {
		return err
	}

	if t.box != nil {
		log.Printf("[tester] closing old box")
		t.box.Close()
		t.box = nil
	}

	opts := box.Options{
		Options: option.Options{
			Log: &option.LogOptions{
				Level: "warn",
			},
			Outbounds: outbounds,
			Route: &option.RouteOptions{
				Final: "direct",
			},
		},
		Context: t.ctx,
	}

	log.Printf("[tester] creating box...")
	b, err := box.New(opts)
	if err != nil {
		return fmt.Errorf("box.New: %w", err)
	}

	log.Printf("[tester] starting box...")
	if err := b.Start(); err != nil {
		b.Close()
		return fmt.Errorf("box.Start: %w", err)
	}

	t.box = b
	log.Printf("[tester] box ready")
	return nil
}

func buildTesterOutbounds(proxies []*ProxyInfo, upstream string) ([]option.Outbound, error) {
	upstream = strings.TrimSpace(upstream)
	outbounds := make([]option.Outbound, 0, len(proxies)+2)

	if upstream != "" {
		upstreamOutbound, err := buildUpstreamOutbound(upstream)
		if err != nil {
			return nil, err
		}
		for _, p := range proxies {
			if err := ValidateProxyForSingBox(p); err != nil {
				return nil, err
			}
			proxyOut := proxyToOutbound(p)
			applyDetour(&proxyOut, "upstream")
			outbounds = append(outbounds, proxyOut)
		}
		outbounds = append(outbounds, upstreamOutbound)
	} else {
		for _, p := range proxies {
			if err := ValidateProxyForSingBox(p); err != nil {
				return nil, err
			}
			outbounds = append(outbounds, proxyToOutbound(p))
		}
	}

	outbounds = append(outbounds, option.Outbound{
		Type: C.TypeDirect,
		Tag:  "direct",
	})
	return outbounds, nil
}

func (t *Tester) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.box != nil {
		return t.box.Close()
	}
	return nil
}

func (t *Tester) RecordResult(result *TestResult) {
	if result == nil || result.Tag == "" {
		return
	}
	t.RecordResults(map[string]*TestResult{result.Tag: result})
}

func (t *Tester) RecordResults(results map[string]*TestResult) {
	if len(results) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for tag, result := range results {
		if result == nil || result.Tag == "" {
			continue
		}
		t.cache[tag] = result
	}
	t.persistResults(results)
}

func (t *Tester) testTag(tag string, target httpTestTarget, timeout time.Duration) (int, error) {
	outboundMgr := t.box.Outbound()
	out, found := outboundMgr.Outbound(tag)
	if !found {
		return 0, fmt.Errorf("outbound %s not found", tag)
	}
	return testOutbound(tag, out, target, timeout)
}

func testOutbound(tag string, out outboundDialer, target httpTestTarget, timeout time.Duration) (int, error) {
	if timeout <= 0 {
		timeout = defaultTestTimeout
	}

	labels := []string{"cold", "warm"}
	results := testOutboundProbes(tag, out, target, timeout)
	for i, label := range labels {
		if results[i].err != nil {
			log.Printf("[tester] %s %s failed: %s target=%s timeout=%v", tag, label, results[i].String(), target.requestTarget(), timeout)
			continue
		}
		log.Printf("[tester] %s %s success: %s target=%s timeout=%v", tag, label, results[i].String(), target.requestTarget(), timeout)
	}

	best := 0
	found := false
	for _, result := range results {
		if result.err != nil {
			continue
		}
		if !found || result.latency < best {
			best = result.latency
			found = true
		}
	}
	if found {
		log.Printf("[tester] %s: selected %dms (cold=%s, warm=%s)", tag, best, results[0].String(), results[1].String())
		return best, nil
	}

	return 0, fmt.Errorf("all probe attempts failed: cold=%v; warm=%v", results[0].err, results[1].err)
}

func testOutboundProbes(tag string, out outboundDialer, target httpTestTarget, timeout time.Duration) []outboundProbeResult {
	labels := []string{"cold", "warm"}
	results := make([]outboundProbeResult, 2)
	var conn net.Conn
	var reader *bufio.Reader
	defer closeProbeConn(&conn, &reader)

	for i := range results {
		if i > 0 {
			time.Sleep(outboundProbeInterval)
		}

		results[i] = testOutboundProbe(tag, labels[i], out, target, timeout, &conn, &reader)
		if results[i].err != nil {
			results[i].closedAfter = true
			closeProbeConn(&conn, &reader)
		}
	}

	return results
}

type outboundProbeResult struct {
	latency       int
	err           error
	stage         string
	reused        bool
	status        string
	statusCode    int
	contentLength int64
	close         bool
	bodyBytes     int64
	closedAfter   bool
}

func (r outboundProbeResult) String() string {
	detail := fmt.Sprintf("stage=%s reused=%t elapsed=%dms", r.stage, r.reused, r.latency)
	if r.status != "" {
		detail += fmt.Sprintf(" status=%q status_code=%d content_length=%d close=%t body_bytes=%d", r.status, r.statusCode, r.contentLength, r.close, r.bodyBytes)
	}
	if r.err != nil {
		detail += fmt.Sprintf(" closed_after=%t err=%v", r.closedAfter, r.err)
	}
	return detail
}

func testOutboundProbe(tag string, label string, out outboundDialer, target httpTestTarget, timeout time.Duration, conn *net.Conn, reader **bufio.Reader) outboundProbeResult {
	start := time.Now()
	deadline := start.Add(timeout)
	result := outboundProbeResult{
		reused: *conn != nil,
		stage:  "dial",
	}
	log.Printf("[tester] %s %s probe start: addr=%s target=%s timeout=%v reused=%t", tag, label, target.address(), target.requestTarget(), timeout, result.reused)

	if *conn == nil {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()

		probeConn, errDial := out.DialContext(ctx, "tcp", target.addr)
		if errDial != nil {
			log.Printf("[tester] %s %s probe failed: stage=dial err=%v", tag, label, errDial)
			return result.withError(start, "dial", errDial)
		}
		if target.scheme == "https" {
			result.stage = "tls_handshake"
			tlsConn := tls.Client(probeConn, &tls.Config{ServerName: target.serverName})
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				_ = probeConn.Close()
				return result.withError(start, "tls_handshake", err)
			}
			probeConn = tlsConn
		}
		*conn = probeConn
		*reader = bufio.NewReader(probeConn)
	}

	result.stage = "deadline"
	if err := (*conn).SetDeadline(deadline); err != nil {
		if isUnsupportedDeadlineError(err) {
			log.Printf("[tester] %s %s probe: stage=deadline err=%v; continuing without conn deadline", tag, label, err)
		} else {
			log.Printf("[tester] %s %s probe failed: stage=deadline err=%v", tag, label, err)
			return result.withError(start, "deadline", err)
		}
	}
	result.stage = "write"
	if _, err := (*conn).Write([]byte(target.request())); err != nil {
		log.Printf("[tester] %s %s probe failed: stage=write err=%v", tag, label, err)
		return result.withError(start, "write", err)
	}
	response, stage, err := readHTTPProbeResponse(*reader)
	result.stage = stage
	result.status = response.status
	result.statusCode = response.statusCode
	result.contentLength = response.contentLength
	result.close = response.close
	result.bodyBytes = response.bodyBytes
	if err != nil {
		log.Printf("[tester] %s %s probe failed: stage=read detail=%s err=%v", tag, label, stage, err)
		return result.withError(start, stage, err)
	}

	result.latency = elapsedMilliseconds(start)
	return result
}

func isUnsupportedDeadlineError(err error) bool {
	return errors.Is(err, os.ErrInvalid)
}

func (r outboundProbeResult) withError(start time.Time, stage string, err error) outboundProbeResult {
	r.stage = stage
	r.latency = elapsedMilliseconds(start)
	r.err = fmt.Errorf("%s: %w", stage, err)
	return r
}

type probeHTTPResponse struct {
	status        string
	statusCode    int
	contentLength int64
	close         bool
	bodyBytes     int64
}

func readHTTPProbeResponse(reader *bufio.Reader) (probeHTTPResponse, string, error) {
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		return probeHTTPResponse{}, "read_response", err
	}
	defer resp.Body.Close()

	response := probeHTTPResponse{
		status:        resp.Status,
		statusCode:    resp.StatusCode,
		contentLength: resp.ContentLength,
		close:         resp.Close,
	}
	bodyBytes, err := io.Copy(io.Discard, resp.Body)
	response.bodyBytes = bodyBytes
	if err != nil {
		return response, "read_body", err
	}
	return response, "read_body", nil
}

func closeProbeConn(conn *net.Conn, reader **bufio.Reader) {
	if *conn != nil {
		_ = (*conn).Close()
	}
	*conn = nil
	*reader = nil
}

func elapsedMilliseconds(start time.Time) int {
	elapsed := int(time.Since(start).Milliseconds())
	if elapsed <= 0 {
		return 1
	}
	return elapsed
}

func TestUpstream(raw string, targetURL string) (*TestResult, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("upstream proxy is empty")
	}
	if strings.TrimSpace(targetURL) == "" {
		targetURL = DefaultTestTarget
	}
	target, ok := parseHTTPTestTarget(targetURL)
	if !ok {
		return nil, fmt.Errorf("invalid test target")
	}
	upstreamOutbound, err := buildUpstreamOutbound(raw)
	if err != nil {
		return nil, err
	}

	ctx := include.Context(context.Background())
	b, err := box.New(box.Options{
		Options: option.Options{
			Log: &option.LogOptions{
				Level: "warn",
			},
			Outbounds: []option.Outbound{
				upstreamOutbound,
				{
					Type: C.TypeDirect,
					Tag:  "direct",
				},
			},
			Route: &option.RouteOptions{
				Final: "direct",
			},
		},
		Context: ctx,
	})
	if err != nil {
		return nil, fmt.Errorf("box.New: %w", err)
	}
	defer b.Close()

	if err := b.Start(); err != nil {
		return nil, fmt.Errorf("box.Start: %w", err)
	}
	out, found := b.Outbound().Outbound("upstream")
	if !found {
		return nil, fmt.Errorf("upstream outbound not found")
	}

	result := &TestResult{
		Tag:       "upstream",
		Timestamp: time.Now(),
	}
	latency, err := testOutbound("upstream", out, target, defaultTestTimeout)
	if err != nil {
		result.Err = err.Error()
	} else {
		result.Latency = latency
	}
	return result, nil
}

func (t *Tester) refreshCache(proxies []*ProxyInfo) {
	target := t.testTarget
	timeout := t.testTimeoutLocked()
	now := time.Now()
	var toTest []string

	for _, p := range proxies {
		r, exists := t.cache[p.Tag]
		if !exists || now.Sub(r.Timestamp) > TestResultTTL {
			toTest = append(toTest, p.Tag)
		}
	}

	if len(toTest) == 0 {
		log.Printf("[tester] all %d proxies cache valid, skip test", len(proxies))
		return
	}

	log.Printf("[tester] testing %d proxies (concurrency=%d)...", len(toTest), TestConcurrency)

	sem := make(chan struct{}, TestConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	results := make(map[string]*TestResult, len(toTest))
	done := 0

	for _, tag := range toTest {
		sem <- struct{}{}
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			defer func() { <-sem }()

			latency, err := t.testTag(tag, target, timeout)
			r := &TestResult{
				Tag:       tag,
				Timestamp: now,
			}
			if err != nil {
				r.Err = err.Error()
			} else {
				r.Latency = latency
			}

			mu.Lock()
			results[tag] = r
			done++
			mu.Unlock()
		}(tag)
	}
	wg.Wait()

	for tag, r := range results {
		t.cache[tag] = r
	}
	t.persistResults(results)
	log.Printf("[tester] complete: %d results cached", len(results))
}

func (t *Tester) persistResult(result *TestResult) {
	if t.resultStore == nil {
		return
	}
	if err := t.resultStore.SaveTestResult(result); err != nil {
		log.Printf("[tester] could not persist test result for %s: %v", result.Tag, err)
	}
}

func (t *Tester) persistResults(results map[string]*TestResult) {
	if t.resultStore == nil || len(results) == 0 {
		return
	}
	if err := t.resultStore.SaveTestResults(results); err != nil {
		log.Printf("[tester] could not persist test results: %v", err)
	}
}

func (t *Tester) GetOrRefreshResults(proxies []*ProxyInfo) map[string]*TestResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.refreshCache(proxies)

	results := make(map[string]*TestResult, len(proxies))
	for _, p := range proxies {
		if r, ok := t.cache[p.Tag]; ok {
			results[p.Tag] = r
		}
	}
	return results
}

func SelectProxies(proxies []*ProxyInfo, results map[string]*TestResult, count int) ([]*ProxyInfo, map[string]int) {
	if count <= 0 {
		count = DefaultProxyCount
	}

	type scored struct {
		proxy   *ProxyInfo
		latency int
	}
	var healthy []scored

	for _, p := range proxies {
		r, ok := results[p.Tag]
		if !ok {
			continue
		}
		if r.Err != "" || r.Latency <= 0 || r.Latency >= MaxLatencyMs {
			continue
		}
		healthy = append(healthy, scored{proxy: p, latency: r.Latency})
	}

	if len(healthy) == 0 {
		return nil, nil
	}

	sort.Slice(healthy, func(i, j int) bool {
		return healthy[i].latency < healthy[j].latency
	})

	window := len(healthy)
	if window > count*3 {
		window = count * 3
	}
	candidates := healthy[:window]

	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})

	if len(candidates) > count {
		candidates = candidates[:count]
	}

	selected := make([]*ProxyInfo, len(candidates))
	latencyMap := make(map[string]int, len(candidates))
	for i, s := range candidates {
		selected[i] = s.proxy
		latencyMap[s.proxy.Tag] = s.latency
	}

	return selected, latencyMap
}

func proxyToOutbound(p *ProxyInfo) option.Outbound {
	switch p.Protocol {
	case "vless":
		return buildVLESSOutbound(p)
	case "trojan":
		return buildTrojanOutbound(p)
	case "ss", "shadowsocks":
		return buildSSOutbound(p)
	default:
		return option.Outbound{
			Tag:  p.Tag,
			Type: C.TypeDirect,
		}
	}
}

func buildVLESSOutbound(p *ProxyInfo) option.Outbound {
	opts := &option.VLESSOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     p.Server,
			ServerPort: uint16(p.ServerPort),
		},
		UUID: p.UUID,
		Flow: p.Flow,
	}
	if p.TLS != nil && p.TLS.Enabled {
		opts.OutboundTLSOptionsContainer.TLS = buildTLSOptions(p.TLS)
	}
	if p.Transport != nil {
		opts.Transport = buildTransportOptions(p.Transport)
	}
	if p.PacketEncoding != "" {
		opts.PacketEncoding = &p.PacketEncoding
	}

	return option.Outbound{
		Type:    C.TypeVLESS,
		Tag:     p.Tag,
		Options: opts,
	}
}

func buildTrojanOutbound(p *ProxyInfo) option.Outbound {
	opts := &option.TrojanOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     p.Server,
			ServerPort: uint16(p.ServerPort),
		},
		Password: p.Password,
	}
	if p.TLS != nil && p.TLS.Enabled {
		opts.OutboundTLSOptionsContainer.TLS = buildTLSOptions(p.TLS)
	}
	if p.Transport != nil {
		opts.Transport = buildTransportOptions(p.Transport)
	}

	return option.Outbound{
		Type:    C.TypeTrojan,
		Tag:     p.Tag,
		Options: opts,
	}
}

func buildSSOutbound(p *ProxyInfo) option.Outbound {
	opts := &option.ShadowsocksOutboundOptions{
		ServerOptions: option.ServerOptions{
			Server:     p.Server,
			ServerPort: uint16(p.ServerPort),
		},
		Method:   p.Method,
		Password: p.Password,
	}
	return option.Outbound{
		Type:    C.TypeShadowsocks,
		Tag:     p.Tag,
		Options: opts,
	}
}

func buildTLSOptions(tls *TLSConfig) *option.OutboundTLSOptions {
	opts := &option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: tls.ServerName,
		Insecure:   tls.Insecure,
	}
	if len(tls.ALPN) > 0 {
		opts.ALPN = badoption.Listable[string](tls.ALPN)
	}
	if tls.UTLS != nil && tls.UTLS.Enabled {
		opts.UTLS = &option.OutboundUTLSOptions{
			Enabled:     true,
			Fingerprint: tls.UTLS.Fingerprint,
		}
	}
	if tls.Reality != nil && tls.Reality.Enabled {
		opts.Reality = &option.OutboundRealityOptions{
			Enabled:   true,
			PublicKey: tls.Reality.PublicKey,
			ShortID:   tls.Reality.ShortID,
		}
	}
	return opts
}

func buildTransportOptions(t *TransportConfig) *option.V2RayTransportOptions {
	transportType := t.Type
	if transportType == "websocket" {
		transportType = "ws"
	}
	opts := &option.V2RayTransportOptions{
		Type: transportType,
	}
	switch transportType {
	case "ws", "websocket":
		opts.WebsocketOptions = option.V2RayWebsocketOptions{
			Path:    t.Path,
			Headers: buildHTTPHeaders(t.Headers),
		}
	case "grpc":
		opts.GRPCOptions = option.V2RayGRPCOptions{
			ServiceName: t.Path,
		}
	}
	return opts
}

func buildHTTPHeaders(headers map[string]string) badoption.HTTPHeader {
	if len(headers) == 0 {
		return nil
	}
	out := make(badoption.HTTPHeader, len(headers))
	for name, value := range headers {
		if name == "" {
			continue
		}
		out[name] = badoption.Listable[string]{value}
	}
	return out
}
