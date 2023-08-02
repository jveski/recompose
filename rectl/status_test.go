package main

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPrintClusterStatus(t *testing.T) {
	now := time.Now()
	mktime := func(delta time.Duration) string {
		return strconv.Itoa(int(now.Add(delta).Unix()))
	}

	cluster := [][]string{
		{"test-name-1", "TestState", "test reason", mktime(0), mktime(-time.Second * 2), "111111111111111111111"},
		{"test-name-2", "TestState", "", mktime(0), mktime(-time.Minute * 2), "111111111111111111111"},
		{"test-name-3", "", "", mktime(0), mktime(-time.Hour * 2), "111111111111111111111"},
		{"test-name-4", "", "test reason", mktime(0), mktime(-time.Hour * 24 * 2), "111111111111111111111"},
	}

	buf := &bytes.Buffer{}
	printClusterStatus(cluster, buf)

	assert.Equal(t, "NAME           STATE        CREATED    STARTED    NODE      REASON\ntest-name-1    TestState    0s         2s         111111    \"test reason\"\ntest-name-2    TestState    0s         2m         111111    \ntest-name-3                 0s         2h         111111    \ntest-name-4                 0s         2d         111111    \"test reason\"\n", buf.String())
}
