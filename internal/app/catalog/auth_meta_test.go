package catalog

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	proxyapp "grok-quality-guard/internal/app/proxy"
)

func TestCatalogRefreshDoesNotWaitForAuthMetadata(t *testing.T) {
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{
		{ID: "auth-0", AuthIndex: "auth-0", Type: "xai", Provider: "xai"},
		{ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai", Path: "auth-1.json"},
	})
	service := NewService(env)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	var reads atomic.Int32
	service.authMetaRead = func(string) (string, string, bool) {
		reads.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return "socks5h://user:secret@proxy.example:1080", "oauth-1", true
	}

	done := make(chan error, 1)
	go func() { done <- service.RefreshCatalog(context.Background()) }()
	select {
	case errRefresh := <-done:
		if errRefresh != nil {
			t.Fatal(errRefresh)
		}
	case <-time.After(time.Second):
		t.Fatal("catalog refresh waited for auth metadata")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("auth metadata refresh did not start")
	}
	account, ok := service.FindAccount("auth-1")
	if !ok {
		t.Fatal("catalog account was not published")
	}
	if account.ProxyURLKnown || account.ProxyMasked != "" || account.OAuthID != "" {
		t.Fatalf("catalog waited for metadata before publishing: %#v", account)
	}

	waitDone := make(chan struct{})
	go func() {
		service.WaitAuthMetaRefresh()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("WaitAuthMetaRefresh returned before metadata completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	released = true
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("WaitAuthMetaRefresh did not return after metadata completed")
	}
	account, ok = service.FindAccount("auth-1")
	if !ok || !account.ProxyURLKnown || account.ProxyMasked != proxyapp.MaskProxyURL("socks5h://user:secret@proxy.example:1080") || account.OAuthID != "oauth-1" {
		t.Fatalf("auth metadata was not enriched: %#v", account)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("auth metadata reads = %d, want 1", got)
	}
}

func TestAuthMetadataRefreshDiscardsStaleCatalogGeneration(t *testing.T) {
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai", Path: "auth-1.json",
	}})
	service := NewService(env)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	service.authMetaRead = func(path string) (string, string, bool) {
		if calls.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
			return "socks5h://old:secret@proxy.example:1080", "old", true
		}
		return "socks5h://new:secret@proxy.example:1080", "new", true
	}

	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first auth metadata refresh did not start")
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-2", AuthIndex: "auth-2", Type: "xai", Provider: "xai", Path: "auth-2.json",
	}})
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		close(releaseFirst)
		t.Fatal(errRefresh)
	}
	close(releaseFirst)
	service.WaitAuthMetaRefresh()

	account, ok := service.FindAccount("auth-2")
	if !ok || !account.ProxyURLKnown || account.OAuthID != "new" {
		t.Fatalf("new catalog was not enriched after stale result: %#v", account)
	}
	if stale, exists := service.FindAccount("auth-1"); exists && stale.ProxyURLKnown {
		t.Fatalf("stale auth metadata was applied: %#v", stale)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("auth metadata reads = %d, want 2", got)
	}
}

