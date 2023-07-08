package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jveski/recompose/internal/api"
	"github.com/jveski/recompose/internal/concurrency"
)

type inventoryContainer = *concurrency.StateContainer[*indexedInventory]

func syncInventory(dir string, state inventoryContainer, nms *nodeMetadataStore) error {
	sha, err := gitPull(dir)
	if err != nil {
		return fmt.Errorf("pulling git repo: %w", err)
	}

	if current := state.Get(); current != nil && current.GitSHA == sha {
		return nil // already in sync
	}
	log.Printf("pulled git SHA: %s", sha)

	inv := &indexedInventory{
		GitSHA:               sha,
		NodesByFingerprint:   make(map[string]*api.NodeInventory),
		ClientsByFingerprint: make(map[string]struct{}),
	}
	err = readInventory(dir, inv, nms)
	if err != nil {
		return fmt.Errorf("reading inventory: %w", err)
	}

	state.Swap(inv)
	return nil
}

func gitPull(dir string) (string /* sha */, error) {
	start := time.Now()
	cmd := exec.Command("git", "pull")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git error: %s", out)
	}
	log.Printf("pulled git repo in %s", time.Since(start))

	cmd = exec.Command("git", "rev-parse", "--verify", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git error: %s", out)
	}
	rev := strings.TrimSpace(string(out))

	return rev, nil
}

func readInventory(dir string, inv *indexedInventory, nms *nodeMetadataStore) error {
	cluster := &clusterSpec{}
	_, err := toml.DecodeFile(filepath.Join(dir, "cluster.toml"), cluster)
	if os.IsNotExist(err) {
		return nil // no inventory
	}
	if err != nil {
		return err
	}

	containerIndex := map[string]*api.ContainerSpec{}
	for _, node := range cluster.Nodes {
		if node.Fingerprint == "" {
			continue
		}

		nodeInv := &api.NodeInventory{GitSHA: inv.GitSHA}
		for _, path := range node.Containers {
			if container, ok := containerIndex[path]; ok {
				nodeInv.Containers = append(nodeInv.Containers, container)
				continue
			}

			container, err := readContainerSpec(filepath.Join(dir, path))
			if err != nil {
				log.Printf("error while reading container file %q referenced by node %q: %s", path, node.Fingerprint, err)
				continue
			}
			containerIndex[path] = container
			nodeInv.Containers = append(nodeInv.Containers, container)
		}

		inv.NodesByFingerprint[node.Fingerprint] = nodeInv
	}
	for _, cli := range cluster.Clients {
		inv.ClientsByFingerprint[cli.Fingerprint] = struct{}{}
	}

	// Prune metadata for nodes that no longer exist
	nms.lock.Lock()
	defer nms.lock.Unlock()

	for _, node := range nms.byFingerprint {
		if _, ok := inv.NodesByFingerprint[node.Fingerprint]; ok {
			continue
		}
		delete(nms.byFingerprint, node.Fingerprint)
	}

	return nil
}

func readContainerSpec(file string) (*api.ContainerSpec, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	hash := md5.New()
	r := io.TeeReader(f, hash)

	spec := &api.ContainerSpec{}
	if _, err := toml.NewDecoder(r).Decode(spec); err != nil {
		return nil, err
	}

	fileName := path.Base(file)
	spec.Name = fileName[:len(fileName)-len(path.Ext(fileName))]
	spec.Hash = hex.EncodeToString(hash.Sum(nil))

	return spec, nil
}

type clusterSpec struct {
	Nodes   []*nodeSpec   `toml:"node"`
	Clients []*clientSpec `toml:"client"`
}

type nodeSpec struct {
	Fingerprint string   `toml:"fingerprint"`
	Containers  []string `toml:"containers"`
}

type clientSpec struct {
	Fingerprint string `toml:"fingerprint"`
}

type indexedInventory struct {
	GitSHA               string
	NodesByFingerprint   map[string]*api.NodeInventory
	ClientsByFingerprint map[string]struct{}
}

type nodeMetadataStore struct {
	lock          sync.Mutex
	byFingerprint map[string]*nodeMetadata
}

func newNodeMetadataStore() *nodeMetadataStore {
	return &nodeMetadataStore{byFingerprint: make(map[string]*nodeMetadata)}
}

func (n *nodeMetadataStore) Set(fingerprint string, meta *nodeMetadata) {
	n.lock.Lock()
	defer n.lock.Unlock()
	n.byFingerprint[fingerprint] = meta
}

func (n *nodeMetadataStore) Get(fingerprint string) *nodeMetadata {
	n.lock.Lock()
	defer n.lock.Unlock()
	return n.byFingerprint[fingerprint]
}

func (n *nodeMetadataStore) List() []*nodeMetadata {
	n.lock.Lock()
	defer n.lock.Unlock()

	slice := []*nodeMetadata{}
	for _, node := range n.byFingerprint {
		slice = append(slice, node)
	}
	return slice
}

type nodeMetadata struct {
	Fingerprint string
	IP          string
	APIPort     uint
}
