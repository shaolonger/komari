package rpc

import (
	"sort"
	"sync"
)

const ContractVersion = "komari.rpc.v2.3"

type Discovery struct {
	JSONRPCVersion string            `json:"jsonrpc_version"`
	Contract       string            `json:"contract"`
	Methods        []string          `json:"methods"`
	Capabilities   map[string]string `json:"capabilities"`
}

var (
	discoveryMu           sync.RWMutex
	discoveryCapabilities = map[string]string{}
)

// SetCapabilities replaces the immutable capability snapshot exposed by
// rpc.discover. Values are small contract revisions, not runtime state.
func SetCapabilities(capabilities map[string]string) {
	next := make(map[string]string, len(capabilities))
	for name, version := range capabilities {
		next[name] = version
	}
	discoveryMu.Lock()
	discoveryCapabilities = next
	discoveryMu.Unlock()
}

func discover() Discovery {
	methods := ListMethods()
	sort.Strings(methods)
	discoveryMu.RLock()
	capabilities := make(map[string]string, len(discoveryCapabilities))
	for name, version := range discoveryCapabilities {
		capabilities[name] = version
	}
	discoveryMu.RUnlock()
	return Discovery{
		JSONRPCVersion: RPC_VERSION,
		Contract:       ContractVersion,
		Methods:        methods,
		Capabilities:   capabilities,
	}
}
