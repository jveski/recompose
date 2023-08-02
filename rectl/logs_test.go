package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveContainerName(t *testing.T) {
	redHerringRows := [][]string{
		{"foo", ""},
		{"bar", "", "", "", "", "test-node"},
		{"baz", "", "", "", "", "another-node"},
	}

	t.Run("simple", func(t *testing.T) {
		cluster := append(redHerringRows, []string{
			"test-container", "", "", "", "", "test-node",
		})
		name, node, err := resolveContainerName(cluster, "test-container")
		assert.NoError(t, err)
		assert.Equal(t, "test-container", name)
		assert.Equal(t, "test-node", node)
	})

	t.Run("not found", func(t *testing.T) {
		_, _, err := resolveContainerName(redHerringRows, "nope")
		assert.EqualError(t, err, "container not found")
	})

	conflictCluster := append(redHerringRows, []string{
		"test-container", "", "", "", "", "test-node-1",
	}, []string{
		"test-container", "", "", "", "", "test-node-2",
	})

	t.Run("conflict", func(t *testing.T) {
		name, node, err := resolveContainerName(conflictCluster, "test-container")
		assert.NoError(t, err)
		assert.Equal(t, "test-container", name)
		assert.Equal(t, "test-node-1", node)
	})

	t.Run("resolved conflict", func(t *testing.T) {
		name, node, err := resolveContainerName(conflictCluster, "test-container@test-node-1")
		assert.NoError(t, err)
		assert.Equal(t, "test-container", name)
		assert.Equal(t, "test-node-1", node)

		name, node, err = resolveContainerName(conflictCluster, "test-container@test-node-2")
		assert.NoError(t, err)
		assert.Equal(t, "test-container", name)
		assert.Equal(t, "test-node-2", node)
	})

	t.Run("semi-resolved conflict", func(t *testing.T) {
		name, node, err := resolveContainerName(conflictCluster, "test-container@test-node-")
		assert.NoError(t, err)
		assert.Equal(t, "test-container", name)
		assert.Equal(t, "test-node-1", node)
	})
}
