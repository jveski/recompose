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
