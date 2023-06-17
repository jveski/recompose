package common

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
