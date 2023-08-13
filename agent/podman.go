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

	// Clean up state files when the associated container no longer exists
	stateFiles, err := os.ReadDir("state")
	if err != nil {
		return fmt.Errorf("listing state files: %w", err)
	}
	for _, file := range stateFiles {
		hash := strings.TrimSuffix(file.Name(), ".txt")
		if _, ok := goalIndex[hash]; ok {
			continue // container should still exist
		}
		if _, ok := existingIndex[hash]; ok {
			continue // container still exists
		}

		err := os.Remove(filepath.Join("state", file.Name()))
		if err != nil {
			return fmt.Errorf("cleaning up container state file: %w", err)
		}

		state.ReEnter()
		return nil
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

		writeState(name, hash, "Deleting", "")
		log.Printf("removing container %q...", name)
		if err := podmanRm(name); err != nil {
			writeState(name, hash, "StuckRemoving", "")
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
		if _, ok := existingIndex[c.Hash]; ok {
			writeState(c.Name, c.Hash, "Created", "")
			continue // already created
		}

		log.Printf("starting container %q...", c.Name)
		writeState(c.Name, c.Hash, "Creating", "")
		if err := podmanRm(c.Name); err != nil {
			return fmt.Errorf("error while cleaning up previous container %q: %s", c.Name, err)
		}
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
	Names              []string
	Labels             map[string]string
	Created, StartedAt int64
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
		val, err := decryptSecret(client, secret)
		if err != nil {
			writeState(spec.Name, spec.Hash, "StuckDecryptingSecret", err.Error())
			return fmt.Errorf("decrypting secret for env var %q: %s", secret.EnvVar, err)
		}
		expanded.DecryptedSecrets[i] = string(val)
	}

	// Write files to disk
	for i, file := range spec.Files {
		id, abspath, err := writeFile(file)
		if err != nil {
			writeState(spec.Name, spec.Hash, "StuckWritingFile", err.Error())
			return fmt.Errorf("writing file for mount %q: %s", file.Path, err)
		}

		expanded.MountIDs[i] = id
		expanded.Mounts[i] = abspath
		log.Printf("wrote mount file %q", id)
	}

	out, err := exec.Command("podman", getPodmanFlags(expanded)...).CombinedOutput()
	if err != nil {
		writeState(spec.Name, spec.Hash, "StuckCreating", string(out))
		return fmt.Errorf("%s", out)
	}
	return nil
}

func decryptSecret(client *coordClient, secret *api.Secret) ([]byte, error) {
	resp, err := client.POST(context.Background(), client.BaseURL+"/decrypt", bytes.NewBufferString(secret.Ciphertext))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func writeFile(file *api.File) (string /* id */, string /* abspath */, error) {
	id := uuid.Must(uuid.NewRandom()).String()
	dest := filepath.Join("mounts", id)
	err := os.WriteFile(dest, []byte(file.Content), 0755)
	if err != nil {
		return "", "", err
	}

	abspath, err := filepath.Abs(dest)
	return id, abspath, err
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

func writeState(name, hash, state, reason string) {
	writeStateInDir("state", name, hash, state, reason)
}

func writeStateInDir(dir, name, hash, state, reason string) {
	fp := filepath.Join(dir, hash+".txt")
	goal := []byte(name + "\n" + state + "\n" + reason)

	current, err := os.ReadFile(fp)
	if err == nil && bytes.Equal(current, goal) {
		return // in sync
	}

	f, err := os.CreateTemp("", "")
	if err != nil {
		log.Fatalf("unable to create temp file: %s", err)
	}
	defer f.Close()

	_, err = f.Write(goal)
	if err != nil {
		log.Fatalf("unable to write temp file: %s", err)
	}
	f.Close()

	err = os.Rename(f.Name(), fp)
	if err != nil {
		log.Fatalf("error while swapping container state file: %s", err)
	}
}
