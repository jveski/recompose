package rpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUrlPrefix(t *testing.T) {
	assert.Equal(t, "https://foo:8123", UrlPrefix("foo"))
	assert.Equal(t, "https://foo:123", UrlPrefix("foo:123"))
}
