package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"http-proxy/core"
)

type noopService struct{}

func (noopService) ListSubscriptions() []*core.Subscription                { return nil }
func (noopService) AddSubscription(string) (*core.Subscription, error)     { return nil, nil }
func (noopService) RemoveSubscription(string) error                        { return nil }
func (noopService) RefreshSubscription(string) (*core.Subscription, error) { return nil, nil }
func (noopService) GetAllProxies() []*core.ProxyInfo                       { return nil }
func (noopService) GetAvailableProxies(int) ([]AvailableProxy, error)      { return nil, nil }
func (noopService) GetAvailableStatus() AvailableStatus                    { return AvailableStatus{} }
func (noopService) GetPoolStatus() *core.PoolStatus                        { return &core.PoolStatus{} }
func (noopService) StopPool()                                              {}
func (noopService) GetConfig() core.AppConfig                              { return core.AppConfig{} }
func (noopService) SetConfig(core.AppConfig) error                         { return nil }
func (noopService) GetTestResults() map[string]*core.TestResult            { return nil }
func (noopService) TestProxy(string) (*core.TestResult, error)             { return nil, nil }
func (noopService) TestUpstream(string, string) (*core.TestResult, error)  { return nil, nil }
func (noopService) GetDiagnosticLogs(int) []core.DiagnosticEvent           { return nil }
func (noopService) ClearDiagnosticLogs()                                   {}
func (noopService) GetRequestLogDates() ([]string, error)                  { return nil, nil }
func (noopService) GetRequestLogs(string, int) ([]core.RequestLogEntry, error) {
	return nil, nil
}
func (noopService) ClearRequestLogs(string) error { return nil }

type authTestService struct {
	noopService
	config         core.AppConfig
	availableCalls int
	availableCount int
	available      []AvailableProxy
}

func (s *authTestService) GetConfig() core.AppConfig {
	return s.config
}

func (s *authTestService) GetAvailableProxies(count int) ([]AvailableProxy, error) {
	s.availableCalls++
	s.availableCount = count
	if s.available != nil {
		return s.available, nil
	}
	return []AvailableProxy{{
		HTTP:     "http://127.0.0.1:10000",
		Name:     "Proxy",
		Tag:      "proxy",
		Latency:  12,
		Protocol: "direct",
	}}, nil
}

func TestServerServesEmbeddedWebUI(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><div id=\"app\"></div>")},
		"assets/index.js":  {Data: []byte("console.log('ok')")},
		"assets/index.css": {Data: []byte("body{}")},
	}
	srv := NewServer(noopService{}, ui)

	tests := []struct {
		path        string
		wantStatus  int
		wantSnippet string
	}{
		{path: "/", wantStatus: http.StatusOK, wantSnippet: "id=\"app\""},
		{path: "/proxies", wantStatus: http.StatusOK, wantSnippet: "id=\"app\""},
		{path: "/assets/index.js", wantStatus: http.StatusOK, wantSnippet: "console.log"},
		{path: "/api/missing", wantStatus: http.StatusNotFound, wantSnippet: "404"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			srv.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if body := rec.Body.String(); !strings.Contains(body, tt.wantSnippet) {
				t.Fatalf("body = %q, want snippet %q", body, tt.wantSnippet)
			}
		})
	}
}

func TestServerAdminTokenProtectsManagementAPI(t *testing.T) {
	svc := &authTestService{config: core.AppConfig{AdminToken: "admin-token"}}
	srv := NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/subscriptions", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET without admin token status = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/subscriptions", nil)
	req.Header.Set(adminTokenHeader, "admin-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET with admin token status = %d, want 200", rec.Code)
	}

	openSrv := NewServer(&authTestService{})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/subscriptions", nil)
	openSrv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET with empty admin token status = %d, want 200", rec.Code)
	}
}

func TestServerAvailableProxiesRequiresQueryToken(t *testing.T) {
	svc := &authTestService{config: core.AppConfig{AvailableToken: "available-token"}}
	srv := NewServer(svc)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantCalls  int
	}{
		{name: "missing", path: "/api/proxies/available?count=1", wantStatus: http.StatusForbidden, wantCalls: 0},
		{name: "wrong", path: "/api/proxies/available?count=1&token=wrong", wantStatus: http.StatusForbidden, wantCalls: 0},
		{name: "ok", path: "/api/proxies/available?count=1&token=available-token", wantStatus: http.StatusOK, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			srv.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if svc.availableCalls != tt.wantCalls {
				t.Fatalf("available calls = %d, want %d", svc.availableCalls, tt.wantCalls)
			}
		})
	}
}

func TestServerAvailableProxiesTextRequiresQueryToken(t *testing.T) {
	svc := &authTestService{config: core.AppConfig{AvailableToken: "available-token"}}
	srv := NewServer(svc)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantCalls  int
	}{
		{name: "missing", path: "/api/proxies/available.txt?count=1", wantStatus: http.StatusForbidden, wantCalls: 0},
		{name: "wrong", path: "/api/proxies/available.txt?count=1&token=wrong", wantStatus: http.StatusForbidden, wantCalls: 0},
		{name: "ok", path: "/api/proxies/available.txt?count=1&token=available-token", wantStatus: http.StatusOK, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc.availableCalls = 0
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			srv.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if svc.availableCalls != tt.wantCalls {
				t.Fatalf("available calls = %d, want %d", svc.availableCalls, tt.wantCalls)
			}
		})
	}
}

