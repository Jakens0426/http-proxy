package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const appConfigID = 1

type Store struct {
	mu       sync.RWMutex
	filePath string
	db       *sql.DB
	initErr  error
	subs     []*Subscription
}

func NewStore(filePath string) *Store {
	if filePath == "" {
		filePath = DatabaseFile
	}
	s := &Store{
		filePath: filePath,
		subs:     make([]*Subscription, 0),
	}
	s.initErr = s.open()
	return s
}

func (s *Store) open() error {
	dir := filepath.Dir(s.filePath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create database directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", s.filePath)
	if err != nil {
		return fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;`); err != nil {
		db.Close()
		return fmt.Errorf("configure sqlite database: %w", err)
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return err
	}

	s.db = db
	return nil
}

func initSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS subscriptions (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
			added_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS subscription_proxies (
			subscription_id TEXT NOT NULL,
			position INTEGER NOT NULL,
			tag TEXT NOT NULL,
			proxy_json TEXT NOT NULL,
			PRIMARY KEY (subscription_id, position),
			FOREIGN KEY (subscription_id) REFERENCES subscriptions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_subscription_proxies_tag ON subscription_proxies(tag)`,
		`CREATE TABLE IF NOT EXISTS app_config (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			upstream_proxy TEXT NOT NULL DEFAULT '',
			test_target TEXT NOT NULL DEFAULT '',
			test_timeout_seconds INTEGER NOT NULL DEFAULT 0,
			available_cache_ttl_seconds INTEGER NOT NULL DEFAULT 30,
			test_result_ttl_minutes INTEGER NOT NULL DEFAULT 120,
			available_quick_probe_seconds INTEGER NOT NULL DEFAULT 1,
			available_quick_concurrency INTEGER NOT NULL DEFAULT 10,
			available_background_concurrency INTEGER NOT NULL DEFAULT 3,
			available_min_warm_pool_size INTEGER NOT NULL DEFAULT 20,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS test_results (
			tag TEXT PRIMARY KEY,
			latency INTEGER NOT NULL,
			err TEXT NOT NULL DEFAULT '',
			timestamp TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("initialize sqlite schema: %w", err)
		}
	}
	if err := ensureAppConfigColumns(db); err != nil {
		return err
	}
	return nil
}

func ensureAppConfigColumns(db *sql.DB) error {
	columns, err := tableColumns(db, "app_config")
	if err != nil {
		return fmt.Errorf("inspect app_config schema: %w", err)
	}
	migrations := map[string]string{
		"admin_token":                      `ALTER TABLE app_config ADD COLUMN admin_token TEXT NOT NULL DEFAULT ''`,
		"available_token":                  `ALTER TABLE app_config ADD COLUMN available_token TEXT NOT NULL DEFAULT ''`,
		"pool_proxy_username":              `ALTER TABLE app_config ADD COLUMN pool_proxy_username TEXT NOT NULL DEFAULT ''`,
		"pool_proxy_password":              `ALTER TABLE app_config ADD COLUMN pool_proxy_password TEXT NOT NULL DEFAULT ''`,
		"available_cache_ttl_seconds":      `ALTER TABLE app_config ADD COLUMN available_cache_ttl_seconds INTEGER NOT NULL DEFAULT 30`,
		"test_result_ttl_minutes":          `ALTER TABLE app_config ADD COLUMN test_result_ttl_minutes INTEGER NOT NULL DEFAULT 120`,
		"available_quick_probe_seconds":    `ALTER TABLE app_config ADD COLUMN available_quick_probe_seconds INTEGER NOT NULL DEFAULT 1`,
		"available_quick_concurrency":      `ALTER TABLE app_config ADD COLUMN available_quick_concurrency INTEGER NOT NULL DEFAULT 10`,
		"available_background_concurrency": `ALTER TABLE app_config ADD COLUMN available_background_concurrency INTEGER NOT NULL DEFAULT 3`,
		"available_min_warm_pool_size":     `ALTER TABLE app_config ADD COLUMN available_min_warm_pool_size INTEGER NOT NULL DEFAULT 20`,
	}
	for name, statement := range migrations {
		if columns[name] {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("migrate app_config.%s: %w", name, err)
		}
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func (s *Store) ready() error {
	if s.initErr != nil {
		return s.initErr
	}
	if s.db == nil {
		return errors.New("sqlite database is not open")
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	subs, err := s.loadSubscriptions(context.Background())
	if err != nil {
		return err
	}
	s.subs = subs
	return nil
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM subscription_proxies`); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM subscriptions`); err != nil {
		tx.Rollback()
		return err
	}
	for _, sub := range s.subs {
		if err := saveSubscriptionTx(context.Background(), tx, sub); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetSubscriptions() []*Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*Subscription, len(s.subs))
	copy(result, s.subs)
	return result
}

func (s *Store) AddSubscription(sub *Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	if err := s.saveSubscription(sub); err != nil {
		return err
	}
	s.subs = append(s.subs, sub)
	return nil
}

func (s *Store) RemoveSubscription(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM subscriptions WHERE id = ?`, id); err != nil {
		return err
	}
	idx := -1
	for i, sub := range s.subs {
		if sub.ID == id {
			idx = i
			break
		}
	}
	if idx >= 0 {
		s.subs = append(s.subs[:idx], s.subs[idx+1:]...)
	}
	return nil
}

