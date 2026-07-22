package historycache

import (
	"bytes"
	"reflect"
	"sync"
	"testing"
)

func TestCacheScopesKeysAndRejectsStaleGeneration(t *testing.T) {
	cache := New(4, 1_024, 512)
	generation := cache.Generation()
	if !cache.PutIfGeneration("permission=public|uuid=a", []byte("public"), generation) {
		t.Fatal("failed to cache public response")
	}
	if !cache.PutIfGeneration("permission=admin|uuid=a", []byte("admin"), generation) {
		t.Fatal("failed to cache admin response")
	}
	if got, ok := cache.Get("permission=public|uuid=a", generation); !ok || string(got) != "public" {
		t.Fatalf("public cache=(%q,%v)", got, ok)
	}
	if got, ok := cache.Get("permission=admin|uuid=a", generation); !ok || string(got) != "admin" {
		t.Fatalf("admin cache=(%q,%v)", got, ok)
	}
	cache.Invalidate()
	if _, ok := cache.Get("permission=public|uuid=a", generation); ok {
		t.Fatal("stale public response survived invalidation")
	}
	if cache.PutIfGeneration("permission=public|uuid=a", []byte("stale"), generation) {
		t.Fatal("in-flight stale response repopulated cache")
	}
}

func TestCacheIsStrictlyMemoryBoundedAndOwnsPayload(t *testing.T) {
	cache := New(3, 30, 12)
	generation := cache.Generation()
	payload := []byte("0123456789")
	if !cache.PutIfGeneration("first", payload, generation) {
		t.Fatal("put first")
	}
	payload[0] = 'X'
	got, ok := cache.Get("first", generation)
	if !ok || !bytes.Equal(got, []byte("0123456789")) {
		t.Fatalf("cache did not own payload: %q", got)
	}
	for index := 0; index < 20; index++ {
		cache.PutIfGeneration(string(rune('a'+index)), []byte("0123456789"), generation)
	}
	if cache.Len() > 3 || cache.Bytes() > 30 {
		t.Fatalf("cache len=%d bytes=%d exceeds bound", cache.Len(), cache.Bytes())
	}
	if cache.PutIfGeneration("oversized", make([]byte, 13), generation) {
		t.Fatal("oversized response entered cache")
	}
	cacheType := reflect.TypeOf(Cache{})
	for index := 0; index < cacheType.NumField(); index++ {
		if cacheType.Field(index).Type.Kind() == reflect.String {
			t.Fatalf("cache retains plaintext key field %q", cacheType.Field(index).Name)
		}
	}
}

func TestConcurrentGetPutInvalidate(t *testing.T) {
	cache := New(64, 64<<10, 1<<10)
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			for iteration := 0; iteration < 1_000; iteration++ {
				generation := cache.Generation()
				cache.PutIfGeneration("shared", []byte{byte(id)}, generation)
				cache.Get("shared", generation)
				if iteration%97 == 0 {
					cache.Invalidate()
				}
			}
		}(worker)
	}
	workers.Wait()
}

func BenchmarkCacheHit512KiB(b *testing.B) {
	cache := New(16, 8<<20, 1<<20)
	generation := cache.Generation()
	if !cache.PutIfGeneration("permission=public|history", make([]byte, 512<<10), generation) {
		b.Fatal("cache setup failed")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if payload, ok := cache.Get("permission=public|history", generation); !ok || len(payload) != 512<<10 {
			b.Fatal("cache miss")
		}
	}
}
