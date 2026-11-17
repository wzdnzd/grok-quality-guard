package proxy

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func probeServerEndpoint(t *testing.T, rawURL string, protocol Protocol) Endpoint {
	t.Helper()
	endpoint, err := ParseEndpoint(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Protocol = protocol
	return endpoint
}

func TestProbeHTTPConnectAuthenticatesAndIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	authorization := "Basic " + base64.StdEncoding.EncodeToString([]byte("u@x:p:/%"))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect || request.RequestURI != "target.example:443" || request.Header.Get("Proxy-Authorization") != authorization {
			writer.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	endpoint := probeServerEndpoint(t, server.URL, ProtocolHTTP)
	endpoint.Username, endpoint.Password = "u@x", "p:/%"
	endpoint.HasUser, endpoint.HasPassword = true, true
	result, err := ProbeEndpointTo(context.Background(), endpoint, "target.example:443")
	if err != nil || !result.Reachable || result.Protocol != ProtocolHTTP || result.Level != "tunnel" {
		t.Fatalf("CONNECT verification failed: %#v %v", result, err)
	}
}

func TestProbeRejectsHTTPAuthenticationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer server.Close()
	result, err := ProbeEndpoint(context.Background(), probeServerEndpoint(t, server.URL, ProtocolHTTP))
	var issue *ValidationError
	if err == nil || result.Reachable || !errors.As(err, &issue) || issue.Code != "proxy_auth_failed" {
		t.Fatalf("HTTP 407 was treated as reachable: %#v %v", result, err)
	}
}

func TestProbeHTTPSTLSIsVerified(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	endpoint := probeServerEndpoint(t, server.URL, ProtocolHTTPS)
	if result, err := ProbeEndpoint(context.Background(), endpoint); err == nil || result.Reachable {
		t.Fatal("untrusted proxy TLS certificate was accepted")
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	result, err := ProbeEndpointWithOptions(context.Background(), endpoint, ProbeOptions{TLSRoots: roots})
	if err != nil || !result.Reachable || result.Protocol != ProtocolHTTPS {
		t.Fatalf("trusted HTTPS proxy tunnel failed: %#v %v", result, err)
	}
}

func startSOCKSProbeServer(t *testing.T, authenticated, reject bool) (Endpoint, <-chan string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	target := make(chan string, 1)
	finished := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			finished <- err
			return
		}
		defer connection.Close()
		connection.SetDeadline(time.Now().Add(3 * time.Second))
		handle := func() error {
			var greeting [3]byte
			if _, err := io.ReadFull(connection, greeting[:]); err != nil {
				return err
			}
			method := byte(0)
			if authenticated {
				method = 2
			}
			if greeting != [3]byte{5, 1, method} {
				return fmt.Errorf("unexpected greeting: %v", greeting)
			}
			if _, err := connection.Write([]byte{5, method}); err != nil {
				return err
			}
			if authenticated {
				var header [2]byte
				if _, err := io.ReadFull(connection, header[:]); err != nil {
					return err
				}
				username := make([]byte, int(header[1]))
				if _, err := io.ReadFull(connection, username); err != nil {
					return err
				}
				var length [1]byte
				if _, err := io.ReadFull(connection, length[:]); err != nil {
					return err
				}
				password := make([]byte, int(length[0]))
				if _, err := io.ReadFull(connection, password); err != nil {
					return err
				}
				if string(username) != "alice" || string(password) != "secret" {
					return fmt.Errorf("credentials did not match")
				}
				status := byte(0)
				if reject {
					status = 1
				}
				if _, err := connection.Write([]byte{1, status}); err != nil {
					return err
				}
				if reject {
					return nil
				}
			}
			var connect [4]byte
			if _, err := io.ReadFull(connection, connect[:]); err != nil {
				return err
			}
			if connect[0] != 5 || connect[1] != 1 || connect[2] != 0 {
				return fmt.Errorf("invalid CONNECT request")
			}
			var address []byte
			switch connect[3] {
			case 1:
				address = make([]byte, 4)
			case 4:
				address = make([]byte, 16)
			case 3:
				var length [1]byte
				if _, err := io.ReadFull(connection, length[:]); err != nil {
					return err
				}
				address = make([]byte, int(length[0]))
			default:
				return fmt.Errorf("invalid address type")
			}
			if _, err := io.ReadFull(connection, address); err != nil {
				return err
			}
			var port [2]byte
			if _, err := io.ReadFull(connection, port[:]); err != nil {
				return err
			}
			if port != [2]byte{1, 187} {
				return fmt.Errorf("CONNECT target port changed")
			}
			if connect[3] == 3 {
				target <- "domain:" + string(address)
			} else {
				target <- "ip:" + net.IP(address).String()
			}
			_, err := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
			return err
		}
		finished <- handle()
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return Endpoint{Protocol: ProtocolSOCKS5, Host: host, Port: port}, target, finished
}

func TestProbeSOCKS5AndSOCKS5HKeepDNSModes(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolSOCKS5, ProtocolSOCKS5H} {
		for _, authenticated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/auth=%v", protocol, authenticated), func(t *testing.T) {
				endpoint, target, finished := startSOCKSProbeServer(t, authenticated, false)
				endpoint.Protocol = protocol
				if authenticated {
					endpoint.Username, endpoint.Password = "alice", "secret"
					endpoint.HasUser, endpoint.HasPassword = true, true
				}
				result, err := ProbeEndpointTo(context.Background(), endpoint, "localhost:443")
				if err != nil || !result.Reachable {
					t.Fatalf("SOCKS5 tunnel failed: %#v %v", result, err)
				}
				got := <-target
				if protocol == ProtocolSOCKS5H && got != "domain:localhost" {
					t.Fatalf("SOCKS5H resolved target locally: %s", got)
				}
				if protocol == ProtocolSOCKS5 && got[:3] != "ip:" {
					t.Fatalf("SOCKS5 delegated target DNS: %s", got)
				}
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestProbeSOCKSAuthenticationFailureIsNotHealthy(t *testing.T) {
	endpoint, _, finished := startSOCKSProbeServer(t, true, true)
	endpoint.Username, endpoint.Password = "alice", "secret"
	endpoint.HasUser, endpoint.HasPassword = true, true
	result, err := ProbeEndpoint(context.Background(), endpoint)
	var issue *ValidationError
	if err == nil || result.Reachable || !errors.As(err, &issue) || issue.Code != "proxy_auth_failed" {
		t.Fatalf("failed SOCKS5 authentication reported healthy: %#v %v", result, err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestProbeCancellationStopsAnUnresponsiveProxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		connection.SetDeadline(time.Now().Add(time.Second))
		io.Copy(io.Discard, connection)
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := ProbeEndpoint(ctx, Endpoint{Protocol: ProtocolHTTP, Host: host, Port: port})
	if !errors.Is(err, context.DeadlineExceeded) || result.Reachable || time.Since(start) > time.Second {
		t.Fatalf("probe ignored cancellation: %#v %v", result, err)
	}
	<-done
}
