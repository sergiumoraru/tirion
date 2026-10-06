package trace

import (
	"container/list"
	"sync"
	"time"
)

type lruCache struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	items map[string]*list.Element
	order *list.List
}

type lruEntry struct {
	key     string
	value   interface{}
	expires time.Time
}

func newLRU(max int, ttl time.Duration) *lruCache {
	if max <= 0 {
		max = 1
	}
	return &lruCache{
		max:   max,
		ttl:   ttl,
		items: make(map[string]*list.Element),
		order: list.New(),
	}
}

func (c *lruCache) get(key string) (interface{}, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*lruEntry)
		if !entry.expires.IsZero() && time.Now().After(entry.expires) {
			c.order.Remove(elem)
			delete(c.items, key)
			return nil, false
		}
		c.order.MoveToFront(elem)
		return entry.value, true
	}
	return nil, false
}

func (c *lruCache) set(key string, value interface{}) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*lruEntry)
		entry.value = value
		if c.ttl > 0 {
			entry.expires = time.Now().Add(c.ttl)
		}
		c.order.MoveToFront(elem)
		return
	}
	entry := &lruEntry{key: key, value: value}
	if c.ttl > 0 {
		entry.expires = time.Now().Add(c.ttl)
	}
	elem := c.order.PushFront(entry)
	c.items[key] = elem
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*lruEntry).key)
	}
}
