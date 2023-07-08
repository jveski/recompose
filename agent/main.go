package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/jveski/recompose/internal/api"
	"github.com/jveski/recompose/internal/concurrency"
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
		state         = &concurrency.StateContainer[*api.NodeInventory]{}
		client        = &coordClient{BaseURL: rpc.UrlPrefix(*coordinatorAddr)}
	)

	if err := os.MkdirAll("mounts", 0755); err != nil {
		log.Fatalf("fatal error while creating directory: %s", err)
	}

	cert, _, err := rpc.GenCertificate(".")
	if err != nil {
		log.Fatalf("fatal error while generating certificate: %s", err)
	}

	// The client used to access the coordinator API only trusts the known server cert fingerprint
	coordAuth := rpc.TrustOneCert(*coordinatorFingerprint)
	client.Client = rpc.NewClient(cert, time.Minute*45, coordAuth)

	// Podman is sync'd periodically and when the inventory state changes
	go concurrency.RunLoop(
		state.Watch(context.Background()),
		time.Minute*30, time.Hour,
		func() bool {
			err := syncPodman(client, state)
			if err != nil {
				log.Printf("error syncing podman: %s", err)
			}
			return err == nil
		})

	// The inventory is retrieved from the coordinator in a loop using long polling
	go concurrency.RunLoop(nil, 0, time.Minute*15, func() bool {
		err := syncInventory(client, inventoryFile, state)
		if err != nil {
			log.Printf("error getting inventory from coordinator: %s", err)
		}
		return err == nil
	})

	// Agents register their internal API endpoints with the coordinator in a loop using long polling.
	// The long polling approach allows them to more quickly re-register when coordinators become available
	// while generating minimal request volume during steady state operation.
	go concurrency.RunLoop(nil, 0, time.Minute, func() bool {
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

	// This server exposes information to the coordinator about the current state of containers managed by this agent.
	svr := rpc.NewServer(
		fmt.Sprintf(":%d", *port), cert,
		rpc.WithLogging(newApiHandler(coordAuth)))

	if err := svr.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("fatal error while running API HTTP server: %s", err)
	}
}

type coordClient struct {
	*rpc.Client
	BaseURL string
}

func getOutboundIP() net.IP {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		log.Fatalf("unable to determine outbound IP address: %s", err)
	}
	conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP
}
