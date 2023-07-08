package main

import (
	"testing"

	"github.com/jveski/recompose/internal/api"
	"github.com/stretchr/testify/assert"
)

func TestResolveContainerName(t *testing.T) {
	test1 := &api.ContainerState{
		Name:            "test1",
		NodeFingerprint: "node1",
	}

	test2 := &api.ContainerState{
		Name:            "test2",
		NodeFingerprint: "node1-----",
	}

	test2Conflict := &api.ContainerState{
		Name:            "test2",
		NodeFingerprint: "node2----",
	}

	cluster := &api.ClusterState{
		Containers: []*api.ContainerState{test1, test2, test2Conflict},
	}

	tests := []struct {
		Name, Input                       string
		ExpectedName, ExpectedFingerprint string
		ExpectedError                     bool
	}{
		{
			Name:          "not found",
			Input:         "nope",
			ExpectedError: true,
		},
		{
			Name:                "happy path",
			Input:               test1.Name,
			ExpectedName:        test1.Name,
			ExpectedFingerprint: test1.NodeFingerprint,
		},
		{
			Name:          "conflict",
			Input:         test2.Name,
			ExpectedError: true,
		},
		// TODO: Fix this edge case
		// {
		// 	Name:                "unique prefix in conflict",
		// 	Input:               test2.Name + "@node1",
		// 	ExpectedName:        test2.Name,
		// 	ExpectedFingerprint: test2.NodeFingerprint,
		// },
		{
			Name:          "non-unique prefix in conflict",
			Input:         test2.Name + "@node",
			ExpectedError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			name, fprint, err := resolveContainerName(cluster, test.Input)
			assert.Equal(t, test.ExpectedName, name)
			assert.Equal(t, test.ExpectedFingerprint, fprint)
			assert.Equal(t, test.ExpectedError, err != nil)
		})
	}
}
