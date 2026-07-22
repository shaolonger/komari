package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/internal/credentialcache"
	"github.com/komari-monitor/komari/internal/observability"
)

const (
	DefaultActivityCapacity       = 16_384
	DefaultActivityBatchSize      = 256
	DefaultActivityOnlineInterval = 60 * time.Second
	DefaultActivityRetryInterval  = time.Second
	DefaultActivityChangeDebounce = 5 * time.Millisecond
	DefaultActivityIdleTTL        = 10 * time.Minute
	MaxActivityUserAgentBytes     = 1_024
	MaxActivityIPBytes            = 100
)

var (
	ErrActivityClosed   = errors.New("session activity tracker is closed")
	ErrActivityCapacity = errors.New("session activity tracker capacity reached")
	ErrActivitySession  = errors.New("session activity requires a credential")
)

type ActivityUpdate struct {
	Digest      [32]byte
	LastSeen    time.Time
	UserAgent   string
	IP          string
	WriteOnline bool
	WriteUA     bool
	WriteIP     bool
}

type ActivityStore interface {
	WriteActivity(context.Context, []ActivityUpdate) error
}

type ActivityConfig struct {
	Capacity       int
	BatchSize      int
	OnlineInterval time.Duration
	RetryInterval  time.Duration
	ChangeDebounce time.Duration
	IdleTTL        time.Duration
}

type sessionActivityState struct {
	lastSeen        time.Time
	lastTouched     time.Time
	persistedOnline time.Time
	userAgent       string
	persistedUA     string
	uaKnown         bool
	ip              string
	persistedIP     string
	ipKnown         bool
	onlineDirty     bool
	uaDirty         bool
	ipDirty         bool
}

func (s *sessionActivityState) dirty() bool {
	return s.onlineDirty || s.uaDirty || s.ipDirty
}

// ActivityTracker coalesces high-frequency session touches into bounded,
// retryable batches keyed only by a one-way credential digest.
type ActivityTracker struct {
	store  ActivityStore
	config ActivityConfig

	mu     sync.Mutex
	states map[[32]byte]*sessionActivityState
	closed bool

	wake      chan struct{}
	flushGate chan struct{}
	runCtx    context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	storeOnce sync.Once
	closeErr  error
}

func NewActivityTracker(store ActivityStore, config ActivityConfig) (*ActivityTracker, error) {
	if store == nil {
		return nil, errors.New("session activity store is required")
	}
	if config.Capacity <= 0 {
		config.Capacity = DefaultActivityCapacity
	}
	if config.Capacity > DefaultActivityCapacity {
		return nil, fmt.Errorf("session activity capacity cannot exceed %d", DefaultActivityCapacity)
	}
	if config.BatchSize <= 0 {
		config.BatchSize = DefaultActivityBatchSize
	}
	if config.BatchSize > config.Capacity {
		return nil, errors.New("session activity batch size cannot exceed capacity")
	}
	if config.OnlineInterval <= 0 {
		config.OnlineInterval = DefaultActivityOnlineInterval
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = DefaultActivityRetryInterval
	}
	if config.ChangeDebounce < 0 {
		return nil, errors.New("session activity change debounce cannot be negative")
	}
	if config.ChangeDebounce == 0 {
		config.ChangeDebounce = DefaultActivityChangeDebounce
	}
	if config.IdleTTL <= 0 {
		config.IdleTTL = DefaultActivityIdleTTL
	}
	runCtx, cancel := context.WithCancel(context.Background())
	tracker := &ActivityTracker{
		store: store, config: config, states: make(map[[32]byte]*sessionActivityState),
		wake: make(chan struct{}, 1), flushGate: make(chan struct{}, 1),
		runCtx: runCtx, cancel: cancel, done: make(chan struct{}),
	}
	tracker.flushGate <- struct{}{}
	go tracker.run()
	return tracker, nil
}

func trimActivityValue(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}

func (t *ActivityTracker) Touch(session, userAgent, ip string, now time.Time) error {
	if session == "" {
		return ErrActivitySession
	}
	digest := credentialcache.Digest(session)
	return t.touch(digest, now, &userAgent, &ip, true)
}

func (t *ActivityTracker) TouchOnline(session string, now time.Time) error {
	if session == "" {
		return ErrActivitySession
	}
	return t.touch(credentialcache.Digest(session), now, nil, nil, true)
}

func (t *ActivityTracker) TouchUserAgent(session, userAgent string, now time.Time) error {
	if session == "" {
		return ErrActivitySession
	}
	return t.touch(credentialcache.Digest(session), now, &userAgent, nil, false)
}

