// Package historycache stores small, permission-scoped encoded history
// responses. Keys are retained only as SHA-256 digests and every mutation of
// telemetry or visibility advances a process-wide generation.
package historycache

import (
	"crypto/sha256"
	"sync"
	"sync/atomic"
)

const (
	DefaultMaxEntries = 256
	DefaultMaxBytes   = 32 << 20
	DefaultMaxEntry   = 1 << 20
)

type entry struct {
	payload    []byte
	generation uint64
	sequence   uint64
}

type Cache struct {
	mu         sync.Mutex
	entries    map[[sha256.Size]byte]entry
	generation atomic.Uint64
	sequence   uint64
	bytes      int
	maxEntries int
	maxBytes   int
	maxEntry   int
}

func New(maxEntries, maxBytes, maxEntry int) *Cache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxEntry <= 0 || maxEntry > maxBytes {
		maxEntry = min(DefaultMaxEntry, maxBytes)
	}
	cache := &Cache{
		entries: make(map[[sha256.Size]byte]entry), maxEntries: maxEntries, maxBytes: maxBytes, maxEntry: maxEntry,
	}
	cache.generation.Store(1)
	return cache
}

func (cache *Cache) Generation() uint64 { return cache.generation.Load() }

func (cache *Cache) Get(key string, generation uint64) ([]byte, bool) {
	if generation == 0 || generation != cache.generation.Load() {
		return nil, false
	}
	digest := sha256.Sum256([]byte(key))
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.entries[digest]
	if !ok || value.generation != generation || generation != cache.generation.Load() {
		return nil, false
	}
	return value.payload, true
}

// PutIfGeneration copies payload into the cache only if no invalidation
// occurred while the caller queried and encoded the response.
func (cache *Cache) PutIfGeneration(key string, payload []byte, generation uint64) bool {
	if generation == 0 || len(payload) == 0 || len(payload) > cache.maxEntry || generation != cache.generation.Load() {
		return false
	}
	digest := sha256.Sum256([]byte(key))
	owned := append([]byte(nil), payload...)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if generation != cache.generation.Load() {
		return false
	}
	if previous, ok := cache.entries[digest]; ok {
		cache.bytes -= len(previous.payload)
	}
	cache.sequence++
	cache.entries[digest] = entry{payload: owned, generation: generation, sequence: cache.sequence}
	cache.bytes += len(owned)
	for len(cache.entries) > cache.maxEntries || cache.bytes > cache.maxBytes {
		cache.evictOldest()
	}
	return true
}

func (cache *Cache) Invalidate() uint64 {
	generation := cache.generation.Add(1)
	cache.mu.Lock()
	cache.entries = make(map[[sha256.Size]byte]entry)
	cache.bytes = 0
	cache.mu.Unlock()
	return generation
}

func (cache *Cache) evictOldest() {
	var oldestKey [sha256.Size]byte
	var oldest entry
	found := false
	for key, value := range cache.entries {
		if !found || value.sequence < oldest.sequence {
			oldestKey, oldest, found = key, value, true
		}
	}
	if found {
		delete(cache.entries, oldestKey)
		cache.bytes -= len(oldest.payload)
	}
}

func (cache *Cache) Len() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return len(cache.entries)
}

func (cache *Cache) Bytes() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.bytes
}

var responses = New(DefaultMaxEntries, DefaultMaxBytes, DefaultMaxEntry)

func Generation() uint64 { return responses.Generation() }
func Get(key string, generation uint64) ([]byte, bool) {
	return responses.Get(key, generation)
}
func PutIfGeneration(key string, payload []byte, generation uint64) bool {
	return responses.PutIfGeneration(key, payload, generation)
}
func Invalidate() uint64 { return responses.Invalidate() }