func (s *Store) UpdateSubscription(sub *Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	idx := -1
	for i, existing := range s.subs {
		if existing.ID == sub.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	if err := s.saveSubscription(sub); err != nil {
		return err
	}
	s.subs[idx] = sub
	return nil
}

func (s *Store) GetAllProxies() []*ProxyInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]bool)
	var result []*ProxyInfo
	for _, sub := range s.subs {
		if !sub.Enabled {
			continue
		}
		for _, proxy := range sub.Proxies {
			if !seen[proxy.Tag] {
				seen[proxy.Tag] = true
				result = append(result, proxy)
			}
		}
	}
	return result
}

func (s *Store) LoadConfig() (AppConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := AppConfig{}
	cfg.Default()
	if err := s.ready(); err != nil {
		return cfg, err
	}

	row := s.db.QueryRow(
		`SELECT upstream_proxy, test_target, test_timeout_seconds, admin_token, available_token,
			pool_proxy_username, pool_proxy_password, available_cache_ttl_seconds, test_result_ttl_minutes,
			available_quick_probe_seconds, available_quick_concurrency, available_background_concurrency,
			available_min_warm_pool_size
		 FROM app_config WHERE id = ?`,
		appConfigID,
	)
	var stored AppConfig
	if err := row.Scan(
		&stored.UpstreamProxy,
		&stored.TestTarget,
		&stored.TestTimeoutSeconds,
		&stored.AdminToken,
		&stored.AvailableToken,
		&stored.PoolProxyUsername,
		&stored.PoolProxyPassword,
		&stored.AvailableCacheTTLSeconds,
		&stored.TestResultTTLMinutes,
		&stored.AvailableQuickProbeSeconds,
		&stored.AvailableQuickConcurrency,
		&stored.AvailableBackgroundConcurrency,
		&stored.AvailableMinWarmPoolSize,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cfg, nil
		}
		return cfg, err
	}
	return normalizeStoredConfig(stored), nil
}

