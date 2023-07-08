package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/jveski/recompose/internal/concurrency"
	"github.com/jveski/recompose/internal/rpc"
)

type staticAuthorizer struct {
	Fingerprint string
}

func (s *staticAuthorizer) TrustsCert(fingerprint string) bool { return s.Fingerprint == fingerprint }

func newApiHandler(auth rpc.Authorizer) http.Handler {
	router := httprouter.New()

	router.GET("/ps", rpc.WithAuth(auth, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		cmd := exec.CommandContext(r.Context(), "podman", podmanPsArgs...)
		cmd.Stdout = w
		if err := cmd.Run(); err != nil {
			log.Printf("something went wrong while running `podman ps` on behalf of a client: %s", err)
			return
		}
	}))

	router.GET("/logs", rpc.WithAuth(auth, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		args := []string{"logs"}
		if since := r.URL.Query().Get("since"); since != "" {
			args = append(args, "--since", since)
		}
		args = append(args, r.URL.Query().Get("container"))

		cmd := exec.CommandContext(r.Context(), "podman", args...)
		cmd.Stdout = w
		cmd.Stderr = w
		if err := cmd.Run(); err != nil {
			log.Printf("error starting container log stream: %s", err)
			return
		}
	}))

	return router
}

func register(client *coordClient, ip string, port uint) error {
	form := url.Values{}
	form.Add("ip", ip)
	form.Add("apiport", strconv.Itoa(int(port)))

	// time out the long polling connection after a reasonable period
	ctx, done := context.WithTimeout(context.Background(), concurrency.Jitter(time.Minute*15))
	defer done()

	resp, err := client.POST(ctx, client.BaseURL+"/registernode?"+form.Encode(), nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil // connection recycling timeouts are expected
		}
		return err
	}

	log.Printf("wrote node metadata to coordinator")
	io.Copy(io.Discard, resp.Body)
	return nil
}
