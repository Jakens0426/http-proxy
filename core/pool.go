package core

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/json/badoption"
)

type PoolInstance struct {
	Proxy     *ProxyInfo
	Port      int
	Box       *box.Box
	Latency   int
	StartedAt time.Time
	closeLog  func()
}

type PoolStatus struct {
	Instances []PoolInstanceStatus `json:"instances"`
	Count     int                  `json:"count"`
}

type PoolInstanceStatus struct {
	Tag      string `json:"tag"`
	Name     string `json:"name"`
	HTTP     string `json:"http"`
	Port     int    `json:"port"`
	Latency  int    `json:"latency"`
	Protocol string `json:"protocol"`
	Uptime   string `json:"uptime"`
}

type ProxyPool struct {
	mu          sync.Mutex
	instances   []*PoolInstance
	portByTag   map[string]int
	usedPorts   map[int]bool
	upstream    string
	username    string
	password    string
	requestLogs requestLogRecorder
}

type requestLogRecorder interface {
	Add(RequestLogEntry) (RequestLogEntry, error)
}

type poolInstanceSnapshot struct {
	proxy   *ProxyInfo
	port    int
	latency int
}

func NewProxyPool() *ProxyPool {
	return &ProxyPool{
		portByTag: make(map[string]int),
		usedPorts: make(map[int]bool),
	}
}

func (p *ProxyPool) SetRequestLogRecorder(recorder requestLogRecorder) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requestLogs = recorder
}

func (p *ProxyPool) SetRuntimeConfig(upstream, username, password string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.upstream == upstream && p.username == username && p.password == password {
		return
	}
	p.upstream = upstream
	p.username = username
	p.password = password
	p.stopLocked()
}

func (p *ProxyPool) ReloadRuntimeConfig(upstream, username, password string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.upstream == upstream && p.username == username && p.password == password {
		return
	}

	snapshots := make([]poolInstanceSnapshot, 0, len(p.instances))
	for _, inst := range p.instances {
		if inst == nil || inst.Proxy == nil {
			continue
		}
		snapshots = append(snapshots, poolInstanceSnapshot{
			proxy:   inst.Proxy,
			port:    inst.Port,
			latency: inst.Latency,
		})
	}

	p.upstream = upstream
	p.username = username
	p.password = password
	if len(snapshots) == 0 {
		p.stopLocked()
		return
	}

	log.Printf("[pool] reloading runtime config for %d instances", len(snapshots))
	p.stopLocked()

	started := 0
	newInstances := make([]*PoolInstance, 0, len(snapshots))
	for _, snapshot := range snapshots {
		tag := snapshot.proxy.Tag
		port, err := p.assignPreferredPort(tag, snapshot.port)
		if err != nil {
			log.Printf("[pool] no port available while reloading %s: %v", tag, err)
			continue
		}
		inst := p.startInstance(snapshot.proxy, port, snapshot.latency, p.upstream, p.username, p.password)
		if inst == nil {
			p.freePort(tag)
			continue
		}
		newInstances = append(newInstances, inst)
		started++
	}
	p.instances = newInstances
	log.Printf("[pool] runtime config reload done: restored=%d/%d", started, len(snapshots))
}

func (p *ProxyPool) SetUpstream(raw string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.upstream == raw {
		return
	}
	p.upstream = raw
	p.stopLocked()
}

func (p *ProxyPool) HotSwap(proxies []*ProxyInfo, latencies map[string]int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	log.Printf("[pool] HotSwap: want %d instances, current %d", len(proxies), len(p.instances))

	oldByTag := make(map[string]*PoolInstance)
	for _, inst := range p.instances {
		oldByTag[inst.Proxy.Tag] = inst
	}

	seen := make(map[string]bool)
	var newInstances []*PoolInstance
	kept, started, stopped := 0, 0, 0

	for _, proxy := range proxies {
		tag := proxy.Tag
		if seen[tag] {
			continue
		}
		seen[tag] = true

		if old, ok := oldByTag[tag]; ok {
			old.Latency = latencies[tag]
			newInstances = append(newInstances, old)
			delete(oldByTag, tag)
			kept++
		} else {
			port, err := p.assignPort(tag)
			if err != nil {
				log.Printf("[pool] no port available for %s: %v", tag, err)
				continue
			}
			inst := p.startInstance(proxy, port, latencies[tag], p.upstream, p.username, p.password)
			if inst == nil {
				p.freePort(tag)
				continue
			}
			newInstances = append(newInstances, inst)
			started++
		}
	}

	for _, old := range oldByTag {
		log.Printf("[pool] closing removed instance %s (port %d)", old.Proxy.Tag, old.Port)
		if err := old.Close(); err != nil {
			log.Printf("[pool] error closing %s: %v", old.Proxy.Tag, err)
		}
		p.freePort(old.Proxy.Tag)
		stopped++
	}

	p.instances = newInstances
	log.Printf("[pool] HotSwap done: kept=%d started=%d stopped=%d total=%d", kept, started, stopped, len(p.instances))
}

