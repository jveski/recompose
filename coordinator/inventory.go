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

	"github.com/BurntSushi/toml"

	"github.com/jveski/recompose/common"
)

type inventoryContainer = *common.StateContainer[*indexedInventory]

func syncInventory(dir string, state inventoryContainer) error {
	sha, err := gitPull(dir)
	if err != nil {
		return fmt.Errorf("pulling git repo: %w", err)
	}

	if current := state.Get(); current != nil && current.GitSHA == sha {
		return nil // already in sync
	}
	log.Printf("pulled git SHA: %s", sha)

	inv := &indexedInventory{
		GitSHA: sha,
		ByNode: make(map[string]*common.NodeInventory),
	}
	err = readInventory(dir, inv)
	if err != nil {
		return fmt.Errorf("reading inventory: %w", err)
	}

	state.Swap(inv)
	return nil
}

func gitPull(dir string) (string /* sha */, error) {
	cmd := exec.Command("git", "pull")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git error: %s", out)
	}

	cmd = exec.Command("git", "rev-parse", "--verify", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git error: %s", out)
	}
	rev := strings.TrimSpace(string(out))

	return rev, nil
}

func readInventory(dir string, inv *indexedInventory) error {
	cluster := &clusterSpec{}
	_, err := toml.DecodeFile(filepath.Join(dir, "cluster.toml"), cluster)
	if os.IsNotExist(err) {
		return nil // no inventory
	}
	if err != nil {
		return err
	}

	containerIndex := map[string]*common.ContainerSpec{}
	for _, node := range cluster.Nodes {
		if node.Fingerprint == "" {
			continue
		}

		nodeInv := &common.NodeInventory{GitSHA: inv.GitSHA}
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

		inv.ByNode[node.Fingerprint] = nodeInv
	}

	return nil
}

func readContainerSpec(file string) (*common.ContainerSpec, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	hash := md5.New()
	r := io.TeeReader(f, hash)

	spec := &common.ContainerSpec{}
	if _, err := toml.NewDecoder(r).Decode(spec); err != nil {
		return nil, err
	}

	fileName := path.Base(file)
	spec.Name = fileName[:len(fileName)-len(path.Ext(fileName))]
	spec.Hash = hex.EncodeToString(hash.Sum(nil))

	return spec, nil
}

type clusterSpec struct {
	Nodes []*nodeSpec `toml:"node"`
}

type nodeSpec struct {
	Fingerprint string   `toml:"fingerprint"`
	Containers  []string `toml:"containers"`
}

type indexedInventory struct {
	GitSHA string
	ByNode map[string]*common.NodeInventory
}
