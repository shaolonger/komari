package runtimeprofile

import "testing"

func TestResolveAutoNano(t *testing.T) {
	profile, err := Resolve("auto", Limits{CPUs: 1, MemoryBytes: 2560 * mebibyte})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != ProfileNano || profile.SQLiteReadConnections != 1 {
		t.Fatalf("unexpected nano profile: %+v", profile)
	}
	if profile.GoMemoryLimitBytes != 256*mebibyte {
		t.Fatalf("Go memory limit = %d", profile.GoMemoryLimitBytes)
	}
	if profile.SQLiteTempStore != "FILE" || profile.PingWorkers != 2 {
		t.Fatalf("unexpected nano bounds: %+v", profile)
	}
}

func TestResolveAutoStandard(t *testing.T) {
	profile, err := Resolve("auto", Limits{CPUs: 8, MemoryBytes: 8 * gibibyte})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != ProfileStandard || profile.SQLiteReadConnections != 8 {
		t.Fatalf("unexpected standard profile: %+v", profile)
	}
	if profile.GoMemoryLimitBytes != 2*gibibyte {
		t.Fatalf("Go memory limit = %d", profile.GoMemoryLimitBytes)
	}
}

func TestResolveExplicitProfilesAndBounds(t *testing.T) {
	profile, err := Resolve("nano", Limits{CPUs: 64, MemoryBytes: 512 * mebibyte})
	if err != nil {
		t.Fatal(err)
	}
	if profile.SQLiteReadConnections != 2 || profile.PingWorkers != 4 {
		t.Fatalf("nano concurrency is not bounded: %+v", profile)
	}
	if profile.GoMemoryLimitBytes != 102*mebibyte+419430 {
		t.Fatalf("unexpected proportional memory limit %d", profile.GoMemoryLimitBytes)
	}

	scale, err := Resolve("scale", Limits{CPUs: 64, MemoryBytes: 64 * gibibyte})
	if err != nil {
		t.Fatal(err)
	}
	if scale.GoMemoryLimitBytes != 0 || scale.PingWorkers != 32 {
		t.Fatalf("unexpected scale profile: %+v", scale)
	}
}

func TestResolveRejectsInvalidInput(t *testing.T) {
	if _, err := Resolve("turbo", Limits{CPUs: 1}); err == nil {
		t.Fatal("expected unknown profile to fail")
	}
	if _, err := Resolve("auto", Limits{CPUs: 1, MemoryBytes: -1}); err == nil {
		t.Fatal("expected negative memory to fail")
	}
}
