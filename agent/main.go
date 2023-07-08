package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jveski/recompose/common"
	"github.com/jveski/recompose/internal/rpc"
)

func main() {
	var (
		coordinatorAddr        = flag.String("coordinator", "", "host or host:port of the coordination server")
		coordinatorFingerprint = flag.String("coordinator-fingerprint", "", "fingerprint of the coordination server's certificate")
		ip                     = flag.String("ip", "", "optionally override IP used to reach this process from the coordinator")
		port                   = flag.Uint("addr", 8234, "port to serve the agent API on. 0 to disable")
	)
	flag.Parse()

	var (
		inventoryFile = filepath.Join(".", "inventory.toml")
		state         = &common.StateContainer[*common.NodeInventory]{}
		client        = &coordClient{BaseURL: getCoordinatorBaseUrl(*coordinatorAddr)}
	)

	if err := os.MkdirAll("mounts", 0755); err != nil {
		log.Fatalf("fatal error while creating directory: %s", err)
	}

	cert, _, err := rpc.GenCertificate(".")
	if err != nil {
		log.Fatalf("fatal error while generating certificate: %s", err)
	}

	client.Client = common.NewClient(cert, time.Minute*45, func(fingerprint string) bool {
		return fingerprint == *coordinatorFingerprint
	})

	go common.RunLoop(
		state.Watch(context.Background()),
		time.Minute*30, time.Hour,
		func() bool {
			err := syncPodman(client, state)
			if err != nil {
				log.Printf("error syncing podman: %s", err)
			}
			return err == nil
		})

	tightloop := make(chan struct{})
	go func() {
		for {
			tightloop <- struct{}{}
		}
	}()

	go common.RunLoop(tightloop, 0, time.Minute*15, func() bool {
		err := syncInventory(client, inventoryFile, state)
		if err != nil {
			log.Printf("error getting inventory from coordinator: %s", err)
		}
		return err == nil
	})

	go common.RunLoop(tightloop, 0, time.Minute, func() bool {
		ip := *ip
		if ip == "" {
			ip = getOutboundIP().String()
		}
		err := register(client, ip, *port)
		if err != nil {
			log.Printf("error registering node metadata with coordinator: %s", err)
		}
		return err == nil
	})

	svr := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: common.WithLogging(newApiHandler(&staticAuthorizer{Fingerprint: *coordinatorFingerprint})),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAnyClientCert,
			MinVersion:   tls.VersionTLS12,
		},
	}

	if err := svr.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("fatal error while running API HTTP server: %s", err)
	}
}

type coordClient struct {
	*http.Client
	BaseURL string
}

func getCoordinatorBaseUrl(addr string) string {
	l := strings.Split(addr, ":")
	if len(l) >= 2 {
		return "https://" + addr
	}
	return fmt.Sprintf("https://%s:%d", addr, 8123)
}

func getOutboundIP() net.IP {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		log.Fatalf("unable to determine outbound IP address: %s", err)
	}
	conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP
}
