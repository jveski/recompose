package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"sync"
	"time"

	"github.com/jveski/recompose/internal/concurrency"
	"github.com/jveski/recompose/internal/rpc"
)

func main() {
	var (
		privateAddr        = flag.String("private-addr", ":8123", "address on which to serve the private API (accessed by agents)")
		publicAddr         = flag.String("public-addr", "", "(optional) address on which to serve the public API (i.e. webhooks)")
		gitPollingInterval = flag.Duration("git-polling-interval", time.Minute*5, "how often to `git pull`")
		pprofPort          = flag.Uint("pprof-port", 0, "port to serve default pprof profiling endpoints on or 0 to disable")
		webhookKey         = []byte(os.Getenv("WEBHOOK_HMAC_KEY"))
	)
	flag.Parse()

	if *pprofPort != 0 {
		go func() {
			log.Println(http.ListenAndServe(fmt.Sprintf(":%d", *pprofPort), nil)) // default handler has pprof endpoints when package is imported
		}()
	}

	var (
		webhookSignal = make(chan struct{}, 1)
		state         = &concurrency.StateContainer[*indexedInventory]{}
		nodeStore     = newNodeMetadataStore()
		repoDir       = "./repo"
		agentClient   *rpc.Client
	)

	if err := os.MkdirAll(repoDir, 0755); err != nil {
		log.Fatalf("fatal error while creating git repo directory: %s", err)
	}

	if *publicAddr != "" {
		go func() {
			err := http.ListenAndServe(*publicAddr, rpc.WithLogging(newWebhookHandler(webhookKey, webhookSignal)))
			if err != nil {
				log.Fatalf("fatal error while running public HTTP server: %s", err)
			}
		}()
	}

	cert, _, err := rpc.GenCertificate(".")
	if err != nil {
		log.Fatalf("fatal error while generating certificate: %s", err)
	}

	agentClient = rpc.NewClient(cert, time.Minute*5, rpc.AuthorizerFunc(func(s string) bool {
		current := state.Get()
		return current != nil && current.NodesByFingerprint[s] != nil
	}))

	onSync, synced := block()
	go concurrency.RunLoop(webhookSignal, *gitPollingInterval, time.Minute*30, func() bool {
		err := syncInventory(repoDir, state, nodeStore)
		if err != nil {
			log.Printf("error syncing inventory: %s", err)
		}
		onSync()
		return err == nil
	})

	// wait for initial inventory sync before starting server to ensure any incoming requests are authorized appropriately
	<-synced

	svr := rpc.NewServer(*privateAddr, cert,
		rpc.WithLogging(newApiHandler(state, nodeStore, agentClient)))

	if err := svr.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("fatal error while running private API HTTP server: %s", err)
	}
}

func block() (func(), <-chan struct{}) {
	var (
		ch   = make(chan struct{})
		once = sync.Once{}
	)
	return func() {
		once.Do(func() {
			close(ch)
		})
	}, ch
}
