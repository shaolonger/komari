package accounts

import (
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/credentialcache"
	"github.com/komari-monitor/komari/internal/observability"
)

const (
	sessionCredentialCapacity = 16_384
	sessionCredentialTTL      = 5 * time.Minute
	sessionCredentialMissTTL  = 10 * time.Second
)

type sessionCredential struct {
	UUID string
}

var sessionCredentials = credentialcache.New[sessionCredential](sessionCredentialCapacity)

func cachedSessionCredential(session string, now time.Time) (credentialcache.Entry[sessionCredential], bool) {
	entry, ok := sessionCredentials.Get(session, now)
	if ok {
		observability.CredentialCacheHit()
	} else {
		observability.CredentialCacheMiss()
	}
	return entry, ok
}

func cacheSessionCredential(session string, record models.Session, now time.Time, generation uint64) {
	createdAt := record.CreatedAt.ToTime()
	sessionCredentials.PutIfGeneration(session, credentialcache.Entry[sessionCredential]{
		Value:               sessionCredential{UUID: record.UUID},
		Found:               true,
		Version:             uint64(max(createdAt.UnixNano(), 0)),
		CredentialExpiresAt: record.Expires.ToTime(),
		CacheExpiresAt:      now.Add(sessionCredentialTTL),
	}, now, generation)
}

func cacheMissingSessionCredential(session string, now time.Time, generation uint64) {
	sessionCredentials.PutIfGeneration(session, credentialcache.Entry[sessionCredential]{
		Found:          false,
		CacheExpiresAt: now.Add(sessionCredentialMissTTL),
	}, now, generation)
}

func invalidateSessionCredential(session string) {
	if session == "" {
		return
	}
	sessionCredentials.Invalidate(session)
	observability.CredentialCacheInvalidation()
}

func clearSessionCredentials() {
	sessionCredentials.Clear()
	observability.CredentialCacheInvalidation()
}

func resetSessionCredentialCacheForTest() { sessionCredentials.Clear() }

func sessionCredentialGeneration() uint64 { return sessionCredentials.Generation() }
