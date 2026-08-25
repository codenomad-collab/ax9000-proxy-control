package main

import (
	"sync"
	"time"
)

type AuditEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

type AuditLog struct {
	mu      sync.RWMutex
	entries []AuditEntry
	limit   int
}

func newAuditLog(limit int) *AuditLog {
	if limit < 100 {
		limit = 100
	}
	return &AuditLog{limit: limit}
}

func (a *AuditLog) add(level, source, message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, AuditEntry{
		Time:    time.Now(),
		Level:   level,
		Source:  source,
		Message: redactSensitive(message),
	})
	if len(a.entries) > a.limit {
		a.entries = append([]AuditEntry(nil), a.entries[len(a.entries)-a.limit:]...)
	}
}

func (a *AuditLog) snapshot(limit int) []AuditEntry {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if limit <= 0 || limit > len(a.entries) {
		limit = len(a.entries)
	}
	start := len(a.entries) - limit
	result := make([]AuditEntry, limit)
	copy(result, a.entries[start:])
	return result
}
