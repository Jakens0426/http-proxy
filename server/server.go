package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"http-proxy/core"
)

const (
	adminTokenHeader         = "X-Admin-Token"
	availableProxiesPath     = "/api/proxies/available"
	availableProxiesTextPath = "/api/proxies/available.txt"
)

type Service interface {
	ListSubscriptions() []*core.Subscription
	AddSubscription(url string) (*core.Subscription, error)
	RemoveSubscription(id string) error
	RefreshSubscription(id string) (*core.Subscription, error)
	GetAllProxies() []*core.ProxyInfo
	GetAvailableProxies(count int) ([]AvailableProxy, error)
	GetPoolStatus() *core.PoolStatus
	StopPool()
	GetConfig() core.AppConfig
	SetConfig(cfg core.AppConfig) error
	GetTestResults() map[string]*core.TestResult
	TestProxy(tag string) (*core.TestResult, error)
	TestUpstream(raw string, targetURL string) (*core.TestResult, error)
	GetDiagnosticLogs(limit int) []core.DiagnosticEvent
	ClearDiagnosticLogs()
	GetRequestLogDates() ([]string, error)
	GetRequestLogs(date string, limit int) ([]core.RequestLogEntry, error)
	ClearRequestLogs(date string) error
}

type StatusError struct {
	Status  int
	Message string
}

func NewStatusError(status int, message string) *StatusError {
	return &StatusError{Status: status, Message: message}
}

func (e *StatusError) Error() string {
	return e.Message
}

func statusFromError(err error) int {
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status
	}
	return http.StatusInternalServerError
}

type AvailableProxy struct {
	HTTP     string `json:"http"`
	Name     string `json:"name"`
	Tag      string `json:"tag"`
	Latency  int    `json:"latency"`
	Protocol string `json:"protocol"`
}

type Server struct {
	svc Service
	mux *http.ServeMux
	ui  fs.FS
}

func NewServer(svc Service, uiFS ...fs.FS) *Server {
	s := &Server{
		svc: svc,
		mux: http.NewServeMux(),
	}
	if len(uiFS) > 0 {
		s.ui = uiFS[0]
	}
	s.registerRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	log.Printf("[http] %s %s", r.Method, r.URL.Path)

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Admin-Token, Authorization")

	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		log.Printf("[http] %s %s -> 204 (%.0fms)", r.Method, r.URL.Path, float64(time.Since(start).Milliseconds()))
		return
	}

	if !s.authorizeAPI(w, r) {
		log.Printf("[http] %s %s -> 403 (%.0fms)", r.Method, r.URL.Path, float64(time.Since(start).Milliseconds()))
		return
	}

	s.mux.ServeHTTP(w, r)
	log.Printf("[http] %s %s -> done (%.0fms)", r.Method, r.URL.Path, float64(time.Since(start).Milliseconds()))
}

func (s *Server) authorizeAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	cfg := s.svc.GetConfig()
	if r.URL.Path == availableProxiesPath || r.URL.Path == availableProxiesTextPath {
		return authorizeAvailableToken(w, r, cfg)
	}
	adminToken := strings.TrimSpace(cfg.AdminToken)
	if adminToken == "" {
		return true
	}
	if secureTokenEqual(strings.TrimSpace(r.Header.Get(adminTokenHeader)), adminToken) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
	return false
}

func authorizeAvailableToken(w http.ResponseWriter, r *http.Request, cfg core.AppConfig) bool {
	availableToken := strings.TrimSpace(cfg.AvailableToken)
	if availableToken == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "available token is not configured"})
		return false
	}
	if secureTokenEqual(strings.TrimSpace(r.URL.Query().Get("token")), availableToken) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
	return false
}

