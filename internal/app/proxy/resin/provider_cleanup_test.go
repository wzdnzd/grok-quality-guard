package resin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"grok-quality-guard/internal/proxy"
)

func TestProviderUsesCompiledTemplateAndImmutableCleanupIdentity(t *testing.T) {
	platform := "平台@:/%{literal}"
	token := "{token}@:/%+汉字"
	account := "lease@:/%{}"
	settings := ProviderSettings{
		ProxyTemplate: "socks5h://{platform}.{account}:{proxy_token}@EXAMPLE.COM:2260",
		Platform:      platform, PlatformID: "old-uuid", ProxyToken: token, SnapshotID: "old-snapshot",
	}
	identityCalls := 0
	provider, err := NewProvider(settings, func(context.Context, proxy.GenerateRequest) (string, error) {
		identityCalls++
		return account, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	settings.PlatformID, settings.SnapshotID, settings.ProxyToken = "new-uuid", "new-snapshot", "new-token"
	result, err := provider.Generate(context.Background(), proxy.GenerateRequest{AuthID: "auth-one"})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := proxy.ParseEndpoint(result.URL, proxy.ParseOptions{})
	if err != nil || endpoint.Username != platform+"."+account || endpoint.Password != token || endpoint.Host != "example.com" {
		t.Fatalf("provider literals did not round-trip: %#v %v", endpoint, err)
	}
	if identityCalls != 1 || result.Cleanup == nil || result.Cleanup.PlatformID != "old-uuid" || result.Cleanup.SnapshotID != "old-snapshot" || result.Cleanup.Account != account {
		t.Fatalf("provider used current mutable settings: %#v calls=%d", result.Cleanup, identityCalls)
	}
}

func TestMigrationRecognizesOnlyTheLegacyPasswordMarker(t *testing.T) {
	for _, raw := range []string{
		"socks5h://{platform}.{account}-{proxy_token}:fixed@resin:2260",
		"socks5h://{platform}.{account}:prefix{proxy_token}@resin:2260",
		"socks5h://{platform}.{account}:fixed@{account}:2260",
		"socks5h://{platform}.%7Baccount%7D:{proxy_token}@resin:2260",
	} {
		if _, err := MigrateTemplate(raw, "Default", "fixed"); err == nil {
			t.Fatalf("invalid marker location was accepted: %q", raw)
		}
	}
	template, err := MigrateTemplate("socks5h://{platform}.{account}:{proxy_token}@resin:2260", "Default", "{literal}")
	if err != nil {
		t.Fatal(err)
	}
	if template.UsernamePattern != "Default.{account}" || template.Password != "{literal}" || !template.HasPassword || len(template.Placeholders) != 1 || template.Placeholders[0].Name != "account" {
		t.Fatalf("legacy markers were retained as ordinary variables: %#v", template)
	}
	if err := template.Validate(); err != nil {
		t.Fatal(err)
	}
	// Percent-encoded braces remain a fixed literal password.
	if _, err := MigrateTemplate("socks5h://Default.{account}:%7Bproxy_token%7D@resin:2260", "Default", "{proxy_token}"); err != nil {
		t.Fatalf("encoded literal password was reinterpreted: %v", err)
	}
	if _, err := MigrateTemplate("socks5h://Default.{account}:private-one@resin:2260", "Default", "private-two"); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("conflicting passwords were silently overridden or disclosed: %v", err)
	}
}

func TestProviderGenerationRequiresCleanupSnapshot(t *testing.T) {
	provider, err := NewProvider(ProviderSettings{
		ProxyTemplate: "socks5h://Default.{account}@resin:2260", Platform: "Default", ProxyToken: "token",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Generate(context.Background(), proxy.GenerateRequest{AuthID: "one"}); err == nil {
		t.Fatal("new managed generation omitted its cleanup snapshot")
	}
	if _, err := provider.RenderAccount("preview-account"); err != nil {
		t.Fatalf("local preview unexpectedly needs an API snapshot: %v", err)
	}
}

func TestCleanupHandlerDeletesFromOldSnapshot(t *testing.T) {
	var oldCalls, currentCalls atomic.Int32
	oldServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		oldCalls.Add(1)
		if request.Method != http.MethodDelete || request.URL.EscapedPath() != "/base/api/v1/platforms/old%2Fplatform/leases/lease%2Faccount" || request.Header.Get("Authorization") != "Bearer old-token" {
			t.Errorf("cleanup target or credential changed: path=%s", request.URL.EscapedPath())
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer oldServer.Close()
	currentServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		currentCalls.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer currentServer.Close()
	old := IntegrationSnapshot{ID: "snapshot-one", AdminURL: oldServer.URL + "/base", PlatformID: "old/platform", AdminToken: "old-token"}
	current := IntegrationSnapshot{ID: "snapshot-two", AdminURL: currentServer.URL, PlatformID: "new-platform", AdminToken: "new-token"}
	handler := CleanupHandler{Resolve: func(_ context.Context, id string) (IntegrationSnapshot, error) {
		if id == old.ID {
			return old, nil
		}
		return current, nil
	}}
	ref := proxy.LeaseRef{Provider: "resin", SnapshotID: old.ID, PlatformID: old.PlatformID, Account: "lease/account"}
	if err := handler.Delete(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if oldCalls.Load() != 1 || currentCalls.Load() != 0 {
		t.Fatal("old lease was sent to the current integration")
	}
	ref.PlatformID = current.PlatformID
	if err := handler.Delete(context.Background(), ref); err == nil || oldCalls.Load() != 1 || currentCalls.Load() != 0 {
		t.Fatal("mismatched snapshot/reference was accepted")
	}
}

func TestDeleteLeaseIdempotencyAndRetryInformation(t *testing.T) {
	for _, status := range []int{204, 404, 400, 401, 403, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Retry-After", "120")
				writer.WriteHeader(status)
				if status != http.StatusNoContent {
					writer.Write([]byte("private-response-token"))
				}
			}))
			defer server.Close()
			err := DeleteLease(context.Background(), server.URL, "platform", "admin-token", "lease")
			if status == 204 || status == 404 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var failure *DeleteError
			if !errors.As(err, &failure) || failure.Status != status || failure.RetryAfter != 2*time.Minute || failure.Body != "" || strings.Contains(err.Error(), "private-response-token") {
				t.Fatalf("cleanup failure lost retry details or disclosed response: %#v %v", failure, err)
			}
			if status == 400 && !failure.Permanent || status != 400 && failure.Permanent {
				t.Fatalf("incorrect retry category: %#v", failure)
			}
		})
	}
}

