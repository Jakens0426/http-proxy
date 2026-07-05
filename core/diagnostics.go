package core

import (
	"sync"
	"time"
)

const DefaultDiagnosticLogCapacity = 200

type DiagnosticEvent struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	Level    string    `json:"level"`
	Scope    string    `json:"scope"`
	Tag      string    `json:"tag,omitempty"`
	Name     string    `json:"name,omitempty"`
	SourceID string    `json:"source_id,omitempty"`
	Stage    string    `json:"stage,omitempty"`
	Message  string    `json:"message"`
	Detail   string    `json:"detail,omitempty"`
}

type DiagnosticLog struct {
	mu       sync.Mutex
	capacity int
	nextID   int64
	events   []DiagnosticEvent
}

func NewDiagnosticLog(capacity int) *DiagnosticLog {
	if capacity <= 0 {
		capacity = DefaultDiagnosticLogCapacity
	}
	return &DiagnosticLog{
		capacity: capacity,
		events:   make([]DiagnosticEvent, 0, capacity),
	}
}

func (l *DiagnosticLog) Add(event DiagnosticEvent) DiagnosticEvent {
	if l == nil {
		return event
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.nextID++
	event.ID = l.nextID
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if event.Level == "" {
		event.Level = "info"
	}
	if event.Scope == "" {
		event.Scope = "service"
	}
	l.events = append(l.events, event)
	if len(l.events) > l.capacity {
		copy(l.events, l.events[len(l.events)-l.capacity:])
		l.events = l.events[:l.capacity]
	}
	return event
}

func (l *DiagnosticLog) List(limit int) []DiagnosticEvent {
	if l == nil {
		return []DiagnosticEvent{}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if limit <= 0 || limit > len(l.events) {
		limit = len(l.events)
	}
	out := make([]DiagnosticEvent, 0, limit)
	for i := len(l.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.events[i])
	}
	return out
}

func (l *DiagnosticLog) Clear() {
	if l == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = l.events[:0]
}
