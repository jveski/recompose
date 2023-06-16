package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jveski/recompose/common"
)

func main() {
	var (
		coordinatorAddr        = flag.String("coordinator", "", "host or host:port of the coordination server")
		coordinatorFingerprint = flag.String("coordinator-fingerprint", "", "fingerprint of the coordination server's certificate")
	)
	flag.Parse()

	var (
		inventoryFile = filepath.Join(".", "inventory.toml")
		state         = &common.StateContainer[*common.NodeInventory]{}
		client        = &coordClient{BaseURL: getCoordinatorBaseUrl(*coordinatorAddr)}
	)

	cert, _, err := common.GenCertificate(".")
	if err != nil {
		log.Fatalf("fatal error while generating certificate: %s", err)
	}

	client.Client = &http.Client{
		Timeout: time.Minute * 40,
		Transport: &http.Transport{
			TLSHandshakeTimeout: time.Second * 10,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // this is safe because we verify the fingerprint in VerifyPeerCertificate
				Certificates:       []tls.Certificate{cert},
				VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
					for _, cert := range rawCerts {
						if common.GetCertFingerprint(cert) == *coordinatorFingerprint {
							return nil
						}
					}
					return errors.New("fingerprint is not trusted")
				},
			},
		},
	}

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

	common.RunLoop(tightloop, 0, time.Minute*15, func() bool {
		err := syncInventory(client, inventoryFile, state)
		if err != nil {
			log.Printf("error getting inventory from coordinator: %s", err)
		}
		return err == nil
	})
}

func getCoordinatorBaseUrl(addr string) string {
	l := strings.Split(addr, ":")
	if len(l) >= 2 {
		return "https://" + addr
	}
	return fmt.Sprintf("https://%s:%d", addr, 8123)
}

type coordClient struct {
	*http.Client
	BaseURL string
}

type inventoryContainer = *common.StateContainer[*common.NodeInventory]

func syncInventory(client *coordClient, file string, state inventoryContainer) error {
	current := state.Get()
	if current == nil {
		current = &common.NodeInventory{}
		if _, err := toml.DecodeFile(file, current); err != nil {
			log.Printf("warning: failed to read the last seen git sha from disk: %s", err)
		}
		state.Swap(current)
	}

	resp, err := client.Get(fmt.Sprintf("%s/nodeinventory?after=%s", client.BaseURL, current.GitSHA))
	if err != nil {
		return fmt.Errorf("requesting inventory from coordinator: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		return fmt.Errorf("the coordinator does not trust your cert - add it to cluster.toml")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("server error status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("downloading inventory from coordinator: %w", err)
	}

	if err := os.WriteFile(file, body, 0644); err != nil {
		return fmt.Errorf("writing inventory file: %w", err)
	}

	inv := &common.NodeInventory{}
	if _, err := toml.Decode(string(body), inv); err != nil {
		return fmt.Errorf("decoding inventory: %w", err)
	}

	log.Printf("got inventory from coordinator at git SHA: %s", inv.GitSHA)
	state.Swap(inv)
	return nil
}

func syncPodman(client *coordClient, state inventoryContainer) error {
	current := state.Get()
	if current == nil {
		return nil // nothing to do yet
	}

	goalIndex := map[string]*common.ContainerSpec{}
	for _, container := range current.Containers {
		goalIndex[container.Hash] = container
	}

	existing, err := podmanPs()
	if err != nil {
		return fmt.Errorf("getting current podman state: %s", err)
	}

	existingIndex := map[string]*psOutput{}
	for _, c := range existing {
		hash := c.Labels["recomposeHash"]
		existingIndex[hash] = c
	}

	// Remove orphaned containers
	for _, c := range existingIndex { // random map iteration is important here to avoid deadlocks
		var (
			name = c.Names[0]
			hash = c.Labels["recomposeHash"]
		)
		if hash != "" && goalIndex[hash] != nil {
			continue // still exists in inventory
		}

		log.Printf("removing container %q...", name)
		if err := podmanRm(name); err != nil {
			return fmt.Errorf("removing container %q: %s", name, err)
		}

		log.Printf("removed container %q", name)
		state.ReEnter()
		return nil
	}

	// Start missing containers
	for _, c := range goalIndex {
		if e, ok := existingIndex[c.Hash]; ok {
			if !e.Exited {
				continue // already running
			}

			// the container has stopped somehow - restart it
			log.Printf("restarting exited container %q...", c.Name)
			out, err := exec.Command("podman", "start", c.Name).CombinedOutput()
			if err != nil {
				return fmt.Errorf("starting container %q: %s", c.Name, out)
			}

			log.Printf("restarted exited container %q", c.Name)
			state.ReEnter()
			return nil
		}

		log.Printf("starting container %q...", c.Name)
		if err := podmanStart(client, c); err != nil {
			return fmt.Errorf("error while starting container %q: %s", c.Name, err)
		}

		log.Printf("started container %q", c.Name)
		state.ReEnter()
		return nil
	}

	return nil
}

func podmanPs() ([]*psOutput, error) {
	cmd := exec.Command("podman", "ps", "--all", "--format=json", "--filter=label=createdBy=recompose")
	reader, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("getting command stdout pipe: %w", err)
	}
	defer reader.Close()

	buf := &bytes.Buffer{}
	cmd.Stderr = buf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting 'ps' command: %s", err)
	}

	list := []*psOutput{}
	if err := json.NewDecoder(reader).Decode(&list); err != nil {
		return nil, fmt.Errorf("decoding 'ps' command's output: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("running 'ps' command: %s", buf)
	}

	return list, nil
}

