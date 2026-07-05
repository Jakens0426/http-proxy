package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func TestParseHTTPTestTargetDefault(t *testing.T) {
	target, ok := parseHTTPTestTarget("https://www.gstatic.com/generate_204")
	if !ok {
		t.Fatal("expected target to parse")
	}
	if got := target.addr.AddrString(); got != "www.gstatic.com" {
		t.Fatalf("addr host = %q, want %q", got, "www.gstatic.com")
	}
	if got := target.addr.Port; got != 443 {
		t.Fatalf("addr port = %d, want 443", got)
	}
	if target.scheme != "https" {
		t.Fatalf("scheme = %q, want https", target.scheme)
	}
	if target.path != "/generate_204" {
		t.Fatalf("path = %q, want /generate_204", target.path)
	}
	if target.host != "www.gstatic.com" {
		t.Fatalf("host header = %q, want www.gstatic.com", target.host)
	}
}

func TestParseHTTPTestTargetPortAndQuery(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com:8080/health?x=1")
	if !ok {
		t.Fatal("expected target to parse")
	}
	if got := target.addr.AddrString(); got != "example.com" {
		t.Fatalf("addr host = %q, want %q", got, "example.com")
	}
	if got := target.addr.Port; got != 8080 {
		t.Fatalf("addr port = %d, want 8080", got)
	}
	if target.path != "/health?x=1" {
		t.Fatalf("path = %q, want /health?x=1", target.path)
	}
	if target.host != "example.com:8080" {
		t.Fatalf("host header = %q, want example.com:8080", target.host)
	}
	req := target.request()
	if !strings.HasPrefix(req, "GET /health?x=1 HTTP/1.1\r\n") {
		t.Fatalf("request starts with %q, want GET /health?x=1", req)
	}
	if !strings.Contains(req, "\r\nHost: example.com:8080\r\n") {
		t.Fatalf("request missing expected host header: %q", req)
	}
	if strings.Contains(req, "\r\nConnection: close\r\n") {
		t.Fatalf("request should not force Connection: close: %q", req)
	}
}

func TestParseHTTPTestTargetHTTPSPortAndHostHeader(t *testing.T) {
	target, ok := parseHTTPTestTarget("https://example.com:8443/health")
	if !ok {
		t.Fatal("expected target to parse")
	}
	if got := target.addr.Port; got != 8443 {
		t.Fatalf("addr port = %d, want 8443", got)
	}
	if target.host != "example.com:8443" {
		t.Fatalf("host header = %q, want example.com:8443", target.host)
	}
	if target.serverName != "example.com" {
		t.Fatalf("server name = %q, want example.com", target.serverName)
	}
}

func TestParseHTTPTestTargetEmptyPath(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com")
	if !ok {
		t.Fatal("expected target to parse")
	}
	if target.path != "/" {
		t.Fatalf("path = %q, want /", target.path)
	}
}

func TestSetTestTargetRejectsUnsupportedOrLegacyTargets(t *testing.T) {
	previous, ok := parseHTTPTestTarget("http://example.com:8080/health?x=1")
	if !ok {
		t.Fatal("failed to parse initial target")
	}
	tester := &Tester{testTarget: previous}

	for _, target := range []string{
		"www.gstatic.com:80",
		"ftp://www.gstatic.com/generate_204",
		"http://example.com:bad/health",
		"http:///generate_204",
	} {
		if tester.SetTestTarget(target) {
			t.Fatalf("SetTestTarget(%q) unexpectedly succeeded", target)
		}
		if tester.testTarget != previous {
			t.Fatalf("SetTestTarget(%q) changed target to %+v", target, tester.testTarget)
		}
	}
}

func TestSetTestTimeoutRejectsInvalidValues(t *testing.T) {
	tester := &Tester{testTimeout: 2 * time.Second}

	for _, timeout := range []time.Duration{
		0,
		time.Duration(MinTestTimeoutSeconds)*time.Second - time.Nanosecond,
		time.Duration(MaxTestTimeoutSeconds)*time.Second + time.Nanosecond,
	} {
		if tester.SetTestTimeout(timeout) {
			t.Fatalf("SetTestTimeout(%v) unexpectedly succeeded", timeout)
		}
		if tester.testTimeout != 2*time.Second {
			t.Fatalf("SetTestTimeout(%v) changed timeout to %v", timeout, tester.testTimeout)
		}
	}

	if !tester.SetTestTimeout(5 * time.Second) {
		t.Fatal("SetTestTimeout(5s) failed")
	}
	if tester.testTimeout != 5*time.Second {
		t.Fatalf("test timeout = %v, want 5s", tester.testTimeout)
	}
}