func TestServerAvailableProxiesRequiresConfiguredToken(t *testing.T) {
	svc := &authTestService{}
	srv := NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxies/available?count=1&token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if svc.availableCalls != 0 {
		t.Fatalf("available calls = %d, want 0", svc.availableCalls)
	}
}

func TestServerAvailableStatusUsesAdminToken(t *testing.T) {
	svc := &authTestService{config: core.AppConfig{AdminToken: "admin-token", AvailableToken: "available-token"}}
	srv := NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pool/available/status?token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status without admin token = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/pool/available/status?token=available-token", nil)
	req.Header.Set(adminTokenHeader, "admin-token")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status with admin token = %d, want 200", rec.Code)
	}
}

func TestServerAvailableProxiesTextReturnsPlainLines(t *testing.T) {
	svc := &authTestService{
		config: core.AppConfig{AvailableToken: "available-token"},
		available: []AvailableProxy{
			{HTTP: "http://127.0.0.1:10000"},
			{HTTP: "http://user:pass@127.0.0.1:10001"},
			{HTTP: ""},
			{HTTP: "not-a-url"},
		},
	}
	srv := NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxies/available.txt?count=2&token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q, want text/plain; charset=utf-8", contentType)
	}
	want := "127.0.0.1:10000\n127.0.0.1:10001\n"
	if body := rec.Body.String(); body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if svc.availableCount != 2 {
		t.Fatalf("count = %d, want 2", svc.availableCount)
	}
}

func TestServerAvailableProxiesTextDefaultsCount(t *testing.T) {
	svc := &authTestService{config: core.AppConfig{AvailableToken: "available-token"}}
	srv := NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/proxies/available.txt?token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.availableCount != 10 {
		t.Fatalf("count = %d, want 10", svc.availableCount)
	}
}

func TestServerAvailableProxiesTextRejectsNonGet(t *testing.T) {
	svc := &authTestService{config: core.AppConfig{AvailableToken: "available-token"}}
	srv := NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/proxies/available.txt?token=available-token", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if svc.availableCalls != 0 {
		t.Fatalf("available calls = %d, want 0", svc.availableCalls)
	}
}

func TestServerReturnsEmptyArraysForEmptyCollections(t *testing.T) {
	srv := NewServer(noopService{})

	tests := []string{"/api/subscriptions", "/api/proxies", "/api/logs", "/api/request-logs", "/api/request-logs/dates"}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
				t.Fatalf("body = %q, want []", body)
			}
		})
	}
}

type requestLogTestService struct {
	noopService
	deletedDate string
	deleted     bool
}

func (s *requestLogTestService) GetRequestLogDates() ([]string, error) {
	return []string{"2026-07-05", "2026-07-04"}, nil
}

func (s *requestLogTestService) GetRequestLogs(date string, limit int) ([]core.RequestLogEntry, error) {
	return []core.RequestLogEntry{{
		ID:          int64(limit),
		Time:        time.Date(2026, 7, 5, 1, 2, 3, 0, time.UTC),
		ProxyTag:    "proxy-tag",
		ProxyName:   "Proxy Name",
		Port:        10000,
		Protocol:    "vless",
		Network:     "tcp",
		Destination: "example.com:443",
		Message:     date,
	}}, nil
}

func (s *requestLogTestService) ClearRequestLogs(date string) error {
	s.deleted = true
	s.deletedDate = date
	return nil
}

func TestServerRequestLogEndpoints(t *testing.T) {
	svc := &requestLogTestService{}
	srv := NewServer(svc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/request-logs/dates", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dates status = %d, want 200", rec.Code)
	}
	var dates []string
	if err := json.NewDecoder(rec.Body).Decode(&dates); err != nil {
		t.Fatalf("decode dates: %v", err)
	}
	if len(dates) != 2 || dates[0] != "2026-07-05" {
		t.Fatalf("dates = %#v, want returned dates", dates)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/request-logs?date=2026-07-05&limit=25", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logs status = %d, want 200", rec.Code)
	}
	var logs []core.RequestLogEntry
	if err := json.NewDecoder(rec.Body).Decode(&logs); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if len(logs) != 1 || logs[0].ID != 25 || logs[0].Destination != "example.com:443" {
		t.Fatalf("logs = %+v, want request log", logs)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/request-logs?date=2026-07-05", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete date status = %d, want 200", rec.Code)
	}
	if !svc.deleted || svc.deletedDate != "2026-07-05" {
		t.Fatalf("deleted = %t date = %q, want date delete", svc.deleted, svc.deletedDate)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/request-logs?date=bad-date", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid date status = %d, want 400", rec.Code)
	}
}
