package main

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
	"github.com/jveski/recompose/internal/concurrency"
	"github.com/jveski/recompose/internal/rpc"
)

func newApiHandler(auth rpc.Authorizer) http.Handler {
	router := httprouter.New()

	router.GET("/ps", rpc.WithAuth(auth, func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		// Map container hash -> `podman ps` output
		pso, err := podmanPs()
		if err != nil {
			log.Printf("error while running `podman ps`: %s", err)
			http.Error(w, "internal error", 500)
			return
		}
		psByHash := map[string]*psOutput{}
		for _, ps := range pso {
			if ps.Labels == nil {
				continue
			}
			psByHash[ps.Labels["recomposeHash"]] = ps
		}

		// Get current control plane state of each container
		stateFiles, err := os.ReadDir("state")
		if err != nil {
			log.Printf("error while listing state files: %s", err)
			http.Error(w, "internal error", 500)
			return
		}

		// Parse files and merge in the `podman ps` output
		cw := csv.NewWriter(w)
		for _, file := range stateFiles {
			buf, err := os.ReadFile(filepath.Join("state", file.Name()))
			if err != nil {
				// We crash here to avoid buffering the response.
				// Returning a 50x at this point is not possible, so without a buffer clients would happily accept partial results.
				// It should not be possible to reach this code unless the filesystem is broken.
				log.Fatalf("error while reading state file: %s", err)
				return
			}

			spl := strings.SplitN(string(buf), "\n", 3)
			if len(spl) < 3 {
				continue // corrupted
			}

			hash := strings.TrimSuffix(file.Name(), ".txt")
			ps := psByHash[hash]
			if ps != nil {
				spl = append(spl, strconv.FormatInt(ps.Created, 10), strconv.FormatInt(ps.StartedAt, 10))
			} else {
				spl = append(spl, "", "") // maintain expected number of rows
			}

			cw.Write(spl)
		}
		cw.Flush()
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
