package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/jveski/recompose/internal/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	assert.Equal(t, "{\"containers\":[{\"name\":\"test1\",\"nodeFingerprint\":\"test-fingerprint\",\"created\":\"1969-12-31T18:03:54-06:00\",\"lastRestart\":\"1969-12-31T18:02:03-06:00\"}]}\n", w.Body.String())
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
