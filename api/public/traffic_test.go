package public

import (
	"testing"

	"github.com/komari-monitor/komari/database/models"
)

func TestFilterTrafficClientsHonorsSelectionAndHiddenVisibility(t *testing.T) {
	clients := []models.Client{
		{UUID: "visible-a", Name: "Visible A"},
		{UUID: "hidden-b", Name: "Hidden B", Hidden: true},
		{UUID: "visible-c", Name: "Visible C"},
	}
	requested := map[string]struct{}{
		"hidden-b":  {},
		"visible-c": {},
	}

	anonymous := filterTrafficClients(clients, requested, false)
	if len(anonymous) != 1 || anonymous[0].UUID != "visible-c" {
		t.Fatalf("anonymous clients = %+v, want only visible-c", anonymous)
	}

	loggedIn := filterTrafficClients(clients, requested, true)
	if len(loggedIn) != 2 {
		t.Fatalf("logged-in clients len = %d, want 2", len(loggedIn))
	}
	if loggedIn[0].UUID != "hidden-b" || loggedIn[1].UUID != "visible-c" {
		t.Fatalf("logged-in clients = %+v, want hidden-b then visible-c", loggedIn)
	}
}

func TestFilterTrafficClientsReturnsAllVisibleWhenNoSelection(t *testing.T) {
	clients := []models.Client{
		{UUID: "visible-a", Name: "Visible A"},
		{UUID: "hidden-b", Name: "Hidden B", Hidden: true},
		{UUID: "visible-c", Name: "Visible C"},
	}

	got := filterTrafficClients(clients, nil, false)
	if len(got) != 2 {
		t.Fatalf("clients len = %d, want 2", len(got))
	}
	if got[0].UUID != "visible-a" || got[1].UUID != "visible-c" {
		t.Fatalf("clients = %+v, want visible-a and visible-c", got)
	}
}
