package oauth

import (
	"testing"

	"github.com/komari-monitor/komari/utils/oauth/factory"
)

// Test function
func TestRegisterAndGetProviderConfigs(t *testing.T) {
	All()
	configs := factory.GetProviderConfigs()
	if len(configs) == 0 {
		t.Error("Expected non-empty provider configs, got empty")
	}
	providers := factory.GetAllOidcProviders()
	if len(providers) == 0 {
		t.Error("Expected non-empty OIDC providers, got empty")
	}
	names := factory.GetAllOidcProviderNames()
	if len(names) == 0 {
		t.Error("Expected non-empty OIDC provider names, got empty")
	}

	if err := LoadProvider("github", `{}`); err != nil {
		t.Fatalf("LoadProvider() error = %v", err)
	}
	current := CurrentProvider()
	if current == nil {
		t.Fatal("Expected current provider to be initialized")
	}
	cfg := current.GetConfiguration()
	if cfg == nil {
		t.Error("Expected non-nil configuration for 'github' provider, got nil")
	}
}
