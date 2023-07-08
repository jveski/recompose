package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jveski/recompose/internal/api"
)

func syncPodman(client *coordClient, state inventoryContainer) error {
	current := state.Get()
	if current == nil {
		return nil // nothing to do yet
	}

	goalIndex := map[string]*api.ContainerSpec{}
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
			// TODO: Remove this code
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

var podmanPsArgs = []string{"ps", "--all", "--format=json", "--filter=label=createdBy=recompose"}

func podmanPs() ([]*psOutput, error) {
	cmd := exec.Command("podman", podmanPsArgs...)
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

func podmanStart(client *coordClient, spec *api.ContainerSpec) error {
	expanded := &expandedContainerSpec{
		Spec:             spec,
		DecryptedSecrets: make([]string, len(spec.Secrets)),
		Mounts:           make([]string, len(spec.Files)),
		MountIDs:         make([]string, len(spec.Files)),
	}

	// Decrypt secrets
	for i, secret := range spec.Secrets {
		resp, err := client.POST(context.Background(), client.BaseURL+"/decrypt", bytes.NewBufferString(secret.Ciphertext))
		if err != nil {
			return fmt.Errorf("decrypting secret for env var %q: %s", secret.EnvVar, err)
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
	Spec             *api.ContainerSpec
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
