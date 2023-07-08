package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/jveski/recompose/internal/api"
	"github.com/stretchr/testify/assert"
)

func TestPrintClusterStatus(t *testing.T) {
	lr := time.Now().Add(-time.Second)
	cluster := &api.ClusterState{
		Containers: []*api.ContainerState{
			{Name: "test-name-1", NodeFingerprint: "111111111111111111111", Created: time.Now().Add(-time.Hour * 25)},
			{Name: "test-name-2", NodeFingerprint: "111111111111111111111", Created: time.Now().Add(-time.Hour * 23)},
			{Name: "t2", NodeFingerprint: "22222222", Created: time.Now().Add(-time.Minute), LastRestart: &lr},
		},
	}
	buf := &bytes.Buffer{}
	printClusterStatus(cluster, buf)

	assert.Equal(t, "NAME           NODE      CREATED    RESTARTED\ntest-name-1    111111    1d         \ntest-name-2    111111    23h        \nt2             222222    1m         1s\n", buf.String())
}
