package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/jveski/recompose/internal/rpc"
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
	var (
		router     = httprouter.New()
		agentAuth  = &agentAuthorizer{Container: state}
		clientAuth = &clientAuthorizer{Container: state}
	)

	// inventoryResponseLock is held while we return an inventory to a node
	// in order to prevent excessive concurrency in cases where many nodes are connected.
	inventoryResponseLock := sync.Mutex{}

	// Get the requesting node's inventory
	router.GET("/nodeinventory", rpc.WithAuth(agentAuth, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		q := r.URL.Query()
		after := q.Get("after")
		ctx := r.Context()

		var watcher <-chan struct{}
		for {
			if ctx.Err() != nil {
				w.WriteHeader(400)
				return
			}

			if after != "" && watcher == nil {
				var done context.CancelFunc
				ctx, done = context.WithTimeout(ctx, time.Minute*30)
				defer done()
				watcher = state.Watch(ctx)
			}

			state := state.Get()
			nodeinv := state.NodesByFingerprint[q.Get("fingerprint")]
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
	}))

	// Decrypt a secret (in the request body)
	router.POST("/decrypt", rpc.WithAuth(agentAuth, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		cmd := exec.CommandContext(r.Context(), "age", "--decrypt", "--identity=identity.txt")
		cmd.Stdin = r.Body
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("error while decrypting secret: %s - %s", err, out)
			w.WriteHeader(500)
			return
		}
		w.Write(out[:len(out)-1]) // trim off trailing newline
	}))

	// Register a node's ephemeral metadata
	router.POST("/registernode", rpc.WithAuth(agentAuth, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		q := r.URL.Query()
		fingerprint := q.Get("fingerprint")
		apiport, _ := strconv.Atoi(q.Get("apiport"))
		meta := &nodeMetadata{
			Fingerprint: fingerprint,
			IP:          q.Get("ip"),
			APIPort:     uint(apiport),
		}
		nodeStore.Set(fingerprint, meta)
		log.Printf("received metadata for node: %s - ip=%s apiport=%d", fingerprint, meta.IP, meta.APIPort)

		flusher := w.(rpc.WrappedResponseWriter).Unwrap().(http.Flusher)
		flusher.Flush()
		<-r.Context().Done()
	}))

	// Proxy to agent APIs
	router.GET("/nodes/:fingerprint/logs", rpc.WithAuth(clientAuth, newProxyHandler(nodeStore, agentClient, "/logs")))

	// Get the status of the entire cluster
	router.GET("/status", rpc.WithAuth(clientAuth, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		resp := &common.ClusterState{}

		var partial bool
		for _, node := range nodeStore.List() {
			containers, err := getAgentStatus(r.Context(), agentClient, node)
			if err != nil {
				log.Printf("error while getting agent status: %s", err)
				partial = true
				continue
			}
			resp.Containers = append(resp.Containers, containers...)
		}

		if partial {
			w.WriteHeader(206)
		}
		json.NewEncoder(w).Encode(resp)
	}))

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

func getAgentStatus(ctx context.Context, agentClient *http.Client, node *nodeMetadata) ([]*common.ContainerState, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://%s:%d/ps", node.IP, node.APIPort), nil)
	if err != nil {
		return nil, err
	}

	resp, err := agentClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}
	defer resp.Body.Close()

	body := []struct {
		Names    []string
		ExitedAt int64
		Created  int64
	}{}
	err = json.NewDecoder(resp.Body).Decode(&body)
	if err != nil {
		return nil, err
	}

	states := make([]*common.ContainerState, len(body))
	for i, raw := range body {
		state := &common.ContainerState{
			Name:            raw.Names[0],
			NodeFingerprint: node.Fingerprint,
			Created:         time.Unix(raw.Created, 0),
		}
		states[i] = state

		if raw.ExitedAt > 0 {
			exited := time.Unix(raw.ExitedAt, 0)
			state.LastRestart = &exited
		}
	}

	return states, nil
}

type agentAuthorizer struct {
	Container inventoryContainer
}

func (a *agentAuthorizer) TrustsCert(fingerprint string) bool {
	state := a.Container.Get()
	return state != nil && state.NodesByFingerprint[fingerprint] != nil
}

type clientAuthorizer struct {
	Container inventoryContainer
}

func (a *clientAuthorizer) TrustsCert(fingerprint string) bool {
	state := a.Container.Get()
	if state == nil {
		return false
	}
	_, ok := state.ClientsByFingerprint[fingerprint]
	return ok
}
