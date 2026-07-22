package clients

import (
	"time"

	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/internal/credentialcache"
	"github.com/komari-monitor/komari/internal/observability"
)

const (
	clientCredentialCapacity = 16_384
	clientCredentialTTL      = 5 * time.Minute
	clientCredentialMissTTL  = 10 * time.Second
)

type clientCredential struct {
	UUID      string
	RevokedAt time.Time
}

var clientCredentials = credentialcache.New[clientCredential](clientCredentialCapacity)

func cachedClientCredential(token string, now time.Time) (credentialcache.Entry[clientCredential], bool) {
	entry, ok := clientCredentials.Get(token, now)
	if ok {
		observability.CredentialCacheHit()
	} else {
		observability.CredentialCacheMiss()
	}
	return entry, ok
}

func cacheClientCredential(token string, client models.Client, now time.Time, generation uint64) {
	updatedAt := client.UpdatedAt.ToTime()
	clientCredentials.PutIfGeneration(token, credentialcache.Entry[clientCredential]{
		Value: clientCredential{
			UUID:      client.UUID,
			RevokedAt: client.TokenRevokedAt.ToTime(),
		},
		Found:               true,
		Version:             uint64(max(updatedAt.UnixNano(), 0)),
		CredentialExpiresAt: client.TokenExpiresAt.ToTime(),
		CacheExpiresAt:      now.Add(clientCredentialTTL),
	}, now, generation)
}

func cacheMissingClientCredential(token string, now time.Time, generation uint64) {
	clientCredentials.PutIfGeneration(token, credentialcache.Entry[clientCredential]{
		Found:          false,
		CacheExpiresAt: now.Add(clientCredentialMissTTL),
	}, now, generation)
}

func invalidateClientCredential(tokens ...string) {
	for _, token := range tokens {
		if token == "" {
			continue
		}
		clientCredentials.Invalidate(token)
		observability.CredentialCacheInvalidation()
	}
}

func resetClientCredentialCacheForTest() { clientCredentials.Clear() }

func clientCredentialGeneration() uint64 { return clientCredentials.Generation() }
