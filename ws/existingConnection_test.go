package ws

import "testing"

func TestDeleteClientConditionallyPreservesReplacement(t *testing.T) {
	oldConnection := &SafeConn{}
	newConnection := &SafeConn{}
	const uuid = "replacement-test"
	SetConnectedClients(uuid, oldConnection)
	SetConnectedClients(uuid, newConnection)
	DeleteClientConditionally(uuid, oldConnection)
	current, exists := GetConnectedClient(uuid)
	if !exists || current != newConnection {
		t.Fatal("old connection cleanup removed its replacement")
	}
	DeleteClientConditionally(uuid, newConnection)
	if _, exists := GetConnectedClient(uuid); exists {
		t.Fatal("current connection was not removed")
	}
}
