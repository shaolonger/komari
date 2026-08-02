package clients

import "testing"

func TestNormalizeHomeFacetsSplitsAndDedupesValues(t *testing.T) {
	facets, err := NormalizeHomeFacets(map[string]interface{}{
		"provider": " DMIT; Vultr;DMIT ",
		"line":     []interface{}{"CMI", "CN2", "CMI", " "},
		"empty":    "",
	})
	if err != nil {
		t.Fatalf("NormalizeHomeFacets() error = %v", err)
	}

	if got := facets["provider"]; len(got) != 2 || got[0] != "DMIT" || got[1] != "Vultr" {
		t.Fatalf("provider facets = %#v", got)
	}
	if got := facets["line"]; len(got) != 2 || got[0] != "CMI" || got[1] != "CN2" {
		t.Fatalf("line facets = %#v", got)
	}
	if _, exists := facets["empty"]; exists {
		t.Fatal("empty facet should be removed")
	}
}

func TestNormalizeHomeFacetsRejectsInvalidDimension(t *testing.T) {
	if _, err := NormalizeHomeFacets(map[string]interface{}{
		"bad id": "DMIT",
	}); err == nil {
		t.Fatal("expected invalid dimension id to be rejected")
	}
}

func TestNormalizeHomeFacetsRejectsNonStringValues(t *testing.T) {
	if _, err := NormalizeHomeFacets(map[string]interface{}{
		"provider": []interface{}{"DMIT", 42},
	}); err == nil {
		t.Fatal("expected non-string value to be rejected")
	}
}

func TestEncodeDecodeHomeFacetsRoundTrip(t *testing.T) {
	encoded, err := EncodeHomeFacets(HomeFacetValues{
		"provider": {"DMIT"},
		"line":     {"CMI", "CN2"},
	})
	if err != nil {
		t.Fatalf("EncodeHomeFacets() error = %v", err)
	}
	if encoded == "" {
		t.Fatal("encoded facets should not be empty")
	}

	decoded := DecodeHomeFacets(encoded)
	if got := decoded["line"]; len(got) != 2 || got[0] != "CMI" || got[1] != "CN2" {
		t.Fatalf("decoded line facets = %#v", got)
	}
}

func TestDecodeHomeFacetsIgnoresInvalidPayload(t *testing.T) {
	if got := DecodeHomeFacets(`{"bad id":["x"]}`); len(got) != 0 {
		t.Fatalf("DecodeHomeFacets invalid payload = %#v, want empty", got)
	}
}