func TestAuthMetadataRefreshRetainsUnchangedRowsAcrossCatalogRefresh(t *testing.T) {
	now := time.Now().UTC()
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai",
		Path: "auth-1.json", Size: 10, ModTime: now,
	}})
	service := NewService(env)
	t.Cleanup(service.Close)
	firstStarted := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	service.authMetaRead = func(path string) (string, string, bool) {
		calls.Add(1)
		switch path {
		case "auth-1.json":
			select {
			case firstStarted <- struct{}{}:
			default:
			}
			<-releaseFirst
			return "socks5h://old:secret@proxy.example:1080", "old", true
		case "auth-2.json":
			return "socks5h://new:secret@proxy.example:1080", "new", true
		default:
			return "", "", false
		}
	}

	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first auth metadata refresh did not start")
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{
		{ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai", Path: "auth-1.json", Size: 10, ModTime: now},
		{ID: "auth-2", AuthIndex: "auth-2", Type: "xai", Provider: "xai", Path: "auth-2.json", Size: 20, ModTime: now},
	})
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		close(releaseFirst)
		t.Fatal(errRefresh)
	}
	close(releaseFirst)
	service.WaitAuthMetaRefresh()

	first, ok := service.FindAccount("auth-1")
	if !ok || !first.ProxyURLKnown || first.OAuthID != "old" {
		t.Fatalf("unchanged row metadata was discarded: %#v", first)
	}
	second, ok := service.FindAccount("auth-2")
	if !ok || !second.ProxyURLKnown || second.OAuthID != "new" {
		t.Fatalf("new row metadata was not enriched: %#v", second)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("auth metadata reads = %d, want 2", got)
	}
}
func TestAuthMetadataFailureBackoffSkipsUnchangedFiles(t *testing.T) {
	now := time.Now().UTC()
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai",
		Path: "unreadable-auth.json", Size: 10, ModTime: now,
	}})
	service := NewService(env)
	t.Cleanup(service.Close)
	var reads atomic.Int32
	service.authMetaRead = func(string) (string, string, bool) {
		reads.Add(1)
		return "", "", false
	}

	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	service.WaitAuthMetaRefresh()
	if got := reads.Load(); got != 1 {
		t.Fatalf("first auth metadata reads = %d, want 1", got)
	}
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	service.WaitAuthMetaRefresh()
	if got := reads.Load(); got != 1 {
		t.Fatalf("unchanged failed auth was reread during backoff: reads = %d", got)
	}

	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai",
		Path: "unreadable-auth.json", Size: 11, ModTime: now.Add(time.Second),
	}})
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	service.WaitAuthMetaRefresh()
	if got := reads.Load(); got != 2 {
		t.Fatalf("changed failed auth was not retried: reads = %d", got)
	}
	service.WaitAuthModelsRefresh()
}
func TestWaitAuthMetaRefreshStartsQueuedPass(t *testing.T) {
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
	}
	service := NewService(env)
	service.catalog = []Account{{ID: "auth-1", AuthIndex: "auth-1", Path: "auth-1.json"}}
	service.catalogByID = map[string]int{"auth-1": 0}
	service.authMetaRead = func(string) (string, string, bool) {
		return "socks5h://user:secret@proxy.example:1080", "oauth-1", true
	}
	service.authMetaRefreshMu.Lock()
	service.authMetaRefreshPending = true
	service.authMetaRefreshMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !service.WaitAuthMetaRefreshContext(ctx) {
		t.Fatal("queued auth metadata pass was not started")
	}
	account, ok := service.FindAccount("auth-1")
	if !ok || !account.ProxyURLKnown || account.OAuthID != "oauth-1" {
		t.Fatalf("queued auth metadata pass was not applied: %#v", account)
	}
}
func TestAuthMetadataFailureMarksFileSignatureInspected(t *testing.T) {
	now := time.Now().UTC()
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai",
		Path: "unreadable-auth.json", Size: 10, ModTime: now,
	}})
	service := NewService(env)
	t.Cleanup(service.Close)
	var reads atomic.Int32
	service.authMetaRead = func(string) (string, string, bool) {
		reads.Add(1)
		return "", "", false
	}

	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	service.WaitAuthMetaRefresh()
	account, ok := service.FindAccount("auth-1")
	if !ok || account.ProxyURLKnown || !account.ProxyMetaInspected {
		t.Fatalf("failed auth metadata read was not marked inspected: %#v", account)
	}
	if jobs := service.pendingAuthMetaJobs(service.catalogSnapshot()); len(jobs) != 0 {
		t.Fatalf("failed auth metadata remained pending during backoff: %#v", jobs)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("first auth metadata reads = %d, want 1", got)
	}

	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai",
		Path: "unreadable-auth.json", Size: 11, ModTime: now.Add(time.Second),
	}})
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	service.WaitAuthMetaRefresh()
	account, ok = service.FindAccount("auth-1")
	if !ok || account.ProxyURLKnown || !account.ProxyMetaInspected {
		t.Fatalf("changed auth file was not retried and inspected: %#v", account)
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("changed auth metadata reads = %d, want 2", got)
	}
	service.WaitAuthModelsRefresh()
}
func TestEnsureAuthMetaForIDsReadsOnlyTheRequestedPage(t *testing.T) {
	now := time.Now().UTC()
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{
		{ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai", Path: "auth-1.json", Size: 10, ModTime: now},
		{ID: "auth-2", AuthIndex: "auth-2", Type: "xai", Provider: "xai", Path: "auth-2.json", Size: 20, ModTime: now},
	})
	service := NewService(env)
	t.Cleanup(service.Close)
	var reads atomic.Int32
	var firstPath atomic.Value
	service.authMetaRead = func(path string) (string, string, bool) {
		reads.Add(1)
		firstPath.Store(path)
		return "socks5h://user:secret@proxy.example:1080", "oauth-" + path, true
	}
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	service.WaitAuthMetaRefresh()
	service.catalogMu.Lock()
	for index := range service.catalog {
		service.catalog[index].ProxyURLKnown = false
		service.catalog[index].ProxyMetaInspected = false
		service.catalog[index].ProxyMasked = ""
		service.catalog[index].ProxyHash = ""
		service.catalog[index].OAuthID = ""
	}
	service.catalogMu.Unlock()
	reads.Store(0)
	firstPath.Store("")

	if errEnsure := service.EnsureAuthMetaForIDs(context.Background(), []string{"auth-2"}, 450*time.Millisecond); errEnsure != nil {
		t.Fatal(errEnsure)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("page-scoped metadata reads = %d, want 1", got)
	}
	if got, _ := firstPath.Load().(string); got != "auth-2.json" {
		t.Fatalf("page-scoped metadata read path = %q, want auth-2.json", got)
	}
	first, _ := service.FindAccount("auth-1")
	second, _ := service.FindAccount("auth-2")
	if first.ProxyURLKnown || !second.ProxyURLKnown {
		t.Fatalf("page-scoped enrichment touched the wrong rows: first=%#v second=%#v", first, second)
	}
}