type psOutput struct {
	Names  []string
	Labels map[string]string
	Exited bool
}

func podmanRm(name string) error {
	out, err := exec.Command("podman", "rm", "--force", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", out)
	}
	return nil
}

func podmanStart(client *coordClient, spec *common.ContainerSpec) error {
	expanded := &expandedContainerSpec{
		Spec:             spec,
		DecryptedSecrets: make([]string, len(spec.Secrets)),
	}

	for i, secret := range spec.Secrets {
		resp, err := client.Post(client.BaseURL+"/decrypt", "", bytes.NewBufferString(secret.Ciphertext))
		if err != nil {
			return fmt.Errorf("decrypting secret for env var %q: %s", secret.EnvVar, err)
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return fmt.Errorf("server error (status %d) while decrypting secret for env var %q", resp.StatusCode, secret.EnvVar)
		}

		buf, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading decrypted secret for env var %q: %s", secret.EnvVar, err)
		}
		resp.Body.Close()
		expanded.DecryptedSecrets[i] = string(buf)
	}

	out, err := exec.Command("podman", getPodmanFlags(expanded)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", out)
	}

	return nil
}

type expandedContainerSpec struct {
	Spec             *common.ContainerSpec
	DecryptedSecrets []string // aligned with Config.Secrets
}

func getPodmanFlags(c *expandedContainerSpec) []string {
	args := []string{"run", "-d", "--name", c.Spec.Name, "--label=createdBy=recompose", "--label=recomposeHash=" + c.Spec.Hash}

	for key, val := range c.Spec.Flags {
		switch v := val.(type) {
		case []string:
			for _, cur := range v {
				args = append(args, fmt.Sprintf("--%s=%s", key, cur))
			}
		case []int:
			for _, cur := range v {
				args = append(args, fmt.Sprintf("--%s=%d", key, cur))
			}
		default:
			args = append(args, fmt.Sprintf("--%s=%v", key, val))
		}
	}

	for i, secret := range c.Spec.Secrets {
		args = append(args, fmt.Sprintf("--env=%s=%s", secret.EnvVar, c.DecryptedSecrets[i]))
	}

	args = append(args, c.Spec.Image)
	return append(args, c.Spec.Command...)
}
