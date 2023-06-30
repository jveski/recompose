package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/julienschmidt/httprouter"
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

func newApiHandler(state inventoryContainer, nodeStore *nodeMetadataStore, agentClient *http.Client) http.Handler {
	router := httprouter.New()

	// inventoryResponseLock is held while we return an inventory to a node
	// in order to prevent excessive concurrency in cases where many nodes are connected.
	inventoryResponseLock := sync.Mutex{}

	// Get the requesting node's inventory
	router.GET("/nodeinventory", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
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
	router.POST("/decrypt", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
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

	// Register a node's ephemeral metadata
	router.POST("/registernode", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		fingerprint := r.URL.Query().Get("fingerprint")
		apiport, _ := strconv.Atoi(r.URL.Query().Get("apiport"))
		meta := &nodeMetadata{
			IP:      r.URL.Query().Get("ip"),
			APIPort: uint(apiport),
		}
		nodeStore.Set(fingerprint, meta)
		log.Printf("received metadata for node: %s - ip=%s apiport=%d", fingerprint, meta.IP, meta.APIPort)

		flusher := w.(common.WrappedResponseWriter).Unwrap().(http.Flusher)
		flusher.Flush()
		<-r.Context().Done()
	})

	// Proxy to agent APIs
	router.GET("/nodes/:fingerprint/logs", newProxyHandler(nodeStore, agentClient, "/logs"))
	router.GET("/nodes/:fingerprint/status", newProxyHandler(nodeStore, agentClient, "/status"))

	return router
}

func newProxyHandler(nodeStore *nodeMetadataStore, agentClient *http.Client, upstreamPath string) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		fingerprint := p.ByName("fingerprint")

		metadata := nodeStore.Get(fingerprint)
		if metadata == nil || metadata.APIPort == 0 {
			http.Error(w, "node with the given fingerprint is not known", 400)
			return
		}

		r.URL.Path = upstreamPath

		upstream := &url.URL{
			Scheme: "https",
			Host:   fmt.Sprintf("%s:%d", metadata.IP, metadata.APIPort),
		}
		proxy := httputil.NewSingleHostReverseProxy(upstream)
		proxy.Transport = agentClient.Transport
		proxy.ServeHTTP(w, r)
	}
}
