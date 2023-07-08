package rpc

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"
)

// TODO: Use Authorizer interface
func NewClient(cert tls.Certificate, timeout time.Duration, authhook func(string) bool) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSHandshakeTimeout: time.Second * 15,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // this is safe because we verify the fingerprint in VerifyPeerCertificate
				Certificates:       []tls.Certificate{cert},
				VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
					for _, cert := range rawCerts {
						if authhook(GetCertFingerprint(cert)) {
							return nil
						}
					}

					e := &ErrUntrustedServer{}
					if len(rawCerts) > 0 {
						e.Fingerprint = GetCertFingerprint(rawCerts[0])
					} else {
						e.Fingerprint = "unknown"
					}
					return e
				},
			},
		},
	}
}

type ErrUntrustedServer struct {
	Fingerprint string
}

func (e *ErrUntrustedServer) Error() string { return "untrusted server certificate" }
