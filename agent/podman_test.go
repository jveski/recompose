package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/jveski/recompose/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetPodmanFlags(t *testing.T) {
	fullToml := `
	image = "test-image"
	command = ["foo", "bar"]

	[ flags ]
	strarray = ["bar", "baz"]
	intarray = [1, 2]

	booltrue = true
	boolfalse = false

	str = "foo"
	int = 123

	[[ secret ]]
	envvar = "test-env"
	ciphertext = "encrypted-value"

	[[ file ]]
	path = "/testpath"
	content = """
	  test-content
	"""
`

	container := &api.ContainerSpec{Name: "test-name"}
	_, err := toml.Decode(fullToml, container)
	require.NoError(t, err)

	expanded := &expandedContainerSpec{
		Spec:             container,
		DecryptedSecrets: []string{"decrypted-value"},
		Mounts:           []string{"full-mount-path"},
		MountIDs:         []string{"mount-id"},
	}

	actual := getPodmanFlags(expanded)
	sort.Strings(actual)
	expected := []string{"--boolfalse=false", "--booltrue=true", "--env=test-env=decrypted-value", "--int=123", "--intarray=1", "--intarray=2", "--label=createdBy=recompose", "--label=recomposeHash=", "--label=recomposeMounts=mount-id", "--mount=type=bind,source=full-mount-path,target=/testpath,readonly", "--name", "--str=foo", "--strarray=bar", "--strarray=baz", "-d", "bar", "foo", "run", "test-image", "test-name"}

	assert.Equal(t, expected, actual)
}

func TestWriteState(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "test-hash.txt")

	t.Run("initial creation", func(t *testing.T) {
		writeStateInDir(dir, "test-container", "test-hash", "TestState", "test reason")

		actual, err := os.ReadFile(fp)
		require.NoError(t, err)
		assert.Equal(t, "test-container\nTestState\ntest reason", string(actual))
	})

	t.Run("update", func(t *testing.T) {
		writeStateInDir(dir, "test-container", "test-hash", "TestState", "new reason")

		actual, err := os.ReadFile(fp)
		require.NoError(t, err)
		assert.Equal(t, "test-container\nTestState\nnew reason", string(actual))
	})

	t.Run("idempotence", func(t *testing.T) {
		stat, err := os.Stat(fp)
		require.NoError(t, err)
		prevModTime := stat.ModTime()
		time.Sleep(time.Millisecond) // make sure mod time has time to increment

		writeStateInDir(dir, "test-container", "test-hash", "TestState", "new reason")

		stat, err = os.Stat(fp)
		require.NoError(t, err)
		assert.Equal(t, stat.ModTime(), prevModTime)
	})
}