func secureTokenEqual(got string, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/", s.handleRoot)
	s.mux.HandleFunc("/api/subscriptions", s.handleSubscriptions)
	s.mux.HandleFunc("/api/subscriptions/", s.handleSubscriptionByID)
	s.mux.HandleFunc("/api/proxies", s.handleProxies)
	s.mux.HandleFunc("/api/proxies/", s.handleProxyByTag)
	s.mux.HandleFunc(availableProxiesPath, s.handleAvailableProxies)
	s.mux.HandleFunc(availableProxiesTextPath, s.handleAvailableProxiesText)
	s.mux.HandleFunc("/api/pool/status", s.handlePoolStatus)
	s.mux.HandleFunc("/api/pool/stop", s.handlePoolStop)
	s.mux.HandleFunc("/api/config/upstream/test", s.handleConfigUpstreamTest)
	s.mux.HandleFunc("/api/config", s.handleConfig)
	s.mux.HandleFunc("/api/logs/clear", s.handleLogsClear)
	s.mux.HandleFunc("/api/logs", s.handleLogs)
	s.mux.HandleFunc("/api/request-logs/dates", s.handleRequestLogDates)
	s.mux.HandleFunc("/api/request-logs", s.handleRequestLogs)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	if s.ui == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(200)
		w.Write([]byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>HTTP 代理管理器</title></head><body><div id="app">Web UI is not embedded. Run npm --prefix webui run build before packaging.</div></body></html>`))
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if stat, err := fs.Stat(s.ui, name); err == nil && !stat.IsDir() {
		http.ServeFileFS(w, r, s.ui, name)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeFileFS(w, r, s.ui, "index.html")
}

func (s *Server) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		subs := s.svc.ListSubscriptions()
		if subs == nil {
			subs = []*core.Subscription{}
		}
		writeJSON(w, 200, subs)
	case "POST":
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
			return
		}
		if body.URL == "" {
			writeJSON(w, 400, map[string]string{"error": "url is required"})
			return
		}
		sub, err := s.svc.AddSubscription(body.URL)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 201, sub)
	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleSubscriptionByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/subscriptions/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "id is required"})
		return
	}

	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case r.Method == "DELETE" && action == "":
		if err := s.svc.RemoveSubscription(id); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]string{"message": "deleted"})

	case r.Method == "POST" && action == "refresh":
		sub, err := s.svc.RefreshSubscription(id)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, sub)

	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleProxies(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	proxies := s.svc.GetAllProxies()
	results := s.svc.GetTestResults()
	type ProxyView struct {
		Name     string `json:"name"`
		Server   string `json:"server"`
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
		Tag      string `json:"tag"`
		TLS      string `json:"tls"`
		Latency  int    `json:"latency"`
		Err      string `json:"err,omitempty"`
		ShareURL string `json:"share_url,omitempty"`
	}
	view := make([]ProxyView, 0, len(proxies))
	for _, p := range proxies {
		tls := "none"
		if p.TLS != nil {
			if p.TLS.Reality != nil && p.TLS.Reality.Enabled {
				tls = "reality"
			} else {
				tls = "tls"
			}
		}
		latency := -1
		errText := ""
		if results != nil {
			if r, ok := results[p.Tag]; ok {
				latency = r.Latency
				errText = r.Err
			}
		}
		view = append(view, ProxyView{
			Name:     p.Name,
			Server:   p.Server,
			Port:     p.ServerPort,
			Protocol: p.Protocol,
			Tag:      p.Tag,
			TLS:      tls,
			Latency:  latency,
			Err:      errText,
			ShareURL: core.BuildShareURL(p),
		})
	}
	writeJSON(w, 200, view)
}

func (s *Server) handleProxyByTag(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}

	path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/proxies/")
	if !strings.HasSuffix(path, "/test") {
		http.NotFound(w, r)
		return
	}
	escapedTag := strings.TrimSuffix(path, "/test")
	if escapedTag == "" {
		writeJSON(w, 400, map[string]string{"error": "tag is required"})
		return
	}
	tag, err := url.PathUnescape(escapedTag)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid tag"})
		return
	}

	result, err := s.svc.TestProxy(tag)
	if err != nil {
		writeJSON(w, statusFromError(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleAvailableProxies(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	proxies, ok := s.availableProxiesForRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"proxies": nonNilAvailableProxies(proxies),
		"count":   len(proxies),
	})
}

func (s *Server) handleAvailableProxiesText(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	proxies, ok := s.availableProxiesForRequest(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	for _, proxy := range proxies {
		hostPort := availableProxyTextAddress(proxy.HTTP)
		if hostPort == "" {
			continue
		}
		io.WriteString(w, hostPort)
		io.WriteString(w, "\n")
	}
}

func availableProxyTextAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

func (s *Server) availableProxiesForRequest(w http.ResponseWriter, r *http.Request) ([]AvailableProxy, bool) {
	count := availableProxyCount(r)
	proxies, err := s.svc.GetAvailableProxies(count)
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": err.Error()})
		return nil, false
	}
	if proxies == nil || len(proxies) == 0 {
		total := len(s.svc.GetAllProxies())
		if total > 0 {
			writeJSON(w, 503, map[string]string{"error": "no healthy proxies available"})
			return nil, false
		}
	}
	return proxies, true
}

func availableProxyCount(r *http.Request) int {
	countStr := r.URL.Query().Get("count")
	count := 10
	if countStr != "" {
		if c, err := strconv.Atoi(countStr); err == nil && c > 0 {
			count = c
		}
	}
	return count
}

func (s *Server) handlePoolStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, 200, s.svc.GetPoolStatus())
}

func (s *Server) handlePoolStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	s.svc.StopPool()
	writeJSON(w, 200, map[string]string{"message": "pool stopped"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, 200, s.svc.GetConfig())
	case "PUT":
		var cfg core.AppConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
			return
		}
		if err := s.svc.SetConfig(cfg); err != nil {
			writeJSON(w, statusFromError(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]string{"message": "config updated"})
	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleConfigUpstreamTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		UpstreamProxy string `json:"upstream_proxy"`
		TestTarget    string `json:"test_target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	result, err := s.svc.TestUpstream(body.UpstreamProxy, body.TestTarget)
	if err != nil {
		writeJSON(w, statusFromError(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	limit := core.DefaultDiagnosticLogCapacity
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	logs := s.svc.GetDiagnosticLogs(limit)
	if logs == nil {
		logs = []core.DiagnosticEvent{}
	}
	writeJSON(w, 200, logs)
}

func (s *Server) handleLogsClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	s.svc.ClearDiagnosticLogs()
	writeJSON(w, 200, map[string]string{"message": "logs cleared"})
}

func (s *Server) handleRequestLogDates(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	dates, err := s.svc.GetRequestLogDates()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if dates == nil {
		dates = []string{}
	}
	writeJSON(w, 200, dates)
}

func (s *Server) handleRequestLogs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		date := strings.TrimSpace(r.URL.Query().Get("date"))
		if date != "" && !core.IsRequestLogDate(date) {
			writeJSON(w, 400, map[string]string{"error": "invalid date"})
			return
		}
		limit := core.DefaultRequestLogLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		logs, err := s.svc.GetRequestLogs(date, limit)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if logs == nil {
			logs = []core.RequestLogEntry{}
		}
		writeJSON(w, 200, logs)

	case "DELETE":
		date := strings.TrimSpace(r.URL.Query().Get("date"))
		if date != "" && !core.IsRequestLogDate(date) {
			writeJSON(w, 400, map[string]string{"error": "invalid date"})
			return
		}
		if err := s.svc.ClearRequestLogs(date); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]string{"message": "request logs cleared"})

	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func nonNilAvailableProxies(proxies []AvailableProxy) []AvailableProxy {
	if proxies == nil {
		return []AvailableProxy{}
	}
	return proxies
}
