package credentialcache

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCacheHitExpiryAndInvalidation(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	cache := New[string](128)
	cache.Put("raw-secret", Entry[string]{
		Value:          "identity",
		Found:          true,
		Version:        7,
		CacheExpiresAt: now.Add(time.Minute),
	}, now)

	entry, ok := cache.Get("raw-secret", now.Add(time.Second))
	if !ok || entry.Value != "identity" || entry.Version != 7 {
		t.Fatalf("Get() = (%+v, %v), want cached identity", entry, ok)
	}
	if _, ok := cache.Get("raw-secret", now.Add(time.Minute)); ok {
		t.Fatal("expired entry remained cached")
	}

	cache.Put("raw-secret", Entry[string]{Found: false, CacheExpiresAt: now.Add(time.Minute)}, now)
	cache.Invalidate("raw-secret")
	if _, ok := cache.Get("raw-secret", now); ok {
		t.Fatal("invalidated entry remained cached")
	}
}

func TestCacheCapacityIsHardBound(t *testing.T) {
	now := time.Now()
	cache := New[int](129)
	for i := 0; i < 100_000; i++ {
		cache.Put(fmt.Sprintf("credential-%d", i), Entry[int]{Found: true, CacheExpiresAt: now.Add(time.Hour)}, now)
	}
	// Capacity rounds up per shard but can never grow with attacker input.
	if cache.Len() > 129 {
		t.Fatalf("Len() = %d, want <= 129", cache.Len())
	}
}

func TestCacheDoesNotStructurallyRetainPlaintext(t *testing.T) {
	cacheType := reflect.TypeOf(Cache[string]{})
	for i := 0; i < cacheType.NumField(); i++ {
		if cacheType.Field(i).Type.Kind() == reflect.String {
			t.Fatalf("Cache unexpectedly has string field %q", cacheType.Field(i).Name)
		}
	}
	shardType := reflect.TypeOf(shard[string]{})
	entries, ok := shardType.FieldByName("entries")
	if !ok || entries.Type.Key().Kind() != reflect.Array || entries.Type.Key().Len() != 32 {
		t.Fatalf("cache key type = %v, want fixed 32-byte digest", entries.Type.Key())
	}
}

func TestCacheConcurrentMutation(t *testing.T) {
	now := time.Now()
	cache := New[int](1024)
	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 2_000; i++ {
				secret := fmt.Sprintf("%d-%d", worker, i%100)
				cache.Put(secret, Entry[int]{Value: i, Found: true, CacheExpiresAt: now.Add(time.Minute)}, now)
				cache.Get(secret, now)
				if i%3 == 0 {
					cache.Invalidate(secret)
				}
			}
		}(worker)
	}
	wg.Wait()
	if cache.Len() > 1024 {
		t.Fatalf("Len() = %d, want bounded", cache.Len())
	}
}

func TestStaleGenerationCannotRepopulateInvalidatedCredential(t *testing.T) {
	now := time.Now()
	cache := New[int](128)
	generation := cache.Generation()
	cache.Invalidate("rotated-secret")
	if cache.PutIfGeneration("rotated-secret", Entry[int]{
		Value: 1, Found: true, CacheExpiresAt: now.Add(time.Minute),
	}, now, generation) {
		t.Fatal("stale lookup repopulated cache after invalidation")
	}
	if _, ok := cache.Get("rotated-secret", now); ok {
		t.Fatal("stale credential remained cached")
	}
}

func BenchmarkCacheGet(b *testing.B) {
	cache := New[int](16_384)
	now := time.Now()
	cache.Put("benchmark-secret", Entry[int]{Value: 42, Found: true, CacheExpiresAt: now.Add(time.Hour)}, now)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Get("benchmark-secret", now)
		}
	})
}