func (p *ProxyPool) GetPorts(tags []string) map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()

	ports := make(map[string]int, len(tags))
	for _, tag := range tags {
		if port, ok := p.portByTag[tag]; ok {
			ports[tag] = port
		}
	}
	return ports
}

func (p *ProxyPool) GetStatus() *PoolStatus {
	p.mu.Lock()
	defer p.mu.Unlock()

	status := &PoolStatus{
		Count: len(p.instances),
	}
	status.Instances = make([]PoolInstanceStatus, 0, len(p.instances))
	for _, inst := range p.instances {
		uptime := time.Since(inst.StartedAt).Truncate(time.Second).String()
		status.Instances = append(status.Instances, PoolInstanceStatus{
			Tag:      inst.Proxy.Tag,
			Name:     inst.Proxy.Name,
			HTTP:     FormatHTTPProxyURL(inst.Port, p.username, p.password),
			Port:     inst.Port,
			Latency:  inst.Latency,
			Protocol: inst.Proxy.Protocol,
			Uptime:   uptime,
		})
	}
	return status
}

func (p *ProxyPool) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

func (p *ProxyPool) stopLocked() {
	log.Printf("[pool] stopping all %d instances", len(p.instances))
	for _, inst := range p.instances {
		log.Printf("[pool] closing %s (port %d)", inst.Proxy.Tag, inst.Port)
		if err := inst.Close(); err != nil {
			log.Printf("[pool] error closing %s: %v", inst.Proxy.Tag, err)
		}
	}
	p.instances = nil
	p.portByTag = make(map[string]int)
	p.usedPorts = make(map[int]bool)
	log.Printf("[pool] all instances stopped")
}

