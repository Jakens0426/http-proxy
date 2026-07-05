package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRequestLogStorePersistsDailyLogs(t *testing.T) {
	store := NewRequestLogStore(filepath.Join(t.TempDir(), "request-logs"))
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close request log store: %v", err)
		}
	})

	firstTime := time.Date(2026, 7, 5, 10, 0, 0, 0, time.Local)
	secondTime := firstTime.Add(time.Minute)
	oldTime := firstTime.AddDate(0, 0, -1)

	if _, err := store.Add(RequestLogEntry{
		Time:        firstTime,
		ProxyTag:    "tag-a",
		ProxyName:   "Proxy A",
		Port:        10000,
		Protocol:    "vless",
		Network:     "tcp",
		Destination: "example.com:443",
		Message:     "inbound/mixed[mixed-in]: inbound connection to example.com:443",
	}); err != nil {
		t.Fatalf("add first log: %v", err)
	}
	if _, err := store.Add(RequestLogEntry{
		Time:        secondTime,
		ProxyTag:    "tag-a",
		ProxyName:   "Proxy A",
		Port:        10000,
		Protocol:    "vless",
		Network:     "tcp",
		Destination: "openai.com:443",
		Message:     "inbound/mixed[mixed-in]: inbound connection to openai.com:443",
	}); err != nil {
		t.Fatalf("add second log: %v", err)
	}
	if _, err := store.Add(RequestLogEntry{
		Time:        oldTime,
		ProxyTag:    "tag-b",
		ProxyName:   "Proxy B",
		Port:        10001,
		Protocol:    "trojan",
		Network:     "udp",
		Destination: "1.1.1.1:53",
		Message:     "inbound/mixed[mixed-in]: inbound packet connection to 1.1.1.1:53",
	}); err != nil {
		t.Fatalf("add old log: %v", err)
	}

	dates, err := store.ListDates()
	if err != nil {
		t.Fatalf("list dates: %v", err)
	}
	if len(dates) != 2 || dates[0] != "2026-07-05" || dates[1] != "2026-07-04" {
		t.Fatalf("dates = %#v, want descending daily dates", dates)
	}

	logs, err := store.List("2026-07-05", 1)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("log count = %d, want 1", len(logs))
	}
	if logs[0].Destination != "openai.com:443" || logs[0].ProxyTag != "tag-a" {
		t.Fatalf("latest log = %+v, want second log", logs[0])
	}
}

func TestRequestLogStoreDeletesDates(t *testing.T) {
	store := NewRequestLogStore(filepath.Join(t.TempDir(), "request-logs"))
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close request log store: %v", err)
		}
	})

	for _, day := range []int{5, 6} {
		if _, err := store.Add(RequestLogEntry{
			Time:        time.Date(2026, 7, day, 10, 0, 0, 0, time.Local),
			ProxyTag:    "tag",
			ProxyName:   "Proxy",
			Port:        10000,
			Protocol:    "vless",
			Network:     "tcp",
			Destination: "example.com:443",
			Message:     "message",
		}); err != nil {
			t.Fatalf("add log: %v", err)
		}
	}

	if err := store.DeleteDate("2026-07-05"); err != nil {
		t.Fatalf("delete date: %v", err)
	}
	dates, err := store.ListDates()
	if err != nil {
		t.Fatalf("list dates: %v", err)
	}
	if len(dates) != 1 || dates[0] != "2026-07-06" {
		t.Fatalf("dates after delete = %#v, want only 2026-07-06", dates)
	}

	if err := store.DeleteAll(); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	dates, err = store.ListDates()
	if err != nil {
		t.Fatalf("list dates after delete all: %v", err)
	}
	if len(dates) != 0 {
		t.Fatalf("dates after delete all = %#v, want empty", dates)
	}
}

func TestParseSingBoxRequestLog(t *testing.T) {
	tests := []struct {
		name            string
		message         string
		wantNetwork     string
		wantDestination string
		wantOK          bool
	}{
		{
			name:            "tcp",
			message:         "inbound/mixed[mixed-in]: inbound connection to example.com:443",
			wantNetwork:     "tcp",
			wantDestination: "example.com:443",
			wantOK:          true,
		},
		{
			name:            "tcp with user",
			message:         "inbound/mixed[mixed-in]: [user] inbound connection to example.com:443",
			wantNetwork:     "tcp",
			wantDestination: "example.com:443",
			wantOK:          true,
		},
		{
			name:            "udp",
			message:         "inbound/mixed[mixed-in]: inbound packet connection to 1.1.1.1:53",
			wantNetwork:     "udp",
			wantDestination: "1.1.1.1:53",
			wantOK:          true,
		},
		{
			name:        "udp without destination",
			message:     "inbound/mixed[mixed-in]: inbound packet connection",
			wantNetwork: "udp",
			wantOK:      true,
		},
		{
			name:    "unrelated",
			message: "outbound/vless[tag]: outbound connection to example.com:443",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network, destination, ok := ParseSingBoxRequestLog(tt.message)
			if ok != tt.wantOK || network != tt.wantNetwork || destination != tt.wantDestination {
				t.Fatalf("ParseSingBoxRequestLog() = (%q, %q, %t), want (%q, %q, %t)", network, destination, ok, tt.wantNetwork, tt.wantDestination, tt.wantOK)
			}
		})
	}
}
