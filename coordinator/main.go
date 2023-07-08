package main

import (
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"github.com/jveski/recompose/internal/concurrency"
	"github.com/jveski/recompose/internal/rpc"
)

func main() {
	var (
		privateAddr        = flag.String("private-addr", ":8123", "address on which to serve the private API (accessed by agents)")
		publicAddr         = flag.String("public-addr", "", "(optional) address on which to serve the public API (i.e. webhooks)")
		gitPollingInterval = flag.Duration("git-polling-interval", time.Minute*5, "how often to `git pull`")
		agentTimeout       = flag.Duration("agent-timeout", time.Second*15, "timeout for requests to agents")
		webhookKey         = []byte(os.Getenv("WEBHOOK_HMAC_KEY"))
	)
	flag.Parse()

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

	// The public server exposes Git webhook endpoints - only served when configured
	if *publicAddr != "" {
		go func() {
			err := http.ListenAndServe(*publicAddr, rpc.WithLogging(newPublicHandler(webhookKey, webhookSignal)))
			if err != nil {
				log.Fatalf("fatal error while running public HTTP server: %s", err)
			}
		}()
	}

	cert, _, err := rpc.GenCertificate(".")
	if err != nil {
		log.Fatalf("fatal error while generating certificate: %s", err)
	}

	// Client used to access agents should only trust known agents as per the inventory
	agentClient = rpc.NewClient(cert, time.Minute*5, &agentAuthorizer{Container: state})

	// Block initialization until the inventory has been sync'd to avoid serving empty an empty inventory.
	err = syncInventory(repoDir, state, nodeStore)
	if err != nil {
		log.Fatalf("error syncing inventory: %s", err)
	}

	// Update inventory async to the HTTP request handlers
	go concurrency.RunLoop(webhookSignal, *gitPollingInterval, time.Minute*30, func() bool {
		err := syncInventory(repoDir, state, nodeStore)
		if err != nil {
			log.Printf("error syncing inventory: %s", err)
		}
		return err == nil
	})

	// This is the main HTTP server that accepts requests to the internal coordination API
	svr := rpc.NewServer(*privateAddr, cert,
		rpc.WithLogging(newApiHandler(state, nodeStore, agentClient, *agentTimeout)))

	if err := svr.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("fatal error while running private API HTTP server: %s", err)
	}
}
