package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
				Name:   "status",
				Usage:  "Get the status of all containers running on the cluster",
				Action: statusCmd,
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
				Action: logsCmd,
			},
		},
	}

	err := app.Run(os.Args)
	if err == nil {
		return
	}

	fmt.Fprint(os.Stderr, getErrorString(err))
	os.Exit(1)
}

type appContext struct {
	Client  *rpc.Client
	BaseURL string
}

func setup(c *cli.Context) (*appContext, error) {
	homedir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting homedir: %w", err)
	}
	dir := filepath.Join(homedir, ".rectl")

	cert, _, err := rpc.GenCertificate(dir)
	if err != nil {
		return nil, fmt.Errorf("generating cert: %w", err)
	}

	trusted, err := loadTrustedCerts(dir)
	if err != nil {
		return nil, fmt.Errorf("reading trusted certs file: %w", err)
	}

	client := rpc.NewClient(cert, c.Duration("timeout"), rpc.AuthorizerFunc(func(fingerprint string) bool {
		_, ok := trusted[fingerprint]
		return ok
	}))

	// TODO: Refactor
	var url string
	chunks := strings.Split(c.String("coordinator"), ":")
	if len(chunks) == 1 {
		url = "https://" + chunks[0] + ":8123"
	} else {
		url = "https://" + chunks[0] + ":" + chunks[1]
	}

	return &appContext{
		Client:  client,
		BaseURL: url,
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

func getErrorString(err error) string {
	es := &rpc.ErrUntrustedServer{}
	if errors.As(err, &es) {
		return fmt.Sprintf("The certificate presented by the server is not trusted. Use this command to trust it:\n\n  echo \"%s\" >> %s\n\n", es.Fingerprint, "~/.rectl/trustedcerts")
	}

	ec := &rpc.ErrUntrustedClient{}
	if errors.As(err, &ec) {
		return fmt.Sprintf("The server does not trust your client certificate.\nAdd its fingerprint to the cluster's `cluster.toml` like this:\n\n[[ client ]]\nfingerprint = \"%s\"\n\n", ec.Fingerprint)
	}

	return fmt.Sprintf("error: %s\n", err)
}
