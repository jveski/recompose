package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

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
	sort.Slice(cluster, func(i, j int) bool { return cluster[i][0] < cluster[j][0] })

	printClusterStatus(cluster, os.Stdout)
	return nil
}

func printClusterStatus(cluster [][]string, w io.Writer) {
	tr := tabwriter.NewWriter(w, 6, 6, 4, ' ', 0)
	fmt.Fprintf(tr, "NAME\tSTATE\tCREATED\tSTARTED\tNODE\tREASON\n")
	for _, row := range cluster {
		if len(row) < 6 {
			continue
		}
		reason := ""
		if row[2] != "" {
			reason = fmt.Sprintf("%q", row[2])
		}
		fmt.Fprintf(tr, "%s\t%s\t%s\t%s\t%s\t%s\n", row[0], row[1], transformTime(row[3]), transformTime(row[4]), row[5][:6], reason)
	}
	tr.Flush()
}

func getClusterStatus(c *cli.Context, cc *appContext) ([][]string, error) {
	resp, err := cc.Client.GET(c.Context, cc.BaseURL+"/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 206 {
		fmt.Fprintf(os.Stderr, "warning: partial results returned from server because one or more agents could not be reached\n")
	}

	return csv.NewReader(resp.Body).ReadAll()
}

func transformTime(unix string) string {
	i, err := strconv.ParseInt(unix, 10, 0)
	if err != nil || i == 0 {
		return ""
	}

	return durationToString(time.Since(time.Unix(i, 0)))
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
