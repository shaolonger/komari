package clients

import "testing"

func TestNormalizeAssetMetadataUppercasesCurrencyCode(t *testing.T) {
	updates := map[string]interface{}{
		"provider":      "  CloudSilk  ",
		"business_role": "  ingress  ",
		"currency_code": "usd",
		"currency":      "  $  ",
	}

	if err := normalizeAssetMetadata(updates); err != nil {
		t.Fatalf("normalizeAssetMetadata() error = %v", err)
	}

	if got := updates["provider"]; got != "CloudSilk" {
		t.Fatalf("provider = %v, want %q", got, "CloudSilk")
	}
	if got := updates["business_role"]; got != "ingress" {
		t.Fatalf("business_role = %v, want %q", got, "ingress")
	}
	if got := updates["currency_code"]; got != "USD" {
		t.Fatalf("currency_code = %v, want %q", got, "USD")
	}
	if got := updates["currency"]; got != "$" {
		t.Fatalf("currency = %v, want %q", got, "$")
	}
}

func TestNormalizeAssetMetadataRejectsInvalidCurrencyCode(t *testing.T) {
	updates := map[string]interface{}{
		"currency_code": "usdollar",
	}

	if err := normalizeAssetMetadata(updates); err == nil {
		t.Fatal("expected invalid currency code to be rejected")
	}
}

func TestNormalizeAssetMetadataRejectsNonBooleanAssetIgnored(t *testing.T) {
	updates := map[string]interface{}{
		"asset_ignored": "true",
	}

	if err := normalizeAssetMetadata(updates); err == nil {
		t.Fatal("expected non-boolean asset_ignored to be rejected")
	}
}

func TestNormalizeAssetMetadataNormalizesGovernanceFields(t *testing.T) {
	updates := map[string]interface{}{
		"governance_status": "  Ignore  ",
		"governance_note":   "  retire after migration  ",
	}

	if err := normalizeAssetMetadata(updates); err != nil {
		t.Fatalf("normalizeAssetMetadata() error = %v", err)
	}

	if got := updates["governance_status"]; got != "ignored" {
		t.Fatalf("governance_status = %v, want %q", got, "ignored")
	}
	if got := updates["governance_note"]; got != "retire after migration" {
		t.Fatalf("governance_note = %v, want %q", got, "retire after migration")
	}
}

func TestNormalizeCapabilityMetadataAcceptsBooleanFields(t *testing.T) {
	updates := map[string]interface{}{
		"capability_ping":                 true,
		"capability_terminal":             false,
		"capability_remote_exec":          true,
		"capability_remote_control":       false,
		"capability_gpu":                  true,
		"capability_auto_update":          true,
		"capability_private_ping_targets": false,
	}

	if err := normalizeCapabilityMetadata(updates); err != nil {
		t.Fatalf("normalizeCapabilityMetadata() error = %v", err)
	}
}

func TestNormalizeCapabilityMetadataRejectsInvalidTypes(t *testing.T) {
	updates := map[string]interface{}{
		"capability_ping": "true",
	}

	if err := normalizeCapabilityMetadata(updates); err == nil {
		t.Fatal("expected invalid capability type to be rejected")
	}
}
