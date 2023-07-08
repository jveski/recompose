package rpc

import (
	"log"
	"net/http"

	"github.com/julienschmidt/httprouter"
)

type Authorizer interface {
	TrustsCert(fingerprint string) bool
}

func WithAuth(auth Authorizer, next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(401)
			return
		}

		fingerprint := GetCertFingerprint(r.TLS.PeerCertificates[0].Raw)

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
