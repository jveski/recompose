package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/jveski/recompose/common"
)

type staticAuthorizer struct {
	Fingerprint string
}

func (s *staticAuthorizer) TrustsCert(fingerprint string) bool { return s.Fingerprint == fingerprint }

func newApiHandler(auth common.Authorizer) http.Handler {
	router := httprouter.New()

	router.GET("/ps", common.WithAuth(auth, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		cmd := exec.CommandContext(r.Context(), "podman", podmanPsArgs...)
		cmd.Stdout = w
		if err := cmd.Run(); err != nil {
			log.Printf("something went wrong while running `podman ps` on behalf of a client: %s", err)
			return
		}
	}))

	router.GET("/logs", common.WithAuth(auth, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		args := []string{"logs"}
		if since := r.URL.Query().Get("since"); since != "" {
			args = append(args, "--since", since)
		}
		args = append(args, r.URL.Query().Get("container"))

		cmd := exec.CommandContext(r.Context(), "podman", args...)
		pipe, err := cmd.StderrPipe()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		cmd.Stdout = cmd.Stderr // merge stdout and stderr

		flusher := w.(common.WrappedResponseWriter).Unwrap().(http.Flusher)
		flusher.Flush()

		scan := bufio.NewScanner(pipe)
		if err := cmd.Start(); err != nil {
			log.Printf("error starting container log stream: %s", err)
			return
		}

		// Flush each line out to the client separately
		for scan.Scan() {
			_, err := w.Write(append(scan.Bytes(), '\n'))
			if errors.Is(err, io.EOF) {
				flusher.Flush()
				break
			}
			if err != nil {
				log.Printf("error sending container logs to client: %s", err)
				return
			}
			flusher.Flush()
		}

		cmd.Wait()
	}))

	return router
}

func register(client *coordClient, ip string, port uint) error {
	form := url.Values{}
	form.Add("ip", ip)
	form.Add("apiport", strconv.Itoa(int(port)))

	// time out the long polling connection after a reasonable period
	ctx, done := context.WithTimeout(context.Background(), common.Jitter(time.Minute*15))
	defer done()

	req, err := http.NewRequestWithContext(ctx, "POST", client.BaseURL+"/registernode?"+form.Encode(), nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil // connection recycling timeouts are expected
		}
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status from server: %d", resp.StatusCode)
	}

	log.Printf("wrote node metadata to coordinator")
	io.Copy(io.Discard, resp.Body)
	return nil
}
