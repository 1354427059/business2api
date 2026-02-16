package logger

import (
	"strings"
	"sync"
	"time"
)

type LogEntry struct {
	ID      int64  `json:"id"`
	Source  string `json:"source"`
	TS      string `json:"ts"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type RingStore struct {
	mu       sync.RWMutex
	capacity int
	nextID   int64
	entries  []LogEntry
}

var store = NewRingStore(5000)

func NewRingStore(capacity int) *RingStore {
	if capacity <= 0 {
		capacity = 5000
	}
	return &RingStore{capacity: capacity, entries: make([]LogEntry, 0, capacity)}
}

func AppendRaw(source, line string) LogEntry {
	return store.AppendRaw(source, line)
}

func Recent(limit int, source, level string) []LogEntry {
	return store.Recent(limit, source, level)
}

func After(afterID int64, limit int, source, level string) ([]LogEntry, int64) {
	return store.After(afterID, limit, source, level)
}

func (s *RingStore) AppendRaw(source, line string) LogEntry {
	cleanLine := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
	if cleanLine == "" {
		return LogEntry{}
	}
	if strings.TrimSpace(source) == "" {
		source = "business2api"
	}
	entry := LogEntry{
		Source:  source,
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
		Level:   detectLevel(cleanLine),
		Message: cleanLine,
	}

	s.mu.Lock()
	s.nextID++
	entry.ID = s.nextID
	if len(s.entries) >= s.capacity {
		s.entries = append(s.entries[1:], entry)
	} else {
		s.entries = append(s.entries, entry)
	}
	s.mu.Unlock()

	return entry
}

func (s *RingStore) Recent(limit int, source, level string) []LogEntry {
	source = normalizeSource(source)
	level = normalizeLevel(level)
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]LogEntry, 0, limit)
	for i := len(s.entries) - 1; i >= 0; i-- {
		entry := s.entries[i]
		if !matchSource(source, entry.Source) || !matchLevel(level, entry.Level) {
			continue
		}
		items = append(items, entry)
		if len(items) >= limit {
			break
		}
	}

	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items
}

func (s *RingStore) After(afterID int64, limit int, source, level string) ([]LogEntry, int64) {
	source = normalizeSource(source)
	level = normalizeLevel(level)
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]LogEntry, 0, limit)
	nextAfterID := afterID
	for _, entry := range s.entries {
		if entry.ID <= afterID {
			continue
		}
		if !matchSource(source, entry.Source) || !matchLevel(level, entry.Level) {
			continue
		}
		items = append(items, entry)
		if entry.ID > nextAfterID {
			nextAfterID = entry.ID
		}
		if len(items) >= limit {
			break
		}
	}
	return items, nextAfterID
}

func detectLevel(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, "[ERROR]") || strings.Contains(upper, "❌"):
		return "error"
	case strings.Contains(upper, "[WARN]") || strings.Contains(upper, "⚠️"):
		return "warn"
	case strings.Contains(upper, "[DEBUG]"):
		return "debug"
	default:
		return "info"
	}
}

func normalizeSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	switch source {
	case "business2api", "registrar", "all":
		return source
	default:
		return "all"
	}
}

func normalizeLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "error", "warn", "info", "debug", "all":
		return level
	default:
		return "all"
	}
}

func matchSource(filter, source string) bool {
	if filter == "all" {
		return true
	}
	return normalizeSource(source) == filter
}

func matchLevel(filter, level string) bool {
	if filter == "all" {
		return true
	}
	return normalizeLevel(level) == filter
}
