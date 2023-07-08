package common

import "time"

type NodeInventory struct {
	GitSHA     string           `toml:"gitSHA"`
	Containers []*ContainerSpec `toml:"container"`
}

type ContainerSpec struct {
	Name    string         `toml:"name"` // derived from filename
	Hash    string         `toml:"hash"` // generated when reading
	Image   string         `toml:"image"`
	Command []string       `toml:"command"`
	Flags   map[string]any `toml:"flags"`
	Secrets []*Secret      `toml:"secret"`
	Files   []*File        `toml:"file"`
}

type Secret struct {
	EnvVar     string `toml:"envvar"`
	Ciphertext string `toml:"ciphertext"`
}

type File struct {
	Path    string `toml:"path"`
	Content string `toml:"content"`
}

type ClusterState struct {
	Containers []*ContainerState `json:"containers"`
}

type ContainerState struct {
	Name            string     `json:"name"`
	NodeFingerprint string     `json:"nodeFingerprint"`
	Created         time.Time  `json:"created"`
	LastRestart     *time.Time `json:"lastRestart"`
}