func TestTestOutboundUsesProvidedTimeout(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com/probe")
	if !ok {
		t.Fatal("failed to parse target")
	}
	conn := &recordingConn{}
	dialer := &recordingDialer{results: []dialResult{
		{conn: conn},
	}}
	timeout := 150 * time.Millisecond

	if _, err := testOutbound("recording", dialer, target, timeout); err != nil {
		t.Fatalf("testOutbound() error = %v", err)
	}
	if len(dialer.deadlines) != 1 {
		t.Fatalf("dial attempts = %d, want 1", len(dialer.deadlines))
	}
	assertDeadlineNear(t, "cold dial context", dialer.deadlines[0], dialer.dialTimes[0], timeout)
	if len(conn.deadlines) != 2 {
		t.Fatalf("conn deadlines = %d, want 2", len(conn.deadlines))
	}
	assertDeadlineNear(t, "cold conn deadline", conn.deadlines[0], conn.deadlineTimes[0], timeout)
	assertDeadlineNear(t, "warm conn deadline", conn.deadlines[1], conn.deadlineTimes[1], timeout)
}

func TestTestOutboundIgnoresUnsupportedDeadlineError(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com/probe")
	if !ok {
		t.Fatal("failed to parse target")
	}
	conn := &recordingConn{deadlineErr: os.ErrInvalid}
	dialer := &recordingDialer{results: []dialResult{
		{conn: conn},
	}}

	if _, err := testOutbound("recording", dialer, target, time.Second); err != nil {
		t.Fatalf("testOutbound() error = %v", err)
	}
	if len(conn.deadlines) != 2 {
		t.Fatalf("conn deadlines = %d, want 2", len(conn.deadlines))
	}
	if len(conn.writes) != 2 {
		t.Fatalf("writes = %d, want 2", len(conn.writes))
	}
}

func TestTestOutboundFailsOnDeadlineError(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com/probe")
	if !ok {
		t.Fatal("failed to parse target")
	}
	deadlineErr := fmt.Errorf("deadline failed")
	dialer := &recordingDialer{results: []dialResult{
		{conn: &recordingConn{deadlineErr: deadlineErr}},
		{conn: &recordingConn{deadlineErr: deadlineErr}},
	}}

	_, err := testOutbound("recording", dialer, target, time.Second)
	if err == nil {
		t.Fatal("testOutbound() error nil, want failure")
	}
	if !strings.Contains(err.Error(), "deadline: deadline failed") {
		t.Fatalf("error = %q, want deadline failure", err.Error())
	}
}

func TestTestOutboundReturnsBestSuccessfulLatency(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com/probe")
	if !ok {
		t.Fatal("failed to parse target")
	}
	conn := &recordingConn{readDelays: []time.Duration{150 * time.Millisecond, 10 * time.Millisecond}}
	dialer := &recordingDialer{results: []dialResult{
		{conn: conn},
	}}

	latency, err := testOutbound("recording", dialer, target, time.Second)
	if err != nil {
		t.Fatalf("testOutbound() error = %v", err)
	}
	if latency >= 100 {
		t.Fatalf("latency = %dms, want warm result under 100ms", latency)
	}
	if len(dialer.dialTimes) != 1 {
		t.Fatalf("dial attempts = %d, want 1", len(dialer.dialTimes))
	}
	if len(conn.writes) != 2 {
		t.Fatalf("writes = %d, want 2: %q", len(conn.writes), conn.writes)
	}
	if !strings.HasPrefix(conn.writes[0], "GET /probe HTTP/1.1\r\n") {
		t.Fatalf("cold request = %q, want GET /probe", conn.writes[0])
	}
	if !strings.HasPrefix(conn.writes[1], "GET /probe HTTP/1.1\r\n") {
		t.Fatalf("warm request = %q, want GET /probe", conn.writes[1])
	}
}

