package dockerapi

// The wire types carry only the fields the runner reads or sends. This is a
// client, not a schema: the daemon's other fields are ignored on decode, and
// a new field is added here the day something needs it.

// ImageInspect is the subset of an inspected image the runner cares about:
// the digest it resolves to and the user the image bakes in.
type ImageInspect struct {
	ID     string `json:"Id"`
	Config struct {
		User string `json:"User"`
	} `json:"Config"`
	RepoDigests []string `json:"RepoDigests"`
}

// ContainerConfig is the container-level half of a create request.
//
// Entrypoint and Cmd distinguish nil from empty, which is why neither carries
// omitempty: nil encodes as null and the image's own value stands, while an
// explicitly empty slice encodes as [] and clears it. A caller handing over a
// whole command line needs that difference — without it an image's baked-in
// CMD would be appended to the command it meant to replace.
type ContainerConfig struct {
	Image      string            `json:"Image"`
	Entrypoint []string          `json:"Entrypoint"`
	Cmd        []string          `json:"Cmd"`
	Env        []string          `json:"Env,omitempty"`
	User       string            `json:"User,omitempty"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
}

// HostConfig is the host-level half of a create request — the hardening and
// the resource ceilings. ReadonlyRootfs is sent even when false so the
// intent is explicit on the wire.
type HostConfig struct {
	Binds          []string          `json:"Binds,omitempty"`
	NetworkMode    string            `json:"NetworkMode,omitempty"`
	CapDrop        []string          `json:"CapDrop,omitempty"`
	SecurityOpt    []string          `json:"SecurityOpt,omitempty"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	Memory         int64             `json:"Memory,omitempty"`
	NanoCpus       int64             `json:"NanoCpus,omitempty"`
	PidsLimit      *int64            `json:"PidsLimit,omitempty"`
	ExtraHosts     []string          `json:"ExtraHosts,omitempty"`
}

// EndpointSettings is one container's attachment to one network.
type EndpointSettings struct {
	Aliases []string `json:"Aliases,omitempty"`
}

// ContainerInspect is the subset of an inspected container the runner reads
// to classify an outcome.
type ContainerInspect struct {
	ID    string `json:"Id"`
	State struct {
		Status     string `json:"Status"` // created|running|paused|restarting|removing|exited|dead
		ExitCode   int64  `json:"ExitCode"`
		OOMKilled  bool   `json:"OOMKilled"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
	Config struct {
		Env    []string          `json:"Env"`
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// ContainerSummary is one row of a container list.
type ContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	State   string            `json:"State"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

// NetworkSummary is one row of a network list.
type NetworkSummary struct {
	ID      string            `json:"Id"`
	Name    string            `json:"Name"`
	Created string            `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

// NetworkInspect is the subset of an inspected network the runner reads.
type NetworkInspect struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}
