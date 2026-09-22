package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var errCollectorNotSupported = errors.New("collector not supported")

// Collector keeps hardware-specific data sources isolated. A failed or slow
// collector never blocks the legacy two-second metrics response.
type Collector interface {
	Name() string
	Collect(ctx context.Context) (any, error)
}

type CollectorResult struct {
	Name        string    `json:"name"`
	Supported   bool      `json:"supported"`
	Available   bool      `json:"available"`
	Message     string    `json:"message,omitempty"`
	CollectedAt time.Time `json:"collected_at,omitempty"`
	Data        any       `json:"data,omitempty"`
}

type collectorRegistration struct {
	collector Collector
	timeout   time.Duration
	interval  time.Duration
}

type collectorSlot struct {
	registration collectorRegistration
	mu           sync.RWMutex
	result       CollectorResult
	running      bool
	lastAttempt  time.Time
}

type collectorManager struct {
	slots map[string]*collectorSlot
}

func newCollectorManager(registrations []collectorRegistration) *collectorManager {
	manager := &collectorManager{slots: make(map[string]*collectorSlot, len(registrations))}
	for _, registration := range registrations {
		name := registration.collector.Name()
		manager.slots[name] = &collectorSlot{
			registration: registration,
			result: CollectorResult{
				Name:      name,
				Supported: true,
				Available: false,
				Message:   "采集中",
			},
		}
	}
	return manager
}

func (m *collectorManager) Start() {
	_ = m.Snapshot()
}

func (m *collectorManager) Snapshot() map[string]CollectorResult {
	now := time.Now()
	results := make(map[string]CollectorResult, len(m.slots))
	for name, slot := range m.slots {
		slot.mu.Lock()
		if !slot.running && (slot.lastAttempt.IsZero() || now.Sub(slot.lastAttempt) >= slot.registration.interval) {
			slot.running = true
			slot.lastAttempt = now
			go slot.collect()
		}
		results[name] = slot.result
		slot.mu.Unlock()
	}
	return results
}

func (slot *collectorSlot) collect() {
	result := CollectorResult{Name: slot.registration.collector.Name(), Supported: true, CollectedAt: time.Now()}
	ctx, cancel := context.WithTimeout(context.Background(), slot.registration.timeout)
	defer cancel()

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				result.Message = fmt.Sprintf("采集器异常已隔离: %v", recovered)
			}
		}()
		data, err := slot.registration.collector.Collect(ctx)
		switch {
		case errors.Is(err, errCollectorNotSupported):
			result.Supported = false
			result.Message = err.Error()
		case err != nil:
			result.Message = err.Error()
		case ctx.Err() != nil:
			result.Message = "采集超时"
		default:
			result.Available = true
			result.Data = data
		}
	}()

	if ctx.Err() != nil && !result.Available && result.Message == "" {
		result.Message = "采集超时"
	}
	slot.mu.Lock()
	slot.result = result
	slot.running = false
	slot.mu.Unlock()
}