func (p *ProxyPool) assignPort(tag string) (int, error) {
	if port, ok := p.portByTag[tag]; ok {
		return port, nil
	}
	for port := PoolPortStart; port <= PoolPortEnd; port++ {
		if !p.usedPorts[port] {
			p.portByTag[tag] = port
			p.usedPorts[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("port pool exhausted (%d-%d)", PoolPortStart, PoolPortEnd)
}

func (p *ProxyPool) assignPreferredPort(tag string, preferred int) (int, error) {
	if port, ok := p.portByTag[tag]; ok {
		return port, nil
	}
	if preferred >= PoolPortStart && preferred <= PoolPortEnd && !p.usedPorts[preferred] {
		p.portByTag[tag] = preferred
		p.usedPorts[preferred] = true
		return preferred, nil
	}
	return p.assignPort(tag)
}

func (p *ProxyPool) freePort(tag string) {
	if port, ok := p.portByTag[tag]; ok {
		delete(p.portByTag, tag)
		delete(p.usedPorts, port)
	}
}

func (p *ProxyPool) startInstance(proxy *ProxyInfo, port int, latency int, upstream string, username string, password string) *PoolInstance {
	if err := ValidateProxyForSingBox(proxy); err != nil {
		tag := ""
		if proxy != nil {
			tag = proxy.Tag
		}
		log.Printf("[pool] unsupported proxy %s: %v", tag, err)
		return nil
	}
	if port < PoolPortStart || port > PoolPortEnd {
		log.Printf("[pool] port %d out of range for %s", port, proxy.Tag)
		return nil
	}

	log.Printf("[pool] starting instance %s (%s) on port %d, latency=%dms, upstream=%q",
		proxy.Tag, proxy.Protocol, port, latency, upstream)

	ctx := include.Context(context.Background())
	lAddr := badoption.Addr(netip.MustParseAddr("127.0.0.1"))
	logOptions := &option.LogOptions{Level: "warn"}
	var logPath string
	if p.requestLogs != nil {
		preparedLogPath, err := prepareRequestRuntimeLogPath(proxy.Tag, port)
		if err != nil {
			log.Printf("[pool] request log capture disabled for %s: %v", proxy.Tag, err)
		} else {
			logPath = preparedLogPath
			logOptions = &option.LogOptions{
				Level:  "info",
				Output: logPath,
			}
		}
	}

	outbounds := []option.Outbound{}
	if upstream != "" {
		upstreamOutbound, err := buildUpstreamOutbound(upstream)
		if err != nil {
			log.Printf("[pool] invalid upstream %q for %s: %v", upstream, proxy.Tag, err)
			return nil
		}
		proxyOut := proxyToOutbound(proxy)
		applyDetour(&proxyOut, "upstream")
		outbounds = append(outbounds, proxyOut)
		outbounds = append(outbounds, upstreamOutbound)
	} else {
		outbounds = append(outbounds, proxyToOutbound(proxy))
	}
	outbounds = append(outbounds, option.Outbound{
		Type: C.TypeDirect,
		Tag:  "direct",
	})

	inboundOptions := &option.HTTPMixedInboundOptions{
		ListenOptions: option.ListenOptions{
			Listen:     &lAddr,
			ListenPort: uint16(port),
			ReuseAddr:  true,
		},
	}
	if username != "" || password != "" {
		inboundOptions.Users = []auth.User{{
			Username: username,
			Password: password,
		}}
	}

	b, err := box.New(box.Options{
		Options: option.Options{
			Log: logOptions,
			Inbounds: []option.Inbound{
				{
					Type:    C.TypeMixed,
					Tag:     "mixed-in",
					Options: inboundOptions,
				},
			},
			Outbounds: outbounds,
			Route: &option.RouteOptions{
				Final: proxy.Tag,
			},
		},
		Context: ctx,
	})
	if err != nil {
		log.Printf("[pool] box.New(%s): %v", proxy.Tag, err)
		return nil
	}

	if err := b.Start(); err != nil {
		b.Close()
		log.Printf("[pool] box.Start(%s): %v", proxy.Tag, err)
		return nil
	}

	log.Printf("[pool] instance %s started on port %d", proxy.Tag, port)
	closeLog := p.captureRequestLogs(proxy, port, logPath)
	return &PoolInstance{
		Proxy:     proxy,
		Port:      port,
		Box:       b,
		Latency:   latency,
		StartedAt: time.Now(),
		closeLog:  closeLog,
	}
}

func (inst *PoolInstance) Close() error {
	if inst == nil {
		return nil
	}
	var err error
	if inst.Box == nil {
		if inst.closeLog != nil {
			inst.closeLog()
			inst.closeLog = nil
		}
		return nil
	}
	err = inst.Box.Close()
	if inst.closeLog != nil {
		inst.closeLog()
		inst.closeLog = nil
	}
	return err
}

func (p *ProxyPool) captureRequestLogs(proxy *ProxyInfo, port int, logPath string) func() {
	if proxy == nil || p.requestLogs == nil || logPath == "" {
		return nil
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		var offset int64
		for {
			nextOffset, err := p.readNewRequestLogLines(logPath, offset, proxy, port)
			if err == nil {
				offset = nextOffset
			} else if !os.IsNotExist(err) {
				log.Printf("[pool] could not read request log file for %s: %v", proxy.Tag, err)
			}

			select {
			case <-stop:
				if nextOffset, err := p.readNewRequestLogLines(logPath, offset, proxy, port); err == nil {
					offset = nextOffset
				}
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(stop)
			<-done
			if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
				log.Printf("[pool] could not remove request log file %s: %v", logPath, err)
			}
		})
	}
}

func (p *ProxyPool) readNewRequestLogLines(logPath string, offset int64, proxy *ProxyInfo, port int) (int64, error) {
	file, err := os.Open(logPath)
	if err != nil {
		return offset, err
	}
	defer file.Close()

	if offset == 0 {
		_, err := file.Seek(0, io.SeekStart)
		if err != nil {
			return offset, err
		}
	} else {
		_, err := file.Seek(offset, io.SeekStart)
		if err != nil {
			return offset, err
		}
	}

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			offset += int64(len(line))
			p.saveRequestLogLine(strings.TrimSpace(line), proxy, port)
		}
		if err != nil {
			if err == io.EOF {
				return offset, nil
			}
			return offset, err
		}
	}
}

