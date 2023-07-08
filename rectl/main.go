package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jveski/recompose/common"
	"github.com/jveski/recompose/internal/rpc"
	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:  "rectl",
		Usage: "Recompose admin tools",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "coordinator",
				Usage:    "address of the Recompose coordinator i.e. `recompose.mydomain` or `recompose.mydomain:8124`",
				Required: true,
				EnvVars:  []string{"RECOMPOSE_COORDINATOR"},
			},
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "timeout when sending requests to the Recompose coordinator",
				Value: time.Second * 15,
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "status",
				Usage: "Get the status of all containers running on the cluster",
				Action: func(c *cli.Context) error {
					cc, err := setup(c)
					if err != nil {
						return err
					}

					cluster, err := getClusterStatus(c, cc)
					if err != nil {
						return err
					}
					sort.Slice(cluster.Containers, func(i, j int) bool { return cluster.Containers[i].Name < cluster.Containers[j].Name })

					tr := tabwriter.NewWriter(os.Stdout, 6, 6, 4, ' ', 0)
					fmt.Fprintf(tr, "NAME\tNODE\tCREATED\tRESTARTED\n")
					now := time.Now()
					for _, container := range cluster.Containers {
						var lastRestart string
						if container.LastRestart != nil {
							lastRestart = durationToString(now.Sub(*container.LastRestart))
						}
						fmt.Fprintf(tr, "%s\t%s\t%s\t%s\n", container.Name, container.NodeFingerprint[:6], durationToString(now.Sub(container.Created)), lastRestart)
					}
					return tr.Flush()
				},
			},
			{
				Name:      "logs",
				Usage:     "Get logs from a particular container",
				ArgsUsage: "<container name> | <container name>@<node fingerprint prefix>",
				Flags: []cli.Flag{
					&cli.DurationFlag{
						Name:  "since",
						Usage: "start of the time window to query",
					},
				},
				Action: func(c *cli.Context) error {
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

					req, err := http.NewRequestWithContext(c.Context, "GET", cc.BaseURL+"/nodes/"+nodeFingerprint+"/logs?"+q.Encode(), nil)
					if err != nil {
						return err
					}

					resp, err := cc.Client.Do(req)
					if err != nil {
						return err
					}
					defer resp.Body.Close()

					_, err = io.Copy(os.Stdout, resp.Body)
					return err
				},
			},
		},
	}

	err := app.Run(os.Args)
	if err == nil {
		return
	}

	{
		e := &common.ErrUntrustedServer{}
		if errors.As(err, &e) {
			fmt.Fprintf(os.Stderr, "The certificate presented by the server is not trusted. Use this command to trust it:\n\n  echo \"%s\" >> %s\n\n", e.Fingerprint, "~/.rectl/trustedcerts")
			os.Exit(1)
		}
	}

	{
		e := &errUntrustedClient{}
		if errors.As(err, &e) {
			fmt.Fprintf(os.Stderr, "The server does not trust your client certificate.\nAdd its fingerprint to the cluster's `cluster.toml` like this:\n\n[[ client ]]\nfingerprint = \"%s\"\n\n", e.Fingerprint)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "error: %s\n", err)
	os.Exit(1)
}

func getClusterStatus(c *cli.Context, cc *appContext) (*common.ClusterState, error) {
	req, err := http.NewRequestWithContext(c.Context, "GET", cc.BaseURL+"/status", nil)
	if err != nil {
		return nil, err
	}

	resp, err := cc.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 403 {
		return nil, &errUntrustedClient{Fingerprint: cc.CertFingerprint}
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}
	if resp.StatusCode == 206 {
		fmt.Fprintf(os.Stderr, "warning: partial results returned from server because one or more agents could not be reached\n")
	}
	defer resp.Body.Close()

	body := &common.ClusterState{}
	err = json.NewDecoder(resp.Body).Decode(body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

func resolveContainerName(cluster *common.ClusterState, ref string) (string, string, error) {
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

type errUntrustedClient struct {
	Fingerprint string
}

func (e *errUntrustedClient) Error() string { return "server does not trust this client" }

func setup(c *cli.Context) (*appContext, error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting homedir: %w", err)
	}
	dir := filepath.Join(homedir, ".rectl")

	cert, fingerprint, err := rpc.GenCertificate(dir)
	if err != nil {
		return nil, fmt.Errorf("generating cert: %w", err)
	}

	trusted, err := loadTrustedCerts(dir)
	if err != nil {
		return nil, fmt.Errorf("reading trusted certs file: %w", err)
	}

	client := common.NewClient(cert, c.Duration("timeout"), func(fingerprint string) bool {
		_, ok := trusted[fingerprint]
		return ok
	})

	var url string
	chunks := strings.Split(c.String("coordinator"), ":")
	if len(chunks) == 1 {
		url = "https://" + chunks[0] + ":8123"
	} else {
		url = "https://" + chunks[0] + ":" + chunks[1]
	}

	return &appContext{
		Client:          client,
		CertFingerprint: fingerprint,
		BaseURL:         url,
	}, nil
}

func loadTrustedCerts(dir string) (map[string]struct{}, error) {
	m := map[string]struct{}{}

	buf, err := os.ReadFile(filepath.Join(dir, "trustedcerts"))
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading trusted certs file: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewBuffer(buf))
	for scanner.Scan() {
		m[scanner.Text()] = struct{}{}
	}

	return m, nil
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

type appContext struct {
	Client          *http.Client
	CertFingerprint string
	BaseURL         string
}
