package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jveski/recompose/common"
)

func main() {
	var (
		privateAddr        = flag.String("private-addr", ":8123", "address on which to serve the private API (accessed by agents)")
		publicAddr         = flag.String("public-addr", "", "(optional) address on which to serve the public API (i.e. webhooks)")
		gitPollingInterval = flag.Duration("git-polling-interval", time.Minute*5, "how often to `git pull`")
		webhookKey         = []byte(os.Getenv("WEBHOOK_HMAC_KEY"))
	)
	flag.Parse()

	var (
		webhookSignal = make(chan struct{}, 1)
		state         = &common.StateContainer[*indexedInventory]{}
		repoDir       = "./repo"
	)

	if err := os.MkdirAll(repoDir, 0755); err != nil {
		log.Fatalf("fatal error while creating git repo directory: %s", err)
	}

	if *publicAddr != "" {
		go func() {
			err := http.ListenAndServe(*publicAddr, withLogging(newWebhookHandler(webhookKey, webhookSignal)))
			if err != nil {
				log.Fatalf("fatal error while running public HTTP server: %s", err)
			}
		}()
	}

	cert, _, err := common.GenCertificate(".")
	if err != nil {
		log.Fatalf("fatal error while generating certificate: %s", err)
	}

	go common.RunLoop(webhookSignal, *gitPollingInterval, time.Minute*30, func() bool {
		err := syncInventory(repoDir, state)
		if err != nil {
			log.Printf("error syncing inventory: %s", err)
		}
		return err == nil
	})

	svr := &http.Server{
		Handler: withLogging(withAuth(state, newApiHandler(state))),
		Addr:    *privateAddr,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAnyClientCert,
		},
	}

	if err := svr.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("fatal error while running private API HTTP server: %s", err)
	}
}
