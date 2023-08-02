package main

import (
	"errors"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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

func resolveContainerName(cluster [][]string, ref string) (string, string, error) {
	chunks := strings.SplitN(ref, "@", 2)
	for _, row := range cluster {
		if len(row) < 6 {
			continue
		}
		var (
			containerName   = row[0]
			nodeFingerprint = row[5]
		)
		if containerName != chunks[0] {
			continue
		}
		if len(chunks) == 1 || strings.HasPrefix(nodeFingerprint, chunks[1]) {
			return containerName, nodeFingerprint, nil
		}
	}
	return "", "", errors.New("container not found")
}
