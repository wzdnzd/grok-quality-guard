package main

import (
	"fmt"
	"testing"

	proxy "grok-quality-guard/internal/proxy"
)

func BenchmarkProxySourceSelection(b *testing.B) {
	for _, accounts := range []int{100, 10000, 100000} {
		b.Run(fmt.Sprint(accounts), func(b *testing.B) {
			store := NewStateStore("")
			store.state = State{Accounts: make(map[string]*AccountState, accounts)}
			for i := 0; i < 10; i++ {
				store.state.Proxy.Nodes = append(store.state.Proxy.Nodes, proxy.Node{ID: fmt.Sprint(i), Name: fmt.Sprint(i), Enabled: true, Revision: 1, ConfigVersion: 1, Endpoint: proxy.Endpoint{Protocol: proxy.ProtocolHTTP, Host: "proxy.example", Port: 8000 + i}})
			}
			for i := 0; i < accounts; i++ {
				id := fmt.Sprint(i)
				store.state.Accounts[id] = &AccountState{AuthID: id, Proxy: proxy.Binding{SourceID: fmt.Sprint(i % 10), SourceKind: proxy.SourceNode}}
			}
			store.rebuildProxyIndexesLocked()
			plugin := NewPlugin()
			plugin.store = store
			plugin.proxyCatalog()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				source, _ := plugin.selectProxySource(proxyBindOptions{SourceScope: "nodes", DryRun: true}, "")
				if source == nil {
					b.Fatal("source missing")
				}
			}
		})
	}
}