func TestTestOutboundReturnsWarmWhenColdFails(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com/probe")
	if !ok {
		t.Fatal("failed to parse target")
	}
	dialer := &recordingDialer{results: []dialResult{
		{err: fmt.Errorf("cold failed")},
		{conn: &recordingConn{}},
	}}

	if _, err := testOutbound("recording", dialer, target, time.Second); err != nil {
		t.Fatalf("testOutbound() error = %v", err)
	}
	if len(dialer.deadlines) != 2 {
		t.Fatalf("dial attempts = %d, want 2", len(dialer.deadlines))
	}
}

func TestTestOutboundReturnsErrorWhenBothAttemptsFail(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com/probe")
	if !ok {
		t.Fatal("failed to parse target")
	}
	dialer := &recordingDialer{results: []dialResult{
		{err: fmt.Errorf("cold failed")},
		{err: fmt.Errorf("warm failed")},
	}}

	if _, err := testOutbound("recording", dialer, target, time.Second); err == nil {
		t.Fatal("testOutbound() error nil, want failure")
	}
	if len(dialer.deadlines) != 2 {
		t.Fatalf("dial attempts = %d, want 2", len(dialer.deadlines))
	}
}

func TestTestOutboundErrorIncludesReadResponseStage(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com/probe")
	if !ok {
		t.Fatal("failed to parse target")
	}
	dialer := &recordingDialer{results: []dialResult{
		{conn: &recordingConn{responseChunks: []string{}}},
		{conn: &recordingConn{responseChunks: []string{}}},
	}}

	_, err := testOutbound("recording", dialer, target, time.Second)
	if err == nil {
		t.Fatal("testOutbound() error nil, want failure")
	}
	errText := err.Error()
	if !strings.Contains(errText, "cold=read_response:") {
		t.Fatalf("error = %q, want cold read_response stage", errText)
	}
	if !strings.Contains(errText, "warm=read_response:") {
		t.Fatalf("error = %q, want warm read_response stage", errText)
	}
}

func TestTestOutboundErrorIncludesReadBodyStage(t *testing.T) {
	target, ok := parseHTTPTestTarget("http://example.com/probe")
	if !ok {
		t.Fatal("failed to parse target")
	}
	incompleteBody := "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nab"
	dialer := &recordingDialer{results: []dialResult{
		{conn: &recordingConn{responseChunks: []string{incompleteBody}}},
		{conn: &recordingConn{responseChunks: []string{incompleteBody}}},
	}}

	_, err := testOutbound("recording", dialer, target, time.Second)
	if err == nil {
		t.Fatal("testOutbound() error nil, want failure")
	}
	errText := err.Error()
	if !strings.Contains(errText, "cold=read_body:") {
		t.Fatalf("error = %q, want cold read_body stage", errText)
	}
	if !strings.Contains(errText, "warm=read_body:") {
		t.Fatalf("error = %q, want warm read_body stage", errText)
	}
}

func TestBuildTesterOutboundsWithoutUpstreamDoesNotSetDetour(t *testing.T) {
	proxy := testTrojanProxy()

	outbounds, err := buildTesterOutbounds([]*ProxyInfo{proxy}, "")
	if err != nil {
		t.Fatalf("buildTesterOutbounds() error = %v", err)
	}
	if len(outbounds) != 2 {
		t.Fatalf("outbound count = %d, want 2", len(outbounds))
	}
	if outbounds[0].Tag != proxy.Tag {
		t.Fatalf("proxy outbound tag = %q, want %q", outbounds[0].Tag, proxy.Tag)
	}
	opts, ok := outbounds[0].Options.(*option.TrojanOutboundOptions)
	if !ok {
		t.Fatalf("proxy outbound options = %T, want *option.TrojanOutboundOptions", outbounds[0].Options)
	}
	if opts.Detour != "" {
		t.Fatalf("detour = %q, want empty", opts.Detour)
	}
	if outbounds[1].Tag != "direct" || outbounds[1].Type != C.TypeDirect {
		t.Fatalf("last outbound = (%q, %q), want direct", outbounds[1].Tag, outbounds[1].Type)
	}
}

