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
	"github.com/jveski/recompose/internal/api"
	"github.com/jveski/recompose/internal/rpc"
)

func newPublicHandler(hookKey []byte, hookSignal chan<- struct{}) http.Handler {
	router := httprouter.New()
	router.POST("/hook", newWebhookHandler(hookKey, hookSignal))
	return router
}

func newApiHandler(state inventoryContainer, nodeStore *nodeMetadataStore, client *rpc.Client, statusTimeout time.Duration) http.Handler {
	var (
		router     = httprouter.New()
		agentAuth  = &agentAuthorizer{Container: state}
		clientAuth = &clientAuthorizer{Container: state}
	)

	router.GET("/nodeinventory", rpc.WithAuth(agentAuth, newGetNodeInventoryHandler(state)))
	router.POST("/decrypt", rpc.WithAuth(agentAuth, newDecryptHandler()))
	router.POST("/registernode", rpc.WithAuth(agentAuth, newRegisterNodeHandler(nodeStore)))
	router.GET("/nodes/:fingerprint/logs", rpc.WithAuth(clientAuth, newProxyHandler(nodeStore, client, "/logs")))
	router.GET("/status", rpc.WithAuth(clientAuth, newGetStatusHandler(nodeStore, client, statusTimeout)))

	return router
}

func newWebhookHandler(key []byte, signal chan<- struct{}) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		hash := hmac.New(sha256.New, key)
		io.Copy(hash, r.Body)

		sig := []byte(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
		if !hmac.Equal([]byte(hex.EncodeToString(hash.Sum(nil))), sig) {
			w.WriteHeader(401)
			return
		}

		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

func newGetNodeInventoryHandler(state inventoryContainer) httprouter.Handle {
	// inventoryResponseLock is held while we return an inventory to a node
	// in order to prevent excessive concurrency in cases where many nodes are connected.
	inventoryResponseLock := sync.Mutex{}

	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
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
	}
}

func newDecryptHandler() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		cmd := exec.CommandContext(r.Context(), "age", "--decrypt", "--identity=identity.txt")
		cmd.Stdin = r.Body
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("error while decrypting secret: %s - %s", err, out)
			w.WriteHeader(500)
			return
		}
		w.Write(out[:len(out)-1]) // trim off trailing newline
	}
}

func newRegisterNodeHandler(store *nodeMetadataStore) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		q := r.URL.Query()
		fingerprint := q.Get("fingerprint")
		apiport, _ := strconv.Atoi(q.Get("apiport"))
		meta := &nodeMetadata{
			Fingerprint: fingerprint,
			IP:          q.Get("ip"),
			APIPort:     uint(apiport),
		}
		store.Set(fingerprint, meta)
		log.Printf("received metadata for node: %s - ip=%s apiport=%d", fingerprint, meta.IP, meta.APIPort)

		<-r.Context().Done()
	}
}

func newProxyHandler(store *nodeMetadataStore, client *rpc.Client, upstreamPath string) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		fingerprint := p.ByName("fingerprint")

		metadata := store.Get(fingerprint)
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
		proxy.Transport = client.Transport
		proxy.ServeHTTP(w, r)
	}
}

func newGetStatusHandler(store *nodeMetadataStore, client *rpc.Client, timeout time.Duration) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		resp := &api.ClusterState{}

		var partial bool
		for _, node := range store.List() {
			containers, err := getAgentStatus(r.Context(), client, timeout, node)
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
	}
}

func getAgentStatus(ctx context.Context, client *rpc.Client, timeout time.Duration, node *nodeMetadata) ([]*api.ContainerState, error) {
	ctx, done := context.WithTimeout(ctx, timeout)
	defer done()

	resp, err := client.GET(ctx, fmt.Sprintf("https://%s:%d/ps", node.IP, node.APIPort))
	if err != nil {
		return nil, err
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

	states := make([]*api.ContainerState, len(body))
	for i, raw := range body {
		state := &api.ContainerState{
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
