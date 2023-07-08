package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/jveski/recompose/internal/api"
	"github.com/urfave/cli/v2"
)

func statusCmd(c *cli.Context) error {
	cc, err := setup(c)
	if err != nil {
		return err
	}

	cluster, err := getClusterStatus(c, cc)
	if err != nil {
		return err
	}
	sort.Slice(cluster.Containers, func(i, j int) bool { return cluster.Containers[i].Name < cluster.Containers[j].Name })

	printClusterStatus(cluster, os.Stdout)
	return nil
}

func printClusterStatus(cluster *api.ClusterState, w io.Writer) {
	tr := tabwriter.NewWriter(w, 6, 6, 4, ' ', 0)
	fmt.Fprintf(tr, "NAME\tNODE\tCREATED\tRESTARTED\n")
	now := time.Now()
	for _, container := range cluster.Containers {
		var lastRestart string
		if container.LastRestart != nil {
			lastRestart = durationToString(now.Sub(*container.LastRestart))
		}
		fmt.Fprintf(tr, "%s\t%s\t%s\t%s\n", container.Name, container.NodeFingerprint[:6], durationToString(now.Sub(container.Created)), lastRestart)
	}
	tr.Flush()
}

func getClusterStatus(c *cli.Context, cc *appContext) (*api.ClusterState, error) {
	resp, err := cc.Client.GET(c.Context, cc.BaseURL+"/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 206 {
		fmt.Fprintf(os.Stderr, "warning: partial results returned from server because one or more agents could not be reached\n")
	}

	body := &api.ClusterState{}
	err = json.NewDecoder(resp.Body).Decode(body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

func durationToString(d time.Duration) string {
	hr := d.Hours()
	if hr > 24 {
		return fmt.Sprintf("%dd", int(hr/24))
	}
	if hr > 1 {
		return fmt.Sprintf("%dh", int(hr))
	}

	min := d.Minutes()
	if min > 1 {
		return fmt.Sprintf("%dm", int(min))
	}

	return fmt.Sprintf("%ds", int(d.Seconds()))
}
