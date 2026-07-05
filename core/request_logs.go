package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultRequestLogDir   = "data/request-logs"
	DefaultRequestLogLimit = 200
)

type RequestLogEntry struct {
	ID          int64     `json:"id"`
	Time        time.Time `json:"time"`
	ProxyTag    string    `json:"proxy_tag"`
	ProxyName   string    `json:"proxy_name"`
	Port        int       `json:"port"`
	Protocol    string    `json:"protocol"`
	Network     string    `json:"network"`
	Destination string    `json:"destination"`
	Message     string    `json:"message"`
}

type RequestLogStore struct {
	mu          sync.Mutex
	dir         string
	currentDate string
	currentDB   *sql.DB
}

func NewRequestLogStore(dir string) *RequestLogStore {
	if dir == "" {
		dir = DefaultRequestLogDir
	}
	return &RequestLogStore{dir: dir}
}

func (s *RequestLogStore) Add(entry RequestLogEntry) (RequestLogEntry, error) {
	if s == nil {
		return entry, nil
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	date := requestLogDateForTime(entry.Time)

	s.mu.Lock()
	defer s.mu.Unlock()

	db, err := s.openDateLocked(date)
	if err != nil {
		return entry, err
	}
	result, err := db.ExecContext(
		context.Background(),
		`INSERT INTO request_logs (
			time, proxy_tag, proxy_name, port, protocol, network, destination, message
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		formatDBTime(entry.Time.UTC()),
		entry.ProxyTag,
		entry.ProxyName,
		entry.Port,
		entry.Protocol,
		entry.Network,
		entry.Destination,
		entry.Message,
	)
	if err != nil {
		return entry, err
	}
	entry.ID, _ = result.LastInsertId()
	return entry, nil
}

func (s *RequestLogStore) ListDates() ([]string, error) {
	if s == nil {
		return []string{}, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}

	dates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".db" {
			continue
		}
		date := strings.TrimSuffix(name, ".db")
		if IsRequestLogDate(date) {
			dates = append(dates, date)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	return dates, nil
}

func (s *RequestLogStore) List(date string, limit int) ([]RequestLogEntry, error) {
	if s == nil {
		return []RequestLogEntry{}, nil
	}
	date = normalizeRequestLogDate(date)
	if !IsRequestLogDate(date) {
		return nil, fmt.Errorf("invalid request log date %q", date)
	}
	if limit <= 0 {
		limit = DefaultRequestLogLimit
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.pathForDate(date)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []RequestLogEntry{}, nil
		}
		return nil, err
	}

	db, err := s.openDateLocked(date)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(
		context.Background(),
		`SELECT id, time, proxy_tag, proxy_name, port, protocol, network, destination, message
		 FROM request_logs
		 ORDER BY id DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	logs := make([]RequestLogEntry, 0)
	for rows.Next() {
		var entry RequestLogEntry
		var rawTime string
		if err := rows.Scan(
			&entry.ID,
			&rawTime,
			&entry.ProxyTag,
			&entry.ProxyName,
			&entry.Port,
			&entry.Protocol,
			&entry.Network,
			&entry.Destination,
			&entry.Message,
		); err != nil {
			return nil, err
		}
		t, err := parseDBTime(rawTime)
		if err != nil {
			return nil, err
		}
		entry.Time = t
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

func (s *RequestLogStore) DeleteDate(date string) error {
	if s == nil {
		return nil
	}
	if !IsRequestLogDate(date) {
		return fmt.Errorf("invalid request log date %q", date)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.currentDate == date {
		if err := s.closeCurrentLocked(); err != nil {
			return err
		}
	}
	err := os.Remove(s.pathForDate(date))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *RequestLogStore) DeleteAll() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.closeCurrentLocked(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".db" {
			continue
		}
		date := strings.TrimSuffix(name, ".db")
		if !IsRequestLogDate(date) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *RequestLogStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCurrentLocked()
}

func IsRequestLogDate(date string) bool {
	if len(date) != len("2006-01-02") {
		return false
	}
	parsed, err := time.ParseInLocation(time.DateOnly, date, time.Local)
	if err != nil {
		return false
	}
	return parsed.Format(time.DateOnly) == date
}

func ParseSingBoxRequestLog(message string) (network string, destination string, ok bool) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", "", false
	}

	const tcpMarker = "inbound connection to "
	if idx := strings.Index(message, tcpMarker); idx >= 0 {
		destination = strings.TrimSpace(message[idx+len(tcpMarker):])
		if destination == "" {
			return "", "", false
		}
		return "tcp", destination, true
	}

	const udpMarker = "inbound packet connection to "
	if idx := strings.Index(message, udpMarker); idx >= 0 {
		destination = strings.TrimSpace(message[idx+len(udpMarker):])
		return "udp", destination, true
	}
	if strings.Contains(message, "inbound packet connection") {
		return "udp", "", true
	}

	return "", "", false
}

func normalizeRequestLogDate(date string) string {
	date = strings.TrimSpace(date)
	if date == "" {
		return requestLogDateForTime(time.Now())
	}
	return date
}

func requestLogDateForTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.In(time.Local).Format(time.DateOnly)
}

func (s *RequestLogStore) openDateLocked(date string) (*sql.DB, error) {
	if s.currentDB != nil && s.currentDate == date {
		return s.currentDB, nil
	}
	if err := s.closeCurrentLocked(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", s.pathForDate(date))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		db.Close()
		return nil, err
	}
	if err := initRequestLogSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	s.currentDate = date
	s.currentDB = db
	return db, nil
}

func (s *RequestLogStore) closeCurrentLocked() error {
	if s.currentDB == nil {
		s.currentDate = ""
		return nil
	}
	err := s.currentDB.Close()
	s.currentDB = nil
	s.currentDate = ""
	return err
}

func (s *RequestLogStore) pathForDate(date string) string {
	return filepath.Join(s.dir, date+".db")
}

func initRequestLogSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			time TEXT NOT NULL,
			proxy_tag TEXT NOT NULL,
			proxy_name TEXT NOT NULL,
			port INTEGER NOT NULL,
			protocol TEXT NOT NULL,
			network TEXT NOT NULL,
			destination TEXT NOT NULL,
			message TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_time ON request_logs(time DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}
