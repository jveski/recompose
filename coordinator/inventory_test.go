package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadInventory(t *testing.T) {
	store := newNodeMetadataStore()
	store.Set("not-a-node", &nodeMetadata{})       // this should be pruned since the node isn't defined in the cluster.toml
	store.Set("test-fingerprint", &nodeMetadata{}) // this one should not be pruned since the node is defined in the cluster.toml

	inv := newIndexedInventory("")
	err := readInventory("fixtures/simple-inventory", inv, store)
	require.NoError(t, err)

	assert.Len(t, inv.NodesByFingerprint["test-fingerprint"].Containers, 2)
	assert.Nil(t, store.Get("not-a-node"))
	assert.NotNil(t, store.Get("test-fingerprint"))
}