func (t *ActivityTracker) TouchIP(session, ip string, now time.Time) error {
	if session == "" {
		return ErrActivitySession
	}
	return t.touch(credentialcache.Digest(session), now, nil, &ip, false)
}

func (t *ActivityTracker) touch(digest [32]byte, now time.Time, userAgent, ip *string, online bool) error {
	if now.IsZero() {
		now = time.Now()
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return ErrActivityClosed
	}
	state := t.states[digest]
	if state == nil {
		t.pruneLocked(now)
		if len(t.states) >= t.config.Capacity && !t.evictOldestCleanLocked() {
			t.mu.Unlock()
			return ErrActivityCapacity
		}
		state = &sessionActivityState{}
		t.states[digest] = state
	}
	if now.After(state.lastTouched) {
		state.lastTouched = now
	}
	if online {
		if now.After(state.lastSeen) {
			state.lastSeen = now
		}
		if state.persistedOnline.IsZero() || state.lastSeen.Sub(state.persistedOnline) >= t.config.OnlineInterval {
			state.onlineDirty = true
		}
	}
	if userAgent != nil {
		value := trimActivityValue(*userAgent, MaxActivityUserAgentBytes)
		if !state.uaKnown || value != state.userAgent {
			state.userAgent = value
			state.uaKnown = true
			state.uaDirty = true
		}
	}
	if ip != nil {
		value := trimActivityValue(*ip, MaxActivityIPBytes)
		if !state.ipKnown || value != state.ip {
			state.ip = value
			state.ipKnown = true
			state.ipDirty = true
		}
	}
	dirty := state.dirty()
	t.mu.Unlock()
	if dirty {
		t.signal()
	}
	return nil
}

func (t *ActivityTracker) signal() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *ActivityTracker) pruneLocked(now time.Time) {
	for digest, state := range t.states {
		if !state.dirty() && now.Sub(state.lastTouched) >= t.config.IdleTTL {
			delete(t.states, digest)
		}
	}
}

func (t *ActivityTracker) evictOldestCleanLocked() bool {
	var oldestDigest [32]byte
	var oldest time.Time
	found := false
	for digest, state := range t.states {
		if state.dirty() || (found && !state.lastTouched.Before(oldest)) {
			continue
		}
		oldestDigest, oldest, found = digest, state.lastTouched, true
	}
	if found {
		delete(t.states, oldestDigest)
	}
	return found
}

func (t *ActivityTracker) run() {
	defer close(t.done)
	retry := time.NewTicker(t.config.RetryInterval)
	defer retry.Stop()
	for {
		select {
		case <-t.runCtx.Done():
			return
		case <-t.wake:
			timer := time.NewTimer(t.config.ChangeDebounce)
			select {
			case <-t.runCtx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			_ = t.Flush(t.runCtx)
		case <-retry.C:
			_ = t.Flush(t.runCtx)
		}
	}
}

func (t *ActivityTracker) snapshot() []ActivityUpdate {
	t.mu.Lock()
	defer t.mu.Unlock()
	updates := make([]ActivityUpdate, 0, min(t.config.BatchSize, len(t.states)))
	for digest, state := range t.states {
		if !state.dirty() {
			continue
		}
		updates = append(updates, ActivityUpdate{
			Digest: digest, LastSeen: state.lastSeen, UserAgent: state.userAgent, IP: state.ip,
			WriteOnline: state.onlineDirty, WriteUA: state.uaDirty, WriteIP: state.ipDirty,
		})
		if len(updates) == t.config.BatchSize {
			break
		}
	}
	return updates
}

func (t *ActivityTracker) markPersisted(updates []ActivityUpdate) {
	now := time.Now()
	t.mu.Lock()
	for _, update := range updates {
		state := t.states[update.Digest]
		if state == nil {
			continue
		}
		if update.WriteOnline {
			state.persistedOnline = update.LastSeen
			state.onlineDirty = !state.lastSeen.IsZero() && state.lastSeen.Sub(state.persistedOnline) >= t.config.OnlineInterval
		}
		if update.WriteUA {
			state.persistedUA = update.UserAgent
			state.uaDirty = state.userAgent != state.persistedUA
		}
		if update.WriteIP {
			state.persistedIP = update.IP
			state.ipDirty = state.ip != state.persistedIP
		}
		if state.dirty() {
			t.signal()
		}
	}
	t.pruneLocked(now)
	t.mu.Unlock()
}

func (t *ActivityTracker) Flush(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.flushGate:
	}
	defer func() { t.flushGate <- struct{}{} }()
	maxBatches := (t.config.Capacity + t.config.BatchSize - 1) / t.config.BatchSize
	for batch := 0; batch < maxBatches; batch++ {
		updates := t.snapshot()
		if len(updates) == 0 {
			return nil
		}
		if err := t.store.WriteActivity(ctx, updates); err != nil {
			observability.ObserveSessionActivity(len(updates), true)
			return err
		}
		observability.ObserveSessionActivity(len(updates), false)
		t.markPersisted(updates)
	}
	// Continuous metadata churn cannot monopolize the writer forever. Remaining
	// dirty states retain ownership and schedule the next bounded pass.
	if t.Pending() > 0 {
		t.signal()
	}
	return nil
}

