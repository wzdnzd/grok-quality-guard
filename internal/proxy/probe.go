package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const defaultProbeTarget = "example.com:443"

type ProbeResult struct {
	Reachable bool     `json:"reachable"`
	LatencyMS int64    `json:"latency_ms"`
	Protocol  Protocol `json:"protocol"`
	Level     string   `json:"level"`
	Target    string   `json:"target"`
}

type ProbeOptions struct {
	Target   string
	TLSRoots *x509.CertPool
}

// ProbeEndpoint proves that the proxy can establish a tunnel to a test target.
// It uses direct dialing, normal TLS verification, and no environment proxy.
func ProbeEndpoint(ctx context.Context, endpoint Endpoint) (ProbeResult, error) {
	return ProbeEndpointWithOptions(ctx, endpoint, ProbeOptions{})
}

func ProbeEndpointTo(ctx context.Context, endpoint Endpoint, target string) (ProbeResult, error) {
	return ProbeEndpointWithOptions(ctx, endpoint, ProbeOptions{Target: target})
}

func ProbeEndpointWithOptions(ctx context.Context, endpoint Endpoint, options ProbeOptions) (result ProbeResult, err error) {
	started := time.Now()
	result.Protocol = endpoint.Protocol
	result.Level = "tunnel"
	defer func() {
		result.LatencyMS = time.Since(started).Milliseconds()
		result.Reachable = err == nil
	}()
	endpoint, err = endpoint.Normalize()
	if err != nil {
		return result, err
	}
	result.Protocol = endpoint.Protocol
	target := options.Target
	if target == "" {
		target = defaultProbeTarget
	}
	targetHost, portText, parseErr := net.SplitHostPort(target)
	if parseErr != nil {
		return result, invalid("probe_target_invalid", "target", "proxy test target requires a host and port")
	}
	targetHost, err = normalizeHost(targetHost)
	if err != nil {
		return result, invalid("probe_target_invalid", "target", "proxy test target host is invalid")
	}
	targetPort, parseErr := strconv.Atoi(portText)
	if parseErr != nil || targetPort < 1 || targetPort > 65535 {
		return result, invalid("probe_target_invalid", "target", "proxy test target port is invalid")
	}
	target = net.JoinHostPort(targetHost, strconv.Itoa(targetPort))
	result.Target = target
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(probeCtx, "tcp", net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)))
	if err != nil {
		return result, probeFailure(probeCtx, "proxy_connect_failed", "could not connect to the proxy", err)
	}
	defer connection.Close()
	rawConnection := connection
	stop := context.AfterFunc(probeCtx, func() { rawConnection.Close() })
	defer stop()
	if deadline, ok := probeCtx.Deadline(); ok {
		if err = connection.SetDeadline(deadline); err != nil {
			return result, err
		}
	}
	if endpoint.Protocol == ProtocolHTTPS {
		secure := tls.Client(connection, &tls.Config{
			ServerName: endpoint.Host, MinVersion: tls.VersionTLS12, RootCAs: options.TLSRoots,
		})
		if err = secure.HandshakeContext(probeCtx); err != nil {
			return result, probeFailure(probeCtx, "proxy_tls_failed", "proxy TLS verification failed", err)
		}
		connection = secure
	}
	switch endpoint.Protocol {
	case ProtocolHTTP, ProtocolHTTPS:
		err = probeHTTPConnect(connection, endpoint, target)
	case ProtocolSOCKS5, ProtocolSOCKS5H:
		err = probeSOCKSConnect(probeCtx, connection, endpoint, targetHost, targetPort)
	default:
		err = invalid("protocol_unsupported", "protocol", "proxy protocol is not supported for network testing")
	}
	if err != nil {
		if probeCtx.Err() != nil {
			err = probeCtx.Err()
		} else if deadline, ok := probeCtx.Deadline(); ok && !time.Now().Before(deadline) {
			// The socket deadline can fire just before the context timer runs.
			err = context.DeadlineExceeded
		}
	}
	return result, err
}

func probeFailure(ctx context.Context, code, message string, cause error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %v", invalid(code, "probe", message), cause)
}

func probeHTTPConnect(connection net.Conn, endpoint Endpoint, target string) error {
	request := &http.Request{
		Method: http.MethodConnect, URL: &url.URL{Opaque: target},
		Host: target, Header: make(http.Header),
	}
	if endpoint.HasUser {
		credentials := endpoint.Username + ":" + endpoint.Password
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	if err := request.Write(connection); err != nil {
		return fmt.Errorf("%w: %v", invalid("proxy_handshake_failed", "probe", "proxy CONNECT request failed"), err)
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(connection, 64<<10)), request)
	if err != nil {
		// A malformed response can echo credentials in its status line.
		return invalid("proxy_handshake_failed", "probe", "proxy CONNECT response is invalid")
	}
	// Close the connection before closing a CONNECT response body, which may
	// otherwise try to drain a tunnel until its deadline.
	connection.Close()
	response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusProxyAuthRequired:
		return invalid("proxy_auth_failed", "credentials", "proxy rejected authentication")
	default:
		return invalid("proxy_tunnel_failed", "probe", fmt.Sprintf("proxy CONNECT failed with status %d", response.StatusCode))
	}
}

