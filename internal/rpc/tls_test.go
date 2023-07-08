package rpc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenCertificate(t *testing.T) {
	dir := t.TempDir()

	// Initial generation
	cert, fingerprint, err := GenCertificate(dir)
	require.NoError(t, err)
	assert.Len(t, fingerprint, 64)
	assert.Equal(t, fingerprint, GetCertFingerprint(cert.Leaf.Raw))
	initialFingerprint := fingerprint

	// Load
	cert, fingerprint, err = GenCertificate(dir)
	require.NoError(t, err)
	assert.Equal(t, initialFingerprint, fingerprint)
	assert.Equal(t, initialFingerprint, GetCertFingerprint(cert.Leaf.Raw))

	// The fingerprint file is regenerated if removed
	fingerprintPath := filepath.Join(dir, "tls", "cert-fingerprint.txt")
	require.NoError(t, os.Remove(fingerprintPath))
	cert, fingerprint, err = GenCertificate(dir)
	require.NoError(t, err)
	assert.Equal(t, initialFingerprint, fingerprint)
	assert.Equal(t, initialFingerprint, GetCertFingerprint(cert.Leaf.Raw))
}
