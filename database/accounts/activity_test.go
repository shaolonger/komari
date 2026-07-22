package accounts

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeActivityStore struct {
	mu       sync.Mutex
	calls    [][]ActivityUpdate
	failures int
	failErr  error
	block    bool
	notify   chan struct{}
}

func (s *fakeActivityStore) WriteActivity(ctx context.Context, updates []ActivityUpdate) error {
	if s.block {
		<-ctx.Done()
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures > 0 {
		s.failures--
		return s.failErr
	}
	owned := append([]ActivityUpdate(nil), updates...)
	s.calls = append(s.calls, owned)
	if s.notify != nil {
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *fakeActivityStore) snapshot() [][]ActivityUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([][]ActivityUpdate, len(s.calls))
	for i := range s.calls {
		result[i] = append([]ActivityUpdate(nil), s.calls[i]...)
	}
	return result
}

func manualActivityTracker(t testing.TB, store ActivityStore, capacity int) *ActivityTracker {
	t.Helper()
	if capacity == 0 {
		capacity = 128
	}
	tracker, err := NewActivityTracker(store, ActivityConfig{
		Capacity: capacity, BatchSize: min(capacity, 128), OnlineInterval: time.Minute,
		RetryInterval: time.Hour, ChangeDebounce: time.Hour, IdleTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tracker.Close(ctx)
	})
	return tracker
}

func TestActivityTrackerCoalescesOnlineAndWritesChanges(t *testing.T) {
	store := &fakeActivityStore{}
	tracker := manualActivityTracker(t, store, 128)
	base := time.Now().Truncate(time.Second)
	for i := 0; i < 1_000; i++ {
		if err := tracker.Touch("session-a", "agent-a", "192.0.2.1", base.Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tracker.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := store.snapshot()
	if len(calls) != 1 || len(calls[0]) != 1 {
		t.Fatalf("writes = %#v, want one coalesced row", calls)
	}
	first := calls[0][0]
	if !first.WriteOnline || !first.WriteUA || !first.WriteIP || !first.LastSeen.Equal(base.Add(999*time.Millisecond)) {
		t.Fatalf("first update = %+v", first)
	}

	if err := tracker.Touch("session-a", "agent-a", "192.0.2.1", base.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(store.snapshot()); got != 1 {
		t.Fatalf("same metadata within throttle wrote %d batches, want 1", got)
	}

	if err := tracker.Touch("session-a", "agent-b", "192.0.2.1", base.Add(31*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls = store.snapshot()
	if len(calls) != 2 || !calls[1][0].WriteUA || calls[1][0].WriteOnline || calls[1][0].WriteIP {
		t.Fatalf("metadata update = %+v, want UA-only write", calls[1][0])
	}

	if err := tracker.Touch("session-a", "agent-b", "192.0.2.1", base.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls = store.snapshot()
	if len(calls) != 3 || !calls[2][0].WriteOnline || calls[2][0].WriteUA || calls[2][0].WriteIP {
		t.Fatalf("throttled online update = %+v", calls[2][0])
	}
}

func TestActivityTrackerSignalsMetadataChangeImmediately(t *testing.T) {
	store := &fakeActivityStore{notify: make(chan struct{}, 2)}
	tracker, err := NewActivityTracker(store, ActivityConfig{
		Capacity: 32, BatchSize: 32, OnlineInterval: time.Minute,
		RetryInterval: time.Hour, ChangeDebounce: time.Millisecond, IdleTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tracker.Close(ctx)
	}()
	base := time.Now()
	if err := tracker.Touch("session", "ua-a", "192.0.2.1", base); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("initial state was not flushed promptly")
	}
	if err := tracker.Touch("session", "ua-a", "192.0.2.1", base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.notify:
		t.Fatal("unchanged state bypassed online throttle")
	case <-time.After(20 * time.Millisecond):
	}
	if err := tracker.Touch("session", "ua-b", "192.0.2.1", base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.notify:
	case <-time.After(time.Second):
		t.Fatal("UA change was not flushed promptly")
	}
}

func TestActivityTrackerRetriesFailureWithoutLosingState(t *testing.T) {
	injected := errors.New("injected database failure")
	store := &fakeActivityStore{failures: 1, failErr: injected}
	tracker := manualActivityTracker(t, store, 32)
	if err := tracker.Touch("session", "ua", "192.0.2.1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Flush(context.Background()); !errors.Is(err, injected) {
		t.Fatalf("Flush() error = %v, want injected failure", err)
	}
	if tracker.Pending() != 1 {
		t.Fatalf("Pending() = %d, want failed update retained", tracker.Pending())
	}
	if err := tracker.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tracker.Pending() != 0 || len(store.snapshot()) != 1 {
		t.Fatalf("retry state: pending=%d calls=%d", tracker.Pending(), len(store.snapshot()))
	}
}

func TestActivityTrackerCloseDrainsAndHonorsDeadline(t *testing.T) {
	t.Run("drain", func(t *testing.T) {
		store := &fakeActivityStore{}
		tracker := manualActivityTracker(t, store, 512)
		for i := 0; i < 300; i++ {
			if err := tracker.Touch(fmt.Sprintf("session-%d", i), "ua", "192.0.2.1", time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := tracker.Close(ctx); err != nil {
			t.Fatal(err)
		}
		calls := store.snapshot()
		rows := 0
		for _, call := range calls {
			rows += len(call)
		}
		if rows != 300 || len(calls) != 3 || tracker.Pending() != 0 {
			t.Fatalf("close did not drain: calls=%d rows=%d pending=%d", len(calls), rows, tracker.Pending())
		}
		if err := tracker.Touch("session", "ua", "ip", time.Now()); !errors.Is(err, ErrActivityClosed) {
			t.Fatalf("Touch() after close error = %v", err)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		store := &fakeActivityStore{block: true}
		tracker := manualActivityTracker(t, store, 32)
		if err := tracker.Touch("session", "ua", "192.0.2.1", time.Now()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := tracker.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close() error = %v, want deadline", err)
		}
	})
}

func TestActivityTrackerCapacityBoundsAndTruncatesInput(t *testing.T) {
	store := &fakeActivityStore{}
	tracker := manualActivityTracker(t, store, 2)
	now := time.Now()
	if err := tracker.Touch("", "ua", "ip", now); !errors.Is(err, ErrActivitySession) {
		t.Fatalf("empty session error = %v", err)
	}
	longUA := string(make([]byte, MaxActivityUserAgentBytes+100))
	longIP := string(make([]byte, MaxActivityIPBytes+10))
	if err := tracker.Touch("one", longUA, longIP, now); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Touch("two", "ua", "ip", now); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Touch("three", "ua", "ip", now); !errors.Is(err, ErrActivityCapacity) {
		t.Fatalf("third dirty credential error = %v, want capacity", err)
	}
	if tracker.Len() != 2 {
		t.Fatalf("Len() = %d, want hard bound 2", tracker.Len())
	}
	if err := tracker.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := store.snapshot()
	for _, update := range calls[0] {
		if len(update.UserAgent) > MaxActivityUserAgentBytes || len(update.IP) > MaxActivityIPBytes {
			t.Fatalf("unbounded update: ua=%d ip=%d", len(update.UserAgent), len(update.IP))
		}
	}
	if err := tracker.Touch("three", "ua", "ip", now.Add(time.Second)); err != nil {
		t.Fatalf("clean state was not evictable: %v", err)
	}
}

func TestActivityTrackerStateCannotRetainPlaintextSession(t *testing.T) {
	trackerType := reflect.TypeOf(ActivityTracker{})
	states, ok := trackerType.FieldByName("states")
	if !ok || states.Type.Key().Kind() != reflect.Array || states.Type.Key().Len() != 32 {
		t.Fatalf("activity state key = %v, want fixed 32-byte digest", states.Type.Key())
	}
	stateType := reflect.TypeOf(sessionActivityState{})
	for i := 0; i < stateType.NumField(); i++ {
		field := stateType.Field(i)
		if field.Name == "session" || field.Name == "credential" {
			t.Fatalf("activity state unexpectedly retains plaintext field %q", field.Name)
		}
	}
}

func TestActivityTrackerConcurrentTouches(t *testing.T) {
	store := &fakeActivityStore{}
	tracker := manualActivityTracker(t, store, 1_024)
	base := time.Now()
	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 1_000; i++ {
				session := fmt.Sprintf("session-%d", i%128)
				if err := tracker.Touch(session, fmt.Sprintf("ua-%d", worker%4), fmt.Sprintf("192.0.2.%d", worker), base.Add(time.Duration(i)*time.Millisecond)); err != nil {
					t.Errorf("Touch(): %v", err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	if err := tracker.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tracker.Len() > 1_024 || tracker.Pending() != 0 {
		t.Fatalf("concurrent state: len=%d pending=%d", tracker.Len(), tracker.Pending())
	}
}

func BenchmarkActivityTrackerTouch(b *testing.B) {
	store := &fakeActivityStore{}
	tracker := manualActivityTracker(b, store, 128)
	base := time.Now()
	if err := tracker.Touch("benchmark-session", "benchmark-agent", "192.0.2.1", base); err != nil {
		b.Fatal(err)
	}
	if err := tracker.Flush(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := tracker.Touch("benchmark-session", "benchmark-agent", "192.0.2.1", base.Add(time.Second)); err != nil {
			b.Fatal(err)
		}
	}
}
