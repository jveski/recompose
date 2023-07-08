package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/jveski/recompose/internal/api"
	"github.com/jveski/recompose/internal/concurrency"
	"github.com/jveski/recompose/internal/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebhookHappyPath(t *testing.T) {
	testKey := []byte("test key")
	signal := make(chan struct{}, 1)
	fn := newWebhookHandler(testKey, signal)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", bytes.NewBufferString("test123"))
	r.Header.Set("X-Hub-Signature-256", "sha256=5cf4ccad5951e3c0de540fbad18c940f7dbdd85b37b4c6491f4105bb7ff9063e")
	fn(w, r, httprouter.Params{})
	assert.Equal(t, 200, w.Code)

	<-signal
}

func TestWebhook401(t *testing.T) {
	testKey := []byte("test invalidkey")
	signal := make(chan struct{}, 1)
	fn := newWebhookHandler(testKey, signal)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", bytes.NewBufferString("test123"))
	r.Header.Set("X-Hub-Signature-256", "sha256=5cf4ccad5951e3c0de540fbad18c940f7dbdd85b37b4c6491f4105bb7ff9063e")
	fn(w, r, httprouter.Params{})
	assert.Equal(t, 401, w.Code)
}

func TestGetNodeInventoryHappyPath(t *testing.T) {
	state := &concurrency.StateContainer[*indexedInventory]{}

	state.Swap(&indexedInventory{
		ClientsByFingerprint: map[string]struct{}{},
		NodesByFingerprint: map[string]*api.NodeInventory{
			"test": {GitSHA: "test-sha"},
		},
	})

	fn := newGetNodeInventoryHandler(state)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/?fingerprint=test", nil)
	fn(w, r, httprouter.Params{})
	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "test-sha")
}

func TestRegisterNode(t *testing.T) {
	store := newNodeMetadataStore()
	fn := newRegisterNodeHandler(store)

	ctx, done := context.WithCancel(context.Background())
	done()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/?fingerprint=test1&apiport=123&ip=234", nil)
	r = r.WithContext(ctx)
	fn(w, r, httprouter.Params{})
	assert.Equal(t, 200, w.Code)

	actual := store.Get("test1")
	require.NotNil(t, actual)
	assert.Equal(t, uint(123), actual.APIPort)
	assert.Equal(t, "234", actual.IP)
}

func TestGetStatusHappyPath(t *testing.T) {
	svr := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{
			"Names": ["test1"],
			"ExitedAt": 123,
			"Created": 234
		}]`))
	}))
	defer svr.Close()

	u, err := url.Parse(svr.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	store := newNodeMetadataStore()
	store.Set("test", &nodeMetadata{
		Fingerprint: "test-fingerprint",
		IP:          "127.0.0.1",
		APIPort:     uint(port),
	})

	client := &rpc.Client{Client: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	fn := newGetStatusHandler(store, client, time.Second*10)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	fn(w, r, httprouter.Params{})
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "{\"containers\":[{\"name\":\"test1\",\"nodeFingerprint\":\"test-fingerprint\",\"created\":\"1970-01-01T00:03:54Z\",\"lastRestart\":\"1970-01-01T00:02:03Z\"}]}\n", w.Body.String())
}

func TestGetStatusTimeout(t *testing.T) {
	svr := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer svr.Close()

	u, err := url.Parse(svr.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	store := newNodeMetadataStore()
	store.Set("test", &nodeMetadata{
		Fingerprint: "test-fingerprint",
		IP:          "127.0.0.1",
		APIPort:     uint(port),
	})

	client := &rpc.Client{Client: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	fn := newGetStatusHandler(store, client, time.Millisecond)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	fn(w, r, httprouter.Params{})
	assert.Equal(t, 206, w.Code)
}
