package jsonRpc

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/komari-monitor/komari/utils/rpc"
)

type checkedContract struct {
	Contract        string            `json:"contract"`
	JSONRPCVersion  string            `json:"jsonrpc_version"`
	Capabilities    map[string]string `json:"capabilities"`
	RequiredMethods []string          `json:"required_methods"`
}

func TestCheckedRPCContractMatchesRuntimeDiscovery(t *testing.T) {
	content, err := os.ReadFile("../../contracts/rpc-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract checkedContract
	if err := json.Unmarshal(content, &contract); err != nil {
		t.Fatal(err)
	}

	value, rpcErr := rpc.Invoke("rpc.discover", nil)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	discovery, ok := value.(rpc.Discovery)
	if !ok {
		t.Fatalf("discovery type = %T", value)
	}
	if discovery.Contract != contract.Contract || discovery.JSONRPCVersion != contract.JSONRPCVersion {
		t.Fatalf("runtime contract %#v does not match checked contract %#v", discovery, contract)
	}
	available := make(map[string]struct{}, len(discovery.Methods))
	for _, method := range discovery.Methods {
		available[method] = struct{}{}
	}
	for _, method := range contract.RequiredMethods {
		if _, ok := available[method]; !ok {
			t.Errorf("required method %q is not registered", method)
		}
	}
	for name, version := range contract.Capabilities {
		if discovery.Capabilities[name] != version {
			t.Errorf("capability %q = %q, want %q", name, discovery.Capabilities[name], version)
		}
	}
}
