// Package credentialcache provides a bounded, sharded cache whose map keys are
// fixed-size credential digests. Raw credentials are never retained by Cache.
package credentialcache

import (
	"crypto/sha256"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const shardCount = 64

// Entry is a cached credential lookup result. Found=false represents a bounded,
// short-lived negative lookup. Version and CredentialExpiresAt let callers
// preserve the authoritative credential lifecycle without retaining the secret.
type Entry[V any] struct {
	Value               V
	Found               bool
	Version             uint64
	CredentialExpiresAt time.Time
	CacheExpiresAt      time.Time
}

type shard[V any] struct {
	mu      sync.RWMutex
	entries map[[sha256.Size]byte]Entry[V]
	limit   int
}

// Cache is safe for concurrent use. Capacity is a hard process-local bound;
// each shard owns an independent portion so no global lock exists on reads.
type Cache[V any] struct {
	shards       [shardCount]shard[V]
	entryCount   atomic.Int64
	invalidation atomic.Uint64
}

func New[V any](capacity int) *Cache[V] {
	if capacity < shardCount {
		capacity = shardCount
	}
	c := &Cache[V]{}
	base, remainder := capacity/shardCount, capacity%shardCount
	for i := range c.shards {
		limit := base
		if i < remainder {
			limit++
		}
		c.shards[i].limit = limit
		c.shards[i].entries = make(map[[sha256.Size]byte]Entry[V], limit)
	}
	return c
}

func Digest(secret string) [sha256.Size]byte {
	// The temporary read-only view avoids allocating a plaintext []byte copy.
	// It is consumed synchronously by Sum256 and is never retained or mutated.
	return sha256.Sum256(unsafe.Slice(unsafe.StringData(secret), len(secret)))
}

func (c *Cache[V]) shardFor(key [sha256.Size]byte) *shard[V] {
	return &c.shards[int(key[0])&(shardCount-1)]
}

// Get returns cached=true only while the cache TTL is valid. Credential expiry
// is deliberately returned to the caller because expired and revoked
// credentials must be rejected rather than treated as database misses.
func (c *Cache[V]) Get(secret string, now time.Time) (entry Entry[V], cached bool) {
	key := Digest(secret)
	s := c.shardFor(key)
	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()
	if !ok {
		return Entry[V]{}, false
	}
	if !entry.CacheExpiresAt.IsZero() && !now.Before(entry.CacheExpiresAt) {
		s.mu.Lock()
		if current, exists := s.entries[key]; exists && current.CacheExpiresAt.Equal(entry.CacheExpiresAt) {
			delete(s.entries, key)
			c.entryCount.Add(-1)
		}
		s.mu.Unlock()
		return Entry[V]{}, false
	}
	return entry, true
}

func (c *Cache[V]) Put(secret string, entry Entry[V], now time.Time) {
	c.put(secret, entry, now, 0, false)
}

// PutIfGeneration prevents an in-flight database lookup from repopulating a
// credential after any concurrent revocation/invalidation has started.
func (c *Cache[V]) PutIfGeneration(secret string, entry Entry[V], now time.Time, generation uint64) bool {
	return c.put(secret, entry, now, generation, true)
}

func (c *Cache[V]) put(secret string, entry Entry[V], now time.Time, generation uint64, conditional bool) bool {
	if secret == "" || (!entry.CacheExpiresAt.IsZero() && !now.Before(entry.CacheExpiresAt)) {
		return false
	}
	key := Digest(secret)
	s := c.shardFor(key)
	s.mu.Lock()
	if conditional && c.invalidation.Load() != generation {
		s.mu.Unlock()
		return false
	}
	if _, exists := s.entries[key]; !exists {
		if len(s.entries) >= s.limit {
			c.evictOneLocked(s, now)
		}
		c.entryCount.Add(1)
	}
	s.entries[key] = entry
	s.mu.Unlock()
	return true
}

func (c *Cache[V]) evictOneLocked(s *shard[V], now time.Time) {
	for key, entry := range s.entries {
		if !entry.CacheExpiresAt.IsZero() && !now.Before(entry.CacheExpiresAt) {
			delete(s.entries, key)
			c.entryCount.Add(-1)
			return
		}
	}
	// Random map iteration gives inexpensive approximate eviction while keeping
	// the cache strictly bounded under credential spray attacks.
	for key := range s.entries {
		delete(s.entries, key)
		c.entryCount.Add(-1)
		return
	}
}

func (c *Cache[V]) Invalidate(secret string) {
	if secret == "" {
		return
	}
	key := Digest(secret)
	c.invalidation.Add(1)
	s := c.shardFor(key)
	s.mu.Lock()
	if _, exists := s.entries[key]; exists {
		delete(s.entries, key)
		c.entryCount.Add(-1)
	}
	s.mu.Unlock()
}

func (c *Cache[V]) Clear() {
	c.invalidation.Add(1)
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		removed := len(s.entries)
		clear(s.entries)
		s.mu.Unlock()
		c.entryCount.Add(-int64(removed))
	}
}

func (c *Cache[V]) Len() int { return int(c.entryCount.Load()) }

func (c *Cache[V]) Invalidations() uint64 { return c.invalidation.Load() }

func (c *Cache[V]) Generation() uint64 { return c.invalidation.Load() }