func probeSOCKSConnect(ctx context.Context, connection net.Conn, endpoint Endpoint, targetHost string, targetPort int) error {
	method := byte(0)
	if endpoint.HasUser {
		if endpoint.Username == "" {
			return invalid("proxy_auth_unsupported", "username", "SOCKS5 authentication requires a nonempty username")
		}
		method = 2
	}
	if err := writeProbeBytes(connection, []byte{5, 1, method}); err != nil {
		return err
	}
	var negotiation [2]byte
	if _, err := io.ReadFull(connection, negotiation[:]); err != nil {
		return fmt.Errorf("%w: %v", invalid("proxy_handshake_failed", "probe", "SOCKS5 negotiation failed"), err)
	}
	if negotiation[0] != 5 {
		return invalid("proxy_protocol_invalid", "probe", "proxy did not return a SOCKS5 negotiation")
	}
	if negotiation[1] != method {
		return invalid("proxy_auth_failed", "credentials", "proxy does not accept the configured SOCKS5 authentication")
	}
	if method == 2 {
		packet := []byte{1, byte(len(endpoint.Username))}
		packet = append(packet, endpoint.Username...)
		packet = append(packet, byte(len(endpoint.Password)))
		packet = append(packet, endpoint.Password...)
		if err := writeProbeBytes(connection, packet); err != nil {
			return err
		}
		var authentication [2]byte
		if _, err := io.ReadFull(connection, authentication[:]); err != nil {
			return fmt.Errorf("%w: %v", invalid("proxy_handshake_failed", "probe", "SOCKS5 authentication response failed"), err)
		}
		if authentication[0] != 1 || authentication[1] != 0 {
			return invalid("proxy_auth_failed", "credentials", "proxy rejected SOCKS5 authentication")
		}
	}
	packet := []byte{5, 1, 0}
	targetIP := net.ParseIP(targetHost)
	if targetIP == nil && endpoint.Protocol == ProtocolSOCKS5 {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, targetHost)
		if err != nil || len(addresses) == 0 {
			return invalid("proxy_target_dns_failed", "target", "local resolution of the SOCKS5 test target failed")
		}
		targetIP = addresses[0].IP
	}
	if targetIP != nil {
		if ipv4 := targetIP.To4(); ipv4 != nil {
			packet = append(packet, 1)
			packet = append(packet, ipv4...)
		} else {
			packet = append(packet, 4)
			packet = append(packet, targetIP.To16()...)
		}
	} else {
		if len(targetHost) > 255 {
			return invalid("probe_target_invalid", "target", "SOCKS5 test target hostname is too long")
		}
		packet = append(packet, 3, byte(len(targetHost)))
		packet = append(packet, targetHost...)
	}
	packet = append(packet, byte(targetPort>>8), byte(targetPort))
	if err := writeProbeBytes(connection, packet); err != nil {
		return err
	}
	var reply [4]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil {
		return fmt.Errorf("%w: %v", invalid("proxy_handshake_failed", "probe", "SOCKS5 CONNECT response failed"), err)
	}
	if reply[0] != 5 || reply[2] != 0 {
		return invalid("proxy_protocol_invalid", "probe", "SOCKS5 CONNECT response is invalid")
	}
	if reply[1] != 0 {
		return invalid("proxy_tunnel_failed", "probe", fmt.Sprintf("SOCKS5 CONNECT failed with status %d", reply[1]))
	}
	addressBytes := 0
	switch reply[3] {
	case 1:
		addressBytes = 4
	case 4:
		addressBytes = 16
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(connection, length[:]); err != nil {
			return err
		}
		addressBytes = int(length[0])
	default:
		return invalid("proxy_protocol_invalid", "probe", "SOCKS5 CONNECT returned an invalid address type")
	}
	var address [257]byte
	if _, err := io.ReadFull(connection, address[:addressBytes+2]); err != nil {
		return fmt.Errorf("%w: %v", invalid("proxy_handshake_failed", "probe", "SOCKS5 CONNECT response is incomplete"), err)
	}
	return nil
}

func writeProbeBytes(connection net.Conn, buffer []byte) error {
	written, err := connection.Write(buffer)
	if err == nil && written != len(buffer) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("%w: %v", invalid("proxy_handshake_failed", "probe", "proxy handshake write failed"), err)
	}
	return nil
}
