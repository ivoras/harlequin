package webfetch

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// utlsTransport is an http.RoundTripper that performs the TLS handshake with
// uTLS so the ClientHello (JA3) matches Google Chrome, then speaks HTTP/2 or
// HTTP/1.1 depending on ALPN. A fresh connection is dialed per request (this is
// a low-volume fetch tool), with an SSRF guard on the resolved address.
type utlsTransport struct {
	dialer       *net.Dialer
	h2           *http2.Transport
	allowPrivate bool
}

func newUTLSTransport(allowPrivate bool) *utlsTransport {
	return &utlsTransport{
		dialer:       &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
		h2:           &http2.Transport{},
		allowPrivate: allowPrivate,
	}
}

func (t *utlsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("webfetch: unsupported scheme %q (only https)", req.URL.Scheme)
	}
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}

	addr, err := t.safeResolve(req.Context(), host, port)
	if err != nil {
		return nil, err
	}

	rawConn, err := t.dialer.DialContext(req.Context(), "tcp", addr)
	if err != nil {
		return nil, err
	}

	// Chrome JA3 fingerprint; ServerName drives SNI. ALPN advertises h2 and
	// http/1.1 (as Chrome does); the negotiated protocol decides the dispatch.
	uconn := utls.UClient(rawConn, &utls.Config{ServerName: host}, utls.HelloChrome_Auto)
	if err := uconn.HandshakeContext(req.Context()); err != nil {
		rawConn.Close()
		return nil, err
	}

	if uconn.ConnectionState().NegotiatedProtocol == "h2" {
		cc, err := t.h2.NewClientConn(uconn)
		if err != nil {
			uconn.Close()
			return nil, err
		}
		resp, err := cc.RoundTrip(req)
		if err != nil {
			uconn.Close()
			return nil, err
		}
		// One connection per request: close it when the body is done.
		resp.Body = &closingBody{ReadCloser: resp.Body, conn: uconn}
		return resp, nil
	}

	return roundTripHTTP1(uconn, req)
}

// roundTripHTTP1 writes the request and reads the response over an already
// handshaked HTTP/1.1 connection.
func roundTripHTTP1(conn net.Conn, req *http.Request) (*http.Response, error) {
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.Body = &closingBody{ReadCloser: resp.Body, conn: conn}
	return resp, nil
}

// safeResolve resolves host and returns host:port for a public IP, rejecting
// private/loopback/link-local targets (SSRF guard). It pins the dial to the
// resolved IP to avoid DNS rebinding between check and connect.
func (t *utlsTransport) safeResolve(ctx context.Context, host, port string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !t.allowPrivate && privateIP(ip) {
			return "", fmt.Errorf("webfetch: refusing to connect to non-public address %s", host)
		}
		return net.JoinHostPort(host, port), nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if t.allowPrivate || !privateIP(ip) {
			return net.JoinHostPort(ip.String(), port), nil
		}
	}
	return "", fmt.Errorf("webfetch: %s resolves only to non-public addresses", host)
}

// closingBody closes the underlying connection when the response body is closed,
// since each request uses its own connection.
type closingBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b *closingBody) Close() error {
	err := b.ReadCloser.Close()
	b.conn.Close()
	return err
}
