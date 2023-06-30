package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"

	"github.com/jveski/recompose/common"
)

func main() {
	var (
		coordinatorAddr        = flag.String("coordinator", "", "host or host:port of the coordination server")
		coordinatorFingerprint = flag.String("coordinator-fingerprint", "", "fingerprint of the coordination server's certificate")
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

	cert, _, err := common.GenCertificate(".")
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
		err := register(client, getOutboundIP().String(), *port)
		if err != nil {
			log.Printf("error registering node metadata with coordinator: %s", err)
		}
		return err == nil
	})

	svr := &http.Server{
		Addr: fmt.Sprintf(":%d", *port),
		Handler: common.WithLogging(
			common.WithAuth(&staticAuthorizer{Fingerprint: *coordinatorFingerprint},
				newApiHandler())),
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
	inUseFiles := map[string]struct{}{}
	for _, c := range existing {
		hash := c.Labels["recomposeHash"]
		existingIndex[hash] = c
		for _, mount := range strings.Split(c.Labels["recomposeMounts"], ",") {
			inUseFiles[mount] = struct{}{}
		}
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

	// Clean up unused files
	mountFiles, err := os.ReadDir("mounts")
	if err != nil {
		return fmt.Errorf("listing mount files: %w", err)
	}
	for _, file := range mountFiles {
		if _, ok := inUseFiles[file.Name()]; ok {
			continue // still in use
		}

		err := os.Remove(filepath.Join("mounts", file.Name()))
		if err != nil {
			return fmt.Errorf("cleaning up mount file: %w", err)
		}
		log.Printf("cleaned up mount file %q", file.Name())
	}

	// Start missing containers
	for _, c := range goalIndex {
		if e, ok := existingIndex[c.Hash]; ok {
			if !e.Exited {
				continue // already running
			}
			if e.Labels != nil && e.Labels["kickstart"] == "false" {
				continue // should not be kickstarted
			}

			// the container has stopped somehow - restart it
			log.Printf("kickstarting exited container %q...", c.Name)
			out, err := exec.Command("podman", "start", c.Name).CombinedOutput()
			if err != nil {
				return fmt.Errorf("kickstarting container %q: %s", c.Name, out)
			}

			log.Printf("kickstarted exited container %q", c.Name)
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
		Mounts:           make([]string, len(spec.Files)),
		MountIDs:         make([]string, len(spec.Files)),
	}

	// Decrypt secrets
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

	// Write files to disk
	for i, file := range spec.Files {
		id := uuid.Must(uuid.NewRandom()).String()
		dest := filepath.Join("mounts", id)
		err := os.WriteFile(dest, []byte(file.Content), 0755)
		if err != nil {
			return fmt.Errorf("writing file for mount %q: %s", file.Path, err)
		}

		expanded.MountIDs[i] = id
		expanded.Mounts[i], err = filepath.Abs(dest)
		if err != nil {
			return fmt.Errorf("getting abspath for mount %q: %s", file.Path, err)
		}
		log.Printf("wrote mount file %q", id)
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
	Mounts           []string // aligned with Config.Files
	MountIDs         []string // aligned with Config.Files
}

func getPodmanFlags(c *expandedContainerSpec) []string {
	args := []string{"run", "-d", "--name", c.Spec.Name, "--label=createdBy=recompose", "--label=recomposeHash=" + c.Spec.Hash}

	for key, val := range c.Spec.Flags {
		switch v := val.(type) {
		case []interface{}:
			for _, cur := range v {
				args = append(args, fmt.Sprintf("--%s=%v", key, cur))
			}
		default:
			if v, ok := val.(string); ok {
				if key == "restart" && v != "always" && v != "unless-stopped" {
					args = append(args, "--label=kickstart=false")
				}
			}
			args = append(args, fmt.Sprintf("--%s=%v", key, val))
		}
	}

	for i, secret := range c.Spec.Secrets {
		args = append(args, fmt.Sprintf("--env=%s=%s", secret.EnvVar, c.DecryptedSecrets[i]))
	}

	for i, file := range c.Spec.Files {
		args = append(args, fmt.Sprintf("--mount=type=bind,source=%s,target=%s,readonly", c.Mounts[i], file.Path))
	}
	if len(c.MountIDs) > 0 {
		args = append(args, fmt.Sprintf("--label=recomposeMounts=%s", strings.Join(c.MountIDs, ",")))
	}

	args = append(args, c.Spec.Image)
	return append(args, c.Spec.Command...)
}

type staticAuthorizer struct {
	Fingerprint string
}

func (s *staticAuthorizer) TrustsCert(fingerprint string) bool { return s.Fingerprint == fingerprint }

func newApiHandler() http.Handler {
	router := httprouter.New()

	router.GET("/status", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		output, err := podmanPs()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		json.NewEncoder(w).Encode(&output)
	})

	router.GET("/logs", func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		args := []string{"logs"}
		if r.URL.Query().Get("follow") != "" {
			args = append(args, "-f")
		}
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

		scan := bufio.NewScanner(pipe)
		if err := cmd.Start(); err != nil {
			log.Printf("error starting container log stream: %s", err)
			return
		}

		flusher := w.(common.WrappedResponseWriter).Unwrap().(http.Flusher)
		flusher.Flush()

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
	})

	return router
}

func register(client *coordClient, ip string, port uint) error {
	form := url.Values{}
	form.Add("ip", ip)
	form.Add("apiport", strconv.Itoa(int(port)))

	// time out the long polling connection after a reasonable period
	ctx, done := context.WithTimeout(context.Background(), common.Jitter(time.Minute*15))
	defer done()

	req, err := http.NewRequestWithContext(ctx, "POST", client.BaseURL+"/registernode", strings.NewReader(form.Encode()))
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

func getOutboundIP() net.IP {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		log.Fatalf("unable to determine outbound IP address: %s", err)
	}
	conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP
}
