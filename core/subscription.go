package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"
)

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func NewSubscription(url string) *Subscription {
	id := generateID()
	now := time.Now().UTC()
	return &Subscription{
		ID:        id,
		URL:       url,
		Name:      url,
		Enabled:   true,
		AddedAt:   now,
		UpdatedAt: now,
	}
}

type SubscriptionManager struct {
	store *Store
}

func NewSubscriptionManager(store *Store) *SubscriptionManager {
	return &SubscriptionManager{store: store}
}

func (m *SubscriptionManager) Store() *Store {
	return m.store
}

func (m *SubscriptionManager) Add(url string) (*Subscription, error) {
	sub := NewSubscription(url)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}

	proxies := ParseSubscription(string(body))
	if len(proxies) == 0 {
		return nil, fmt.Errorf("no valid proxies found in subscription")
	}

	for _, p := range proxies {
		p.SourceID = sub.ID
	}
	sub.Proxies = proxies
	sub.UpdatedAt = time.Now().UTC()

	if len(proxies) > 0 && proxies[0].Name != "" {
		sub.Name = proxies[0].Name
	}

	if err := m.store.AddSubscription(sub); err != nil {
		return nil, fmt.Errorf("save failed: %w", err)
	}

	return sub, nil
}

func (m *SubscriptionManager) Remove(id string) error {
	return m.store.RemoveSubscription(id)
}

func (m *SubscriptionManager) Refresh(id string) (*Subscription, error) {
	subs := m.store.GetSubscriptions()
	var sub *Subscription
	for _, s := range subs {
		if s.ID == id {
			sub = s
			break
		}
	}
	if sub == nil {
		return nil, fmt.Errorf("subscription not found")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(sub.URL)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}

	proxies := ParseSubscription(string(body))
	if len(proxies) == 0 {
		return nil, fmt.Errorf("no valid proxies found after refresh")
	}

	for _, p := range proxies {
		p.SourceID = sub.ID
	}
	sub.Proxies = proxies
	sub.UpdatedAt = time.Now().UTC()

	if err := m.store.UpdateSubscription(sub); err != nil {
		return nil, fmt.Errorf("save failed: %w", err)
	}

	return sub, nil
}

func (m *SubscriptionManager) List() []*Subscription {
	return m.store.GetSubscriptions()
}

func (m *SubscriptionManager) GetAllProxies() []*ProxyInfo {
	return m.store.GetAllProxies()
}