func (p *ProxyPool) saveRequestLogLine(line string, proxy *ProxyInfo, port int) {
	network, destination, ok := ParseSingBoxRequestLog(line)
	if !ok {
		return
	}
	_, err := p.requestLogs.Add(RequestLogEntry{
		Time:        time.Now().UTC(),
		ProxyTag:    proxy.Tag,
		ProxyName:   proxy.Name,
		Port:        port,
		Protocol:    proxy.Protocol,
		Network:     network,
		Destination: destination,
		Message:     line,
	})
	if err != nil {
		log.Printf("[pool] could not save request log for %s: %v", proxy.Tag, err)
	}
}

func prepareRequestRuntimeLogPath(tag string, port int) (string, error) {
	dir := filepath.Join(DefaultRequestLogDir, "runtime")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, tag)
	if name == "" {
		name = "proxy"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	path := filepath.Join(dir, fmt.Sprintf("sing-box-%d-%d-%s.log", os.Getpid(), port, name))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

func FormatHTTPProxyURL(port int, username string, password string) string {
	if username == "" && password == "" {
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return fmt.Sprintf("http://%s@127.0.0.1:%d", url.UserPassword(username, password).String(), port)
}

func applyDetour(out *option.Outbound, detourTag string) {
	switch o := out.Options.(type) {
	case *option.VLESSOutboundOptions:
		o.Detour = detourTag
	case *option.TrojanOutboundOptions:
		o.Detour = detourTag
	case *option.ShadowsocksOutboundOptions:
		o.Detour = detourTag
	}
}

func buildUpstreamOutbound(raw string) (option.Outbound, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return option.Outbound{}, fmt.Errorf("upstream proxy is empty")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return option.Outbound{}, fmt.Errorf("parse upstream proxy: %w", err)
	}
	proxyType := strings.ToLower(u.Scheme)
	switch proxyType {
	case "http", "https":
		server, username, password, port, err := parseStandardUpstreamURL(u)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{
			Type: C.TypeHTTP,
			Tag:  "upstream",
			Options: &option.HTTPOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     server,
					ServerPort: uint16(port),
				},
				Username: username,
				Password: password,
			},
		}, nil
	case "socks5", "socks":
		server, username, password, port, err := parseStandardUpstreamURL(u)
		if err != nil {
			return option.Outbound{}, err
		}
		return option.Outbound{
			Type: C.TypeSOCKS,
			Tag:  "upstream",
			Options: &option.SOCKSOutboundOptions{
				ServerOptions: option.ServerOptions{
					Server:     server,
					ServerPort: uint16(port),
				},
				Username: username,
				Password: password,
			},
		}, nil
	case "ss", "vless", "trojan":
		p := ParseProxyURL(raw)
		if p == nil {
			return option.Outbound{}, fmt.Errorf("parse %s upstream proxy failed", proxyType)
		}
		if err := ValidateProxyForSingBox(p); err != nil {
			return option.Outbound{}, err
		}
		if p.Server == "" {
			return option.Outbound{}, fmt.Errorf("upstream proxy host is empty")
		}
		if p.ServerPort <= 0 || p.ServerPort > 65535 {
			return option.Outbound{}, fmt.Errorf("upstream proxy port is invalid")
		}
		outbound := proxyToOutbound(p)
		if outbound.Type == C.TypeDirect {
			return option.Outbound{}, fmt.Errorf("unsupported upstream proxy protocol %q", proxyType)
		}
		outbound.Tag = "upstream"
		return outbound, nil
	default:
		return option.Outbound{}, fmt.Errorf("unsupported upstream proxy protocol %q", proxyType)
	}
}

func parseStandardUpstreamURL(u *url.URL) (server, username, password string, port int, err error) {
	server = u.Hostname()
	if server == "" {
		err = fmt.Errorf("upstream proxy host is empty")
		return
	}
	portRaw := u.Port()
	if portRaw == "" {
		err = fmt.Errorf("upstream proxy port is empty")
		return
	}
	port64, parseErr := strconv.ParseUint(portRaw, 10, 16)
	if parseErr != nil || port64 == 0 {
		err = fmt.Errorf("upstream proxy port is invalid")
		return
	}
	port = int(port64)
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	return
}
