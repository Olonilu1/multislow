package multislow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

// MaxEntries is the number of tunnel configs combined at once.
const MaxEntries = 3

// SSHConf is how to log in to the SSH server reached through a tunnel.
type SSHConf struct {
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
	// HostKeySHA256 pins the server key, as printed by `ssh-keygen -lf` ("SHA256:...").
	HostKeySHA256 string `json:"host_key_sha256,omitempty"`
	// InsecureSkipHostKey disables host key checking. Opt-in only.
	InsecureSkipHostKey bool `json:"insecure_skip_host_key,omitempty"`
}

// Entry is one tunnel: a local port where a DNS-tunnel client (dnstt style)
// listens, optionally the command that runs it, and the SSH login behind it.
type Entry struct {
	Name      string   `json:"name"`
	Listen    string   `json:"listen"`               // e.g. 127.0.0.1:2201
	ClientCmd []string `json:"client_cmd,omitempty"` // optional: supervised tunnel client command
	SSH       SSHConf  `json:"ssh"`
}

// Config is the whole app configuration.
type Config struct {
	Listen            string   `json:"listen"` // local SOCKS5 address
	Strategy          Strategy `json:"strategy"`
	HealthIntervalSec int      `json:"health_interval_sec"`
	Entries           []Entry  `json:"entries"`
}

// Load reads, defaults and validates a JSON config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse decodes, defaults and validates a JSON config held in memory.
func Parse(data []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // catch typos in keys
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:1080"
	}
	if c.Strategy == "" {
		c.Strategy = Fastest
	}
	if c.HealthIntervalSec <= 0 {
		c.HealthIntervalSec = 10
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate checks the config is usable and safe.
func (c *Config) Validate() error {
	if n := len(c.Entries); n < 1 || n > MaxEntries {
		return fmt.Errorf("need 1 to %d entries, got %d", MaxEntries, n)
	}
	switch c.Strategy {
	case "", RoundRobin, LeastLoaded, Fastest, Race:
	default:
		return fmt.Errorf("unknown strategy %q", c.Strategy)
	}
	if c.Listen != "" {
		host, _, err := net.SplitHostPort(c.Listen)
		if err != nil {
			return fmt.Errorf("listen %q: %w", c.Listen, err)
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("listen %q: refusing to expose an unauthenticated SOCKS5 proxy beyond loopback", c.Listen)
		}
	}
	names := map[string]bool{}
	ports := map[string]bool{}
	for i, e := range c.Entries {
		if e.Name == "" {
			return fmt.Errorf("entry %d: name is required", i)
		}
		if names[e.Name] {
			return fmt.Errorf("entry %q: duplicate name", e.Name)
		}
		names[e.Name] = true
		if e.Listen == "" {
			return fmt.Errorf("entry %q: listen is required", e.Name)
		}
		if ports[e.Listen] {
			return fmt.Errorf("entry %q: listen %s already used by another entry", e.Name, e.Listen)
		}
		ports[e.Listen] = true
		if e.SSH.User == "" {
			return fmt.Errorf("entry %q: ssh.user is required", e.Name)
		}
		if e.SSH.Password == "" && e.SSH.KeyFile == "" {
			return fmt.Errorf("entry %q: set ssh.password or ssh.key_file", e.Name)
		}
		if e.SSH.HostKeySHA256 == "" && !e.SSH.InsecureSkipHostKey {
			return fmt.Errorf("entry %q: pin ssh.host_key_sha256, or set insecure_skip_host_key explicitly", e.Name)
		}
	}
	return nil
}

// PoolOptions derives pool settings from the config.
func (c *Config) PoolOptions() Options {
	return Options{
		Strategy:       c.Strategy,
		HealthInterval: time.Duration(c.HealthIntervalSec) * time.Second,
	}
}
