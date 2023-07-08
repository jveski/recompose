package rpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// UrlPrefix returns the base URL used to reach the given coordinator host.
// The host can be specified as `hostname` or `hostname:port`.
// If port is not given, it will default to 8123 - the default listener port.
func UrlPrefix(host string) string {
	chunks := strings.Split(host, ":")
	if len(chunks) == 1 {
		return "https://" + chunks[0] + ":8123"
	} else {
		return "https://" + chunks[0] + ":" + chunks[1]
	}
}

type Client struct {
	*http.Client
}

func NewClient(cert tls.Certificate, timeout time.Duration, auth Authorizer) *Client {
	return &Client{
		Client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSHandshakeTimeout: time.Second * 15,
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, // this is safe because we verify the fingerprint in VerifyPeerCertificate
					Certificates:       []tls.Certificate{cert},
					VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
						for _, cert := range rawCerts {
							if auth.TrustsCert(GetCertFingerprint(cert)) {
								return nil
							}
						}

						return &ErrUntrustedServer{Fingerprint: GetCertFingerprint(rawCerts[0])}
					},
				},
			},
		},
	}
}

func (c *Client) GET(ctx context.Context, url string) (*http.Response, error) {
	return c.do(ctx, "GET", url, nil)
}

func (c *Client) POST(ctx context.Context, url string, body io.Reader) (*http.Response, error) {
	return c.do(ctx, "POST", url, body)
}

func (c *Client) do(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == 403 {
		defer resp.Body.Close()
		t := c.Transport.(*http.Transport)
		return nil, &ErrUntrustedClient{Fingerprint: GetCertFingerprint(t.TLSClientConfig.Certificates[0].Leaf.Raw)}
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server error status: %d, body: %s", resp.StatusCode, body)
	}

	return resp, nil
}

func NewServer(addr string, cert tls.Certificate, handler http.Handler) *http.Server {
	return &http.Server{
		Handler: WithLogging(handler),
		Addr:    addr,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAnyClientCert,
			MinVersion:   tls.VersionTLS12,
		},
	}
}

type ErrUntrustedServer struct {
	Fingerprint string
}

func (e *ErrUntrustedServer) Error() string { return "untrusted server certificate" }

type ErrUntrustedClient struct {
	Fingerprint string
}

func (e *ErrUntrustedClient) Error() string { return "server does not trust this client" }
