# Recompose

Minimal container orchestration designed for GitOps.

- Polling + webhook support
- Encrypted secrets using [age](https://github.com/FiloSottile/age)
- Lightweight coordinator service with easy mTLS


## Future Features

- Coordinated container updates for zero-downtime rolling deployments


## Architecture

- `recompose-agent` processes run on every managed node
- Agents connect to a single, central `recompose-coordinator` process
- Only the coordinator needs access to the secret encryption private keys and git repo

> Agent nodes will continue to function when the coordinator is unavailable, minus any functionality it provides (deployments)


## Installation

### Prereqs

- Podman 4+
- Git
- [age](https://github.com/FiloSottile/age) (if you plan to encrypt secrets)

### Start the Coordinator

On the node that will serve as coordinator:

- Download a binary from the latest Github release
- Customize and install the systemd unit for the [coordinator](./example/recompose-coordinator.service)
- Clone your GitOps repo to `/opt/recompose-coordinator/repo`
  - (The coordinator will `git pull` in this directory to fetch changes)
- Configure Github webhook per the settings in the unit file, salt to taste

### Start Agents

- Download a binary from the latest Github release
- Customize and install the systemd unit for the [agent](./example/recompose-agent.service)
- Get the agent's fingerprint from `/opt/recompose-agent/tls/cert-fingerprint.txt` and commit it to your GitOps repo (see [example](./example/repo/cluster.toml))

### Done!

See the [cluster configuration example](./example/repo/cluster.toml) for how to get started actually managing containers.