func (s *Store) SaveConfig(cfg AppConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}
	cfg = normalizeStoredConfig(cfg)
	_, err := s.db.Exec(
		`INSERT INTO app_config (
			id, upstream_proxy, test_target, test_timeout_seconds, admin_token, available_token,
			pool_proxy_username, pool_proxy_password, available_cache_ttl_seconds, test_result_ttl_minutes,
			available_quick_probe_seconds, available_quick_concurrency, available_background_concurrency,
			available_min_warm_pool_size, updated_at
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			upstream_proxy = excluded.upstream_proxy,
			test_target = excluded.test_target,
			test_timeout_seconds = excluded.test_timeout_seconds,
			admin_token = excluded.admin_token,
			available_token = excluded.available_token,
			pool_proxy_username = excluded.pool_proxy_username,
			pool_proxy_password = excluded.pool_proxy_password,
			available_cache_ttl_seconds = excluded.available_cache_ttl_seconds,
			test_result_ttl_minutes = excluded.test_result_ttl_minutes,
			available_quick_probe_seconds = excluded.available_quick_probe_seconds,
			available_quick_concurrency = excluded.available_quick_concurrency,
			available_background_concurrency = excluded.available_background_concurrency,
			available_min_warm_pool_size = excluded.available_min_warm_pool_size,
			updated_at = excluded.updated_at`,
		appConfigID,
		cfg.UpstreamProxy,
		cfg.TestTarget,
		cfg.TestTimeoutSeconds,
		cfg.AdminToken,
		cfg.AvailableToken,
		cfg.PoolProxyUsername,
		cfg.PoolProxyPassword,
		cfg.AvailableCacheTTLSeconds,
		cfg.TestResultTTLMinutes,
		cfg.AvailableQuickProbeSeconds,
		cfg.AvailableQuickConcurrency,
		cfg.AvailableBackgroundConcurrency,
		cfg.AvailableMinWarmPoolSize,
		formatDBTime(time.Now().UTC()),
	)
	return err
}

func (s *Store) LoadTestResults() (map[string]*TestResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	results := make(map[string]*TestResult)
	if err := s.ready(); err != nil {
		return results, err
	}

	rows, err := s.db.Query(`SELECT tag, latency, err, timestamp FROM test_results`)
	if err != nil {
		return results, err
	}
	defer rows.Close()

	for rows.Next() {
		var r TestResult
		var rawTime string
		if err := rows.Scan(&r.Tag, &r.Latency, &r.Err, &rawTime); err != nil {
			return results, err
		}
		ts, err := parseDBTime(rawTime)
		if err != nil {
			return results, err
		}
		r.Timestamp = ts
		results[r.Tag] = &r
	}
	return results, rows.Err()
}

func (s *Store) SaveTestResult(result *TestResult) error {
	if result == nil || result.Tag == "" {
		return nil
	}
	return s.SaveTestResults(map[string]*TestResult{result.Tag: result})
}

