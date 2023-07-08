package main

import (
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jveski/recompose/internal/api"
	"github.com/urfave/cli/v2"
)

func logsCmd(c *cli.Context) error {
	name := c.Args().First()
	if name == "" {
		return errors.New("a container name is required")
	}

	cc, err := setup(c)
	if err != nil {
		return err
	}

	cluster, err := getClusterStatus(c, cc)
	if err != nil {
		return err
	}

	container, nodeFingerprint, err := resolveContainerName(cluster, name)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Add("container", container)
	if since := c.Duration("since"); since > 0 {
		q.Add("since", strconv.Itoa(int(time.Now().Add(-since).Unix())))
	}

	resp, err := cc.Client.GET(c.Context, cc.BaseURL+"/nodes/"+nodeFingerprint+"/logs?"+q.Encode())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

func resolveContainerName(cluster *api.ClusterState, ref string) (string, string, error) {
	chunks := strings.SplitN(ref, "@", 2)
	var candidateName, candidateFingerprint string
	for _, container := range cluster.Containers {
		if container.Name != chunks[0] {
			continue
		}
		if candidateName != "" {
			return "", "", errors.New("multiple containers have this name - reference a specific one using: <container name>@<node fingerprint prefix>")
		}
		if len(chunks) == 1 {
			candidateName = container.Name
			candidateFingerprint = container.NodeFingerprint
		} else if strings.HasPrefix(container.NodeFingerprint, chunks[1]) {
			return container.Name, container.NodeFingerprint, nil
		}
	}
	if candidateName != "" {
		return candidateName, candidateFingerprint, nil
	}
	return "", "", errors.New("container not found")
}
