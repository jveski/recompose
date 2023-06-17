package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jveski/recompose/common"
)

func newWebhookHandler(key []byte, signal chan<- struct{}) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/hook", func(w http.ResponseWriter, r *http.Request) {
		hash := hmac.New(sha256.New, key)

		if _, err := io.Copy(hash, r.Body); err != nil {
			w.WriteHeader(400)
			return
		}

		sig := []byte(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
		if !hmac.Equal([]byte(hex.EncodeToString(hash.Sum(nil))), sig) {
			w.WriteHeader(401)
			return
		}

		select {
		case signal <- struct{}{}:
		default:
		}
	})

	return mux
}

func newApiHandler(state inventoryContainer) http.Handler {
	mux := http.NewServeMux()

	// inventoryResponseLock is held while we return an inventory to a node
	// in order to prevent excessive concurrency in cases where many nodes are connected.
	inventoryResponseLock := sync.Mutex{}

	// Get the requesting node's inventory
	mux.HandleFunc("/nodeinventory", func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		var watcher <-chan struct{}
		for {
			if r.Context().Err() != nil {
				w.WriteHeader(400)
				return
			}

			if after != "" && watcher == nil {
				ctx, done := context.WithTimeout(r.Context(), time.Minute*30)
				defer done()
				watcher = state.Watch(ctx)
			}

			state := state.Get()
			nodeinv := state.ByNode[r.URL.Query().Get("fingerprint")]
			if after == "" || (state != nil && state.GitSHA != after) {
				inventoryResponseLock.Lock()
				defer inventoryResponseLock.Unlock()
				toml.NewEncoder(w).Encode(nodeinv)
				return
			}

			if watcher != nil {
				<-watcher
			}
		}
	})

	// Decrypt a secret (in the request body)
	mux.HandleFunc("/decrypt", func(w http.ResponseWriter, r *http.Request) {
		cmd := exec.CommandContext(r.Context(), "age", "--decrypt", "--identity=identity.txt")
		cmd.Stdin = r.Body
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("error while decrypting secret: %s - %s", err, out)
			w.WriteHeader(500)
			return
		}
		w.Write(out[:len(out)-1]) // trim off trailing newline
	})

	return mux
}

func withAuth(inv inventoryContainer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(401)
			return
		}

		fingerprint := common.GetCertFingerprint(r.TLS.PeerCertificates[0].Raw)

		currentInv := inv.Get()
		if currentInv == nil || currentInv.ByNode[fingerprint] == nil {
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

func withLogging(next http.Handler) http.Handler {
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
