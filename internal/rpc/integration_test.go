package rpc

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration(t *testing.T) {
	ctx := context.Background()

	svrCert, svrFprint, err := GenCertificate(t.TempDir())
	require.NoError(t, err)

	cliCert, cliFprint, err := GenCertificate(t.TempDir())
	require.NoError(t, err)

	tests := []struct {
		Name                             string
		Fn                               func(*testing.T, *Client, string)
		Handler                          httprouter.Handle
		AuthorizeClient, AuthorizeServer Authorizer
	}{
		{
			Name: "happy path",
			Fn: func(t *testing.T, cli *Client, addr string) {
				resp, err := cli.GET(ctx, "https://"+addr)
				require.NoError(t, err)
				defer resp.Body.Close()
			},
			Handler: func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
			},
			AuthorizeClient: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == cliFprint
			}),
			AuthorizeServer: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == svrFprint
			}),
		},
		{
			Name: "untrusted client",
			Fn: func(t *testing.T, cli *Client, addr string) {
				e := &ErrUntrustedClient{}
				_, err := cli.GET(ctx, "https://"+addr)
				require.ErrorAs(t, err, &e)
				assert.Equal(t, cliFprint, e.Fingerprint)
			},
			Handler: func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
			},
			AuthorizeClient: AuthorizerFunc(func(fingerprint string) bool {
				return false
			}),
			AuthorizeServer: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == svrFprint
			}),
		},
		{
			Name: "untrusted server",
			Fn: func(t *testing.T, cli *Client, addr string) {
				e := &ErrUntrustedServer{}
				_, err := cli.GET(ctx, "https://"+addr)
				require.ErrorAs(t, err, &e)
				assert.Equal(t, svrFprint, e.Fingerprint)
			},
			Handler: func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
			},
			AuthorizeClient: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == cliFprint
			}),
			AuthorizeServer: AuthorizerFunc(func(fingerprint string) bool {
				return false
			}),
		},
		{
			Name: "50x",
			Fn: func(t *testing.T, cli *Client, addr string) {
				_, err := cli.GET(ctx, "https://"+addr)
				require.EqualError(t, err, "server error status: 502, body: test error")
			},
			Handler: func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
				w.WriteHeader(502)
				w.Write([]byte("test error"))
			},
			AuthorizeClient: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == cliFprint
			}),
			AuthorizeServer: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == svrFprint
			}),
		},
		{
			Name: "20x && != 200",
			Fn: func(t *testing.T, cli *Client, addr string) {
				resp, err := cli.GET(ctx, "https://"+addr)
				require.NoError(t, err)
				defer resp.Body.Close()
			},
			Handler: func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
				w.WriteHeader(204)
			},
			AuthorizeClient: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == cliFprint
			}),
			AuthorizeServer: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == svrFprint
			}),
		},
		{
			Name: "20x && != 200",
			Fn: func(t *testing.T, cli *Client, addr string) {
				cli.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{}

				_, err := cli.GET(ctx, "https://"+addr)
				require.Error(t, err)
			},
			Handler: func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
				w.WriteHeader(204)
			},
			AuthorizeClient: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == cliFprint
			}),
			AuthorizeServer: AuthorizerFunc(func(fingerprint string) bool {
				return fingerprint == svrFprint
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer ln.Close()

			router := httprouter.New()
			router.GET("/", WithAuth(test.AuthorizeClient, test.Handler))
			svr := NewServer("", svrCert, WithLogging(router))
			go svr.ServeTLS(ln, "", "")

			cli := NewClient(cliCert, time.Second, test.AuthorizeServer)

			test.Fn(t, cli, ln.Addr().String())
		})
	}
}
