package common

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log"
	mathrand "math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/jveski/recompose/internal/rpc"
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
			timer := time.NewTicker(Jitter(resync))
			for range timer.C {
				select {
				case ch <- struct{}{}:
				default:
				}
				timer.Reset(Jitter(resync))
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

			time.Sleep(Jitter(lastRetry))
		}
	}

	for range ch {
		attempt()
		time.Sleep(Jitter(time.Millisecond * 100)) // cooldown
	}
}

func Jitter(duration time.Duration) time.Duration {
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

type Authorizer interface {
	TrustsCert(fingerprint string) bool
}

func WithAuth(auth Authorizer, next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(401)
			return
		}

		fingerprint := rpc.GetCertFingerprint(r.TLS.PeerCertificates[0].Raw)

		if auth == nil || !auth.TrustsCert(fingerprint) {
			w.WriteHeader(403)
			return
		}

		// This is a hack to pass the fingerprint to handlers because I don't feel like using context values
		q := r.URL.Query()
		q.Set("fingerprint", fingerprint)
		r.URL.RawQuery = q.Encode()

		next(w, r, ps)
	}
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

func (r *responseProxy) Unwrap() http.ResponseWriter { return r.ResponseWriter }

type WrappedResponseWriter interface {
	Unwrap() http.ResponseWriter
}

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
						if authhook(rpc.GetCertFingerprint(cert)) {
							return nil
						}
					}
					e := &ErrUntrustedServer{}
					if len(rawCerts) > 0 {
						e.Fingerprint = rpc.GetCertFingerprint(rawCerts[0])
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
