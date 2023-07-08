package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/jveski/recompose/common"
	"github.com/jveski/recompose/internal/concurrency"
)

type inventoryContainer = *concurrency.StateContainer[*common.NodeInventory]

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
