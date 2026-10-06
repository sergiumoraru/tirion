package handlers

import (
	"container/list"
	"os"
	"strconv"
	"sync"
	"time"
)

type traceExpandCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	ll         *list.List
	items      map[string]*list.Element
}

type traceExpandCacheEntry struct {
	key       string
	value     TraceExpandResponse
	expiresAt time.Time
}

func newTraceExpandCache(maxEntries int, ttl time.Duration) *traceExpandCache {
	if maxEntries <= 0 || ttl <= 0 {
		return nil
	}
	return &traceExpandCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		ll:         list.New(),
		items:      make(map[string]*list.Element),
	}
}

func newTraceExpandCacheFromEnv() *traceExpandCache {
	size := 200
	ttl := 5 * time.Minute

	if val := os.Getenv("CODEBASE_TRACE_EXPAND_CACHE_SIZE"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			size = parsed
		}
	}
	if val := os.Getenv("CODEBASE_TRACE_EXPAND_CACHE_TTL"); val != "" {
		if parsed, err := time.ParseDuration(val); err == nil {
			ttl = parsed
		}
	}
	return newTraceExpandCache(size, ttl)
}

func (c *traceExpandCache) get(key string) (TraceExpandResponse, bool) {
	if c == nil || key == "" {
		return TraceExpandResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*traceExpandCacheEntry)
		if time.Now().After(entry.expiresAt) {
			c.ll.Remove(elem)
			delete(c.items, key)
			return TraceExpandResponse{}, false
		}
		c.ll.MoveToFront(elem)
		return entry.value, true
	}
	return TraceExpandResponse{}, false
}

func (c *traceExpandCache) set(key string, value TraceExpandResponse) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*traceExpandCacheEntry)
		entry.value = value
		entry.expiresAt = time.Now().Add(c.ttl)
		c.ll.MoveToFront(elem)
		return
	}

	entry := &traceExpandCacheEntry{
		key:       key,
		value:     value,
		expiresAt: time.Now().Add(c.ttl),
	}
	elem := c.ll.PushFront(entry)
	c.items[key] = elem

	for c.ll.Len() > c.maxEntries {
		if back := c.ll.Back(); back != nil {
			removed := back.Value.(*traceExpandCacheEntry)
			delete(c.items, removed.key)
			c.ll.Remove(back)
		}
	}
}
