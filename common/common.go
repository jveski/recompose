package common

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	mathrand "math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func RunLoop(signal <-chan struct{}, resync, maxRetry time.Duration, fn func() bool) {
	ch := make(chan struct{}, 1)
	ch <- struct{}{} // initial sync

	go func() {
		for range signal {
			ch <- struct{}{}
		}
		close(ch)
	}()

	if resync > 0 {
		go func() {
			timer := time.NewTicker(jitter(resync))
			for range timer.C {
				select {
				case ch <- struct{}{}:
				default:
				}
				timer.Reset(jitter(resync))
			}
		}()
	}

	attempt := func() {
		var lastRetry time.Duration
		for {
			if fn() {
				break
			}

			if lastRetry == 0 {
				lastRetry = time.Millisecond * 50
			}
			lastRetry += lastRetry / 8
			if lastRetry > maxRetry {
				lastRetry = maxRetry
			}

			time.Sleep(jitter(lastRetry))
		}
	}

	for range ch {
		attempt()
		time.Sleep(jitter(time.Millisecond * 100)) // cooldown
	}
}

func jitter(duration time.Duration) time.Duration {
	maxJitter := int64(duration) * int64(5) / 100 // 5% jitter
	return duration + time.Duration(mathrand.Int63n(maxJitter*2)-maxJitter)
}

type StateContainer[T any] struct {
	lock     sync.Mutex
	current  T
	watchers map[any]chan struct{}
}

func (s *StateContainer[T]) Get() T {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.current
}

func (s *StateContainer[T]) Swap(val T) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.current = val
	s.bumpUnlocked()
}

func (s *StateContainer[T]) ReEnter() {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.bumpUnlocked()
}

func (s *StateContainer[T]) bumpUnlocked() {
	for _, ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *StateContainer[T]) Watch(ctx context.Context) <-chan struct{} {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.watchers == nil {
		s.watchers = map[any]chan struct{}{}
	}

	ch := make(chan struct{}, 1)
	go func() {
		<-ctx.Done()

		s.lock.Lock()
		defer s.lock.Unlock()

		delete(s.watchers, ctx)
		close(ch)
	}()

	s.watchers[ctx] = ch
	return ch
}

func GetCertFingerprint(cert []byte) string {
	certHash := sha256.Sum256(cert)
	return hex.EncodeToString(certHash[:])
}

// GenCertificate generates a new TLS certificate or loads it from disk.
// The fingerprint text file will be regenerated if it's missing.
// The cert will be regenerated if it and/or the private key are invalid or missing.
func GenCertificate(dir string) (tls.Certificate, string /* fingerprint */, error) {
	dir = filepath.Join(dir, "tls")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return tls.Certificate{}, "", err
	}

	var (
		certFile        = filepath.Join(dir, "cert.pem")
		keyFile         = filepath.Join(dir, "cert-private-key.pem")
		fingerprintFile = filepath.Join(dir, "cert-fingerprint.txt")
	)

	certObj, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err == nil {
		certObj.Leaf, err = x509.ParseCertificate(certObj.Certificate[0])
		if err != nil {
			return certObj, "", err
		}

		if fingerprint, err := os.ReadFile(fingerprintFile); err == nil {
			return certObj, string(fingerprint), nil
		}

		fingerprint := GetCertFingerprint(certObj.Leaf.Raw)
		if err := os.WriteFile(fingerprintFile, []byte(fingerprint), 0644); err != nil {
			return certObj, "", fmt.Errorf("writing fingerprint: %w", err)
		}
		return certObj, fingerprint, nil
	}

	cert, key, err := genCert()
	if err != nil {
		return certObj, "", err
	}

	if err := os.WriteFile(certFile, cert, 0644); err != nil {
		return certObj, "", fmt.Errorf("writing cert: %w", err)
	}
	if err := os.WriteFile(keyFile, key, 0644); err != nil {
		return certObj, "", fmt.Errorf("writing key: %w", err)
	}

	certObj, err = tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return certObj, "", err
	}

	certObj.Leaf, err = x509.ParseCertificate(certObj.Certificate[0])
	if err != nil {
		return certObj, "", err
	}

	fingerprint := GetCertFingerprint(certObj.Leaf.Raw)
	if err := os.WriteFile(fingerprintFile, []byte(fingerprint), 0644); err != nil {
		return certObj, "", fmt.Errorf("writing fingerprint: %w", err)
	}

	return certObj, fingerprint, err
}

func genCert() ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "recompose"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour * 24 * 3650),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}

	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPem, keyPem, nil
}

type Authorizer interface {
	TrustsCert(fingerprint string) bool
}

func WithAuth(auth Authorizer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(401)
			return
		}

		fingerprint := GetCertFingerprint(r.TLS.PeerCertificates[0].Raw)

		if auth == nil || auth.TrustsCert(fingerprint) {
			w.WriteHeader(403)
			return
		}

		// This is a hack to pass the fingerprint to handlers because I don't feel like using context values
		q := r.URL.Query()
		q.Set("fingerprint", fingerprint)
		r.URL.RawQuery = q.Encode()

		next.ServeHTTP(w, r)
	})
}

func WithLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wp := &responseProxy{ResponseWriter: w}
		next.ServeHTTP(wp, r)
		log.Printf("%s %s - %d (%s)", r.Method, r.URL, wp.Status, r.RemoteAddr)
	})
}

// responseProxy is an annoying necessity to retain the response status for logging purposes.
type responseProxy struct {
	http.ResponseWriter
	Status int
}

func (r *responseProxy) WriteHeader(status int) {
	r.Status = status
	r.ResponseWriter.WriteHeader(status)
}