func TestBuildTesterOutboundsWithUpstreamSetsDetourAndAddsUpstream(t *testing.T) {
	proxy := testTrojanProxy()

	outbounds, err := buildTesterOutbounds([]*ProxyInfo{proxy}, "http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("buildTesterOutbounds() error = %v", err)
	}
	if len(outbounds) != 3 {
		t.Fatalf("outbound count = %d, want 3", len(outbounds))
	}
	opts, ok := outbounds[0].Options.(*option.TrojanOutboundOptions)
	if !ok {
		t.Fatalf("proxy outbound options = %T, want *option.TrojanOutboundOptions", outbounds[0].Options)
	}
	if opts.Detour != "upstream" {
		t.Fatalf("detour = %q, want upstream", opts.Detour)
	}
	if outbounds[1].Tag != "upstream" || outbounds[1].Type != C.TypeHTTP {
		t.Fatalf("upstream outbound = (%q, %q), want upstream HTTP", outbounds[1].Tag, outbounds[1].Type)
	}
	if outbounds[2].Tag != "direct" || outbounds[2].Type != C.TypeDirect {
		t.Fatalf("last outbound = (%q, %q), want direct", outbounds[2].Tag, outbounds[2].Type)
	}
}

func TestBuildTesterOutboundsRejectsInvalidUpstream(t *testing.T) {
	if outbounds, err := buildTesterOutbounds([]*ProxyInfo{testTrojanProxy()}, "ftp://example.com:21"); err == nil {
		t.Fatalf("buildTesterOutbounds() = %+v, want error", outbounds)
	}
}

func assertDeadlineNear(t *testing.T, name string, deadline time.Time, start time.Time, timeout time.Duration) {
	t.Helper()
	if deadline.IsZero() {
		t.Fatalf("%s deadline was not set", name)
	}
	elapsed := deadline.Sub(start)
	if elapsed < timeout-50*time.Millisecond || elapsed > timeout+500*time.Millisecond {
		t.Fatalf("%s deadline offset = %v, want near %v", name, elapsed, timeout)
	}
}

type dialResult struct {
	conn *recordingConn
	err  error
}

type recordingDialer struct {
	results   []dialResult
	deadlines []time.Time
	dialTimes []time.Time
}

func (d *recordingDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	d.dialTimes = append(d.dialTimes, time.Now())
	if deadline, ok := ctx.Deadline(); ok {
		d.deadlines = append(d.deadlines, deadline)
	}
	attempt := len(d.dialTimes) - 1
	if attempt >= len(d.results) {
		return &recordingConn{}, nil
	}
	result := d.results[attempt]
	if result.err != nil {
		return nil, result.err
	}
	if result.conn == nil {
		return &recordingConn{}, nil
	}
	return result.conn, nil
}

type recordingConn struct {
	deadlines      []time.Time
	deadlineTimes  []time.Time
	readIndex      int
	readOffset     int
	readDelays     []time.Duration
	readErr        error
	writeErr       error
	deadlineErr    error
	writes         []string
	responseChunks []string
}

func (c *recordingConn) Read(b []byte) (int, error) {
	responses := c.responses()
	if c.readIndex >= len(responses) {
		return 0, io.EOF
	}
	if c.readOffset == 0 && c.readIndex < len(c.readDelays) && c.readDelays[c.readIndex] > 0 {
		time.Sleep(c.readDelays[c.readIndex])
	}
	if c.readErr != nil {
		return 0, c.readErr
	}
	response := responses[c.readIndex]
	n := copy(b, response[c.readOffset:])
	c.readOffset += n
	if c.readOffset >= len(response) {
		c.readIndex++
		c.readOffset = 0
	}
	return n, nil
}

func (c *recordingConn) Write(b []byte) (int, error) {
	c.writes = append(c.writes, string(b))
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return len(b), nil
}

func (c *recordingConn) Close() error {
	return nil
}

func (c *recordingConn) LocalAddr() net.Addr {
	return &net.TCPAddr{}
}

func (c *recordingConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{}
}

func (c *recordingConn) SetDeadline(t time.Time) error {
	c.deadlines = append(c.deadlines, t)
	c.deadlineTimes = append(c.deadlineTimes, time.Now())
	return c.deadlineErr
}

func (c *recordingConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *recordingConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (c *recordingConn) responses() []string {
	if c.responseChunks != nil {
		return c.responseChunks
	}
	return []string{
		"HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n",
		"HTTP/1.1 204 No Content\r\nContent-Length: 0\r\n\r\n",
	}
}

func testTrojanProxy() *ProxyInfo {
	return &ProxyInfo{
		Name:       "Trojan One",
		Tag:        "trojan-one",
		Protocol:   "trojan",
		Server:     "example.com",
		ServerPort: 443,
		Password:   "secret",
	}
}