func (s *Store) SaveTestResults(results map[string]*TestResult) error {
	if len(results) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(
		`INSERT INTO test_results (tag, latency, err, timestamp)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(tag) DO UPDATE SET
			latency = excluded.latency,
			err = excluded.err,
			timestamp = excluded.timestamp`,
	)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, result := range results {
		if result == nil || result.Tag == "" {
			continue
		}
		if _, err := stmt.Exec(result.Tag, result.Latency, result.Err, formatDBTime(result.Timestamp.UTC())); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) saveSubscription(sub *Subscription) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := saveSubscriptionTx(context.Background(), tx, sub); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func saveSubscriptionTx(ctx context.Context, tx *sql.Tx, sub *Subscription) error {
	if sub == nil {
		return errors.New("subscription is nil")
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO subscriptions (id, url, name, enabled, added_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			url = excluded.url,
			name = excluded.name,
			enabled = excluded.enabled,
			added_at = excluded.added_at,
			updated_at = excluded.updated_at`,
		sub.ID,
		sub.URL,
		sub.Name,
		boolToInt(sub.Enabled),
		formatDBTime(sub.AddedAt.UTC()),
		formatDBTime(sub.UpdatedAt.UTC()),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM subscription_proxies WHERE subscription_id = ?`, sub.ID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO subscription_proxies (subscription_id, position, tag, proxy_json) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, proxy := range sub.Proxies {
		if proxy == nil {
			continue
		}
		proxy.SourceID = sub.ID
		data, err := json.Marshal(proxy)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, sub.ID, i, proxy.Tag, string(data)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) loadSubscriptions(ctx context.Context) ([]*Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, url, name, enabled, added_at, updated_at FROM subscriptions ORDER BY added_at, id`)
	if err != nil {
		return nil, err
	}

	type subRow struct {
		sub        *Subscription
		addedRaw   string
		updatedRaw string
	}
	var rawSubs []subRow
	for rows.Next() {
		var enabled int
		row := subRow{sub: &Subscription{}}
		if err := rows.Scan(&row.sub.ID, &row.sub.URL, &row.sub.Name, &enabled, &row.addedRaw, &row.updatedRaw); err != nil {
			rows.Close()
			return nil, err
		}
		row.sub.Enabled = enabled != 0
		addedAt, err := parseDBTime(row.addedRaw)
		if err != nil {
			rows.Close()
			return nil, err
		}
		updatedAt, err := parseDBTime(row.updatedRaw)
		if err != nil {
			rows.Close()
			return nil, err
		}
		row.sub.AddedAt = addedAt
		row.sub.UpdatedAt = updatedAt
		rawSubs = append(rawSubs, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	subs := make([]*Subscription, 0, len(rawSubs))
	for _, row := range rawSubs {
		proxies, err := s.loadProxies(ctx, row.sub.ID)
		if err != nil {
			return nil, err
		}
		row.sub.Proxies = proxies
		subs = append(subs, row.sub)
	}
	return subs, nil
}

func (s *Store) loadProxies(ctx context.Context, subscriptionID string) ([]*ProxyInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT proxy_json FROM subscription_proxies WHERE subscription_id = ? ORDER BY position`, subscriptionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var proxies []*ProxyInfo
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var proxy ProxyInfo
		if err := json.Unmarshal([]byte(raw), &proxy); err != nil {
			return nil, err
		}
		if proxy.SourceID == "" {
			proxy.SourceID = subscriptionID
		}
		proxies = append(proxies, &proxy)
	}
	return proxies, rows.Err()
}

func normalizeStoredConfig(cfg AppConfig) AppConfig {
	cfg.UpstreamProxy = strings.TrimSpace(cfg.UpstreamProxy)
	cfg.AdminToken = strings.TrimSpace(cfg.AdminToken)
	cfg.AvailableToken = strings.TrimSpace(cfg.AvailableToken)
	cfg.PoolProxyUsername = strings.TrimSpace(cfg.PoolProxyUsername)
	cfg.PoolProxyPassword = strings.TrimSpace(cfg.PoolProxyPassword)
	if cfg.TestTarget == "" || !IsValidTestTarget(cfg.TestTarget) {
		cfg.TestTarget = DefaultTestTarget
	}
	cfg.TestTimeoutSeconds = NormalizeTestTimeoutSeconds(cfg.TestTimeoutSeconds, DefaultTestTimeoutSeconds)
	cfg.AvailableCacheTTLSeconds = NormalizeAvailableCacheTTLSeconds(cfg.AvailableCacheTTLSeconds, DefaultAvailableCacheTTLSeconds)
	cfg.TestResultTTLMinutes = NormalizeTestResultTTLMinutes(cfg.TestResultTTLMinutes, DefaultTestResultTTLMinutes)
	cfg.AvailableQuickProbeSeconds = NormalizeAvailableQuickProbeSeconds(cfg.AvailableQuickProbeSeconds, DefaultAvailableQuickProbeSeconds)
	cfg.AvailableQuickConcurrency = NormalizeAvailableQuickConcurrency(cfg.AvailableQuickConcurrency, DefaultAvailableQuickConcurrency)
	cfg.AvailableBackgroundConcurrency = NormalizeAvailableBackgroundConcurrency(cfg.AvailableBackgroundConcurrency, DefaultAvailableBackgroundConcurrency)
	cfg.AvailableMinWarmPoolSize = NormalizeAvailableMinWarmPoolSize(cfg.AvailableMinWarmPoolSize, DefaultAvailableMinWarmPoolSize)
	return cfg
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func formatDBTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseDBTime(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
