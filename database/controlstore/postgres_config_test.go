//go:build scale

package controlstore

import (
	"strings"
	"testing"
)

func TestPostgresPoolConfigRequiresStrictTLS(t *testing.T) {
	_, err := postgresPoolConfig(PostgresConfig{
		URL: "postgres://user:secret@localhost:5432/komari?sslmode=disable", RequireTLS: true,
	})
	if err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("configuration error leaked password")
	}
}

func TestPostgresPoolConfigBoundsAndApplicationName(t *testing.T) {
	config, err := postgresPoolConfig(PostgresConfig{
		URL:      "postgres://user:secret@localhost:5432/komari?sslmode=disable",
		MaxConns: 16, MinConns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxConns != 16 || config.MinConns != 2 {
		t.Fatalf("pool max=%d min=%d", config.MaxConns, config.MinConns)
	}
	if got := config.ConnConfig.RuntimeParams["application_name"]; got != "komari-control" {
		t.Fatalf("application_name=%q", got)
	}
	for _, test := range []PostgresConfig{
		{URL: config.ConnString(), MaxConns: 257},
		{URL: config.ConnString(), MaxConns: 2, MinConns: 3},
	} {
		if _, err := postgresPoolConfig(test); err == nil {
			t.Fatalf("invalid pool config accepted: %+v", test)
		}
	}
}

func TestPostgresPoolConfigRejectsInvalidURLWithoutEcho(t *testing.T) {
	_, err := postgresPoolConfig(PostgresConfig{URL: "postgres://user:top-secret@["})
	if err == nil {
		t.Fatal("invalid URL accepted")
	}
	if strings.Contains(err.Error(), "top-secret") {
		t.Fatal("parse error leaked password")
	}
}