func (t *ActivityTracker) Forget(session string) {
	t.ForgetDigest(credentialcache.Digest(session))
}

func (t *ActivityTracker) ForgetDigest(digest [32]byte) {
	t.mu.Lock()
	delete(t.states, digest)
	t.mu.Unlock()
}

func (t *ActivityTracker) Clear() {
	t.mu.Lock()
	clear(t.states)
	t.mu.Unlock()
}

func (t *ActivityTracker) Pending() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	count := 0
	for _, state := range t.states {
		if state.dirty() {
			count++
		}
	}
	return count
}

func (t *ActivityTracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.states)
}

func (t *ActivityTracker) Close(ctx context.Context) error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		t.closeErr = t.Flush(ctx)
		t.cancel()
	})
	select {
	case <-t.done:
		t.storeOnce.Do(func() {
			if closer, ok := t.store.(interface{ Close() error }); ok {
				t.closeErr = errors.Join(t.closeErr, closer.Close())
			}
		})
		return t.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

type sqliteActivityStore struct {
	db        *sql.DB
	mu        sync.Mutex
	statement *sql.Stmt
}

func (s *sqliteActivityStore) prepared(ctx context.Context) (*sql.Stmt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statement != nil {
		return s.statement, nil
	}
	statement, err := s.db.PrepareContext(ctx, `UPDATE sessions SET
		latest_online = CASE WHEN ? THEN ? ELSE latest_online END,
		latest_user_agent = CASE WHEN ? THEN ? ELSE latest_user_agent END,
		latest_ip = CASE WHEN ? THEN ? ELSE latest_ip END
		WHERE session_digest = ? AND expires > ?`)
	if err != nil {
		return nil, err
	}
	s.statement = statement
	return statement, nil
}

func (s *sqliteActivityStore) WriteActivity(ctx context.Context, updates []ActivityUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	statement, err := s.prepared(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	for i := range updates {
		update := &updates[i]
		if _, err := tx.StmtContext(ctx, statement).ExecContext(ctx,
			update.WriteOnline, update.LastSeen,
			update.WriteUA, update.UserAgent,
			update.WriteIP, update.IP,
			update.Digest[:], now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *sqliteActivityStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statement == nil {
		return nil
	}
	err := s.statement.Close()
	s.statement = nil
	return err
}

var (
	defaultActivityMu      sync.Mutex
	defaultActivityTracker *ActivityTracker
	defaultActivityClosed  bool
)

func defaultSessionActivity() (*ActivityTracker, error) {
	defaultActivityMu.Lock()
	defer defaultActivityMu.Unlock()
	if defaultActivityClosed {
		return nil, ErrActivityClosed
	}
	if defaultActivityTracker != nil {
		return defaultActivityTracker, nil
	}
	db, err := dbcore.GetWriterDBInstance()
	if err != nil {
		return nil, err
	}
	defaultActivityTracker, err = NewActivityTracker(&sqliteActivityStore{db: db}, ActivityConfig{})
	return defaultActivityTracker, err
}

func forgetDefaultSessionActivity(session string) {
	defaultActivityMu.Lock()
	tracker := defaultActivityTracker
	defaultActivityMu.Unlock()
	if tracker != nil {
		tracker.Forget(session)
	}
}

func clearDefaultSessionActivity() {
	defaultActivityMu.Lock()
	tracker := defaultActivityTracker
	defaultActivityMu.Unlock()
	if tracker != nil {
		tracker.Clear()
	}
}

func CloseSessionActivity(ctx context.Context) error {
	defaultActivityMu.Lock()
	tracker := defaultActivityTracker
	defaultActivityTracker = nil
	defaultActivityClosed = true
	defaultActivityMu.Unlock()
	if tracker == nil {
		return nil
	}
	return tracker.Close(ctx)
}