func TestDeleteLeaseDoesNotRedirectCredentialsOrUseEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirected.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	err := DeleteLease(context.Background(), source.URL, "platform", "private-admin-token", "lease")
	var failure *DeleteError
	if !errors.As(err, &failure) || failure.Status != http.StatusTemporaryRedirect || redirected.Load() != 0 {
		t.Fatalf("cleanup followed a redirect: calls=%d error=%v", redirected.Load(), err)
	}
}

func TestCleanupRejectsUnsafeTargetsWithoutDisclosingCredentials(t *testing.T) {
	for _, raw := range []string{
		"http://alice:private@host.example",
		"http://host.example?token=private",
		"http://host.example#private",
		"http://host.example:",
		"http://host.example:0",
		"ftp://host.example",
	} {
		if _, err := deleteURL(raw, "platform", "lease"); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe admin URL was accepted or disclosed: %v", err)
		}
	}
	for _, id := range []string{"", ".", "..", "bad\x00identity"} {
		if _, err := deleteURL("http://host.example", id, "lease"); err == nil {
			t.Fatalf("invalid cleanup resource accepted: %q", id)
		}
	}
}

func TestCleanupRetryAfterBoundsAndDate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if retryAfter("999999999", now) != time.Hour || retryAfter("-1", now) != 0 {
		t.Fatal("Retry-After seconds were not bounded")
	}
	if got := retryAfter(now.Add(2*time.Minute).Format(http.TimeFormat), now); got != 2*time.Minute {
		t.Fatalf("Retry-After date = %v", got)
	}
}

func TestResinRewritePreservesEncodedPassword(t *testing.T) {
	raw := "socks5h://Default.lease-old:p%7Bvalue%7D%40%3A%25@proxy.example:2260/"
	rewritten, err := RewriteAccount(raw, "lease-new")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if password != "p{value}@:%" || parsed.User.Username() != "Default.lease-new" || parsed.Path != "/" {
		t.Fatal("local account rewrite changed the static proxy structure")
	}
}

func BenchmarkCompiledResinRender(b *testing.B) {
	provider, err := NewProvider(ProviderSettings{
		ProxyTemplate: "socks5h://{platform}.{account}:{proxy_token}@resin:2260",
		Platform:      "Default", PlatformID: "uuid", ProxyToken: "token", SnapshotID: "snapshot",
	}, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := provider.RenderAccount("grok-test-abcdefghijkl"); err != nil {
			b.Fatal(err)
		}
	}
}
