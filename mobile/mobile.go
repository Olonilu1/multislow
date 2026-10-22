// Package mobile is the gomobile entry point used by the Android app. Build it
// with: gomobile bind -target=android/arm64 -androidapi 24 ./mobile
//
// It starts the tunnels, the pool and the local SOCKS5 proxy, then attaches a
// packet engine (tun2socks) to the VPN's TUN file descriptor so that all of the
// phone's traffic flows through the SOCKS5 proxy.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"github.com/xjasonlyu/tun2socks/v2/engine"

	"multislow"
	"multislow/sshsession"
)

var (
	mu     sync.Mutex // serialises Start and Stop
	cancel context.CancelFunc
	live   atomic.Pointer[multislow.Pool] // read by Stats without taking mu
)

// Start brings the whole stack up. configJSON is the multislow config,
// tunFd is the detached VPN file descriptor, and dnsttPath replaces a leading
// "{dnstt}" in each entry's client_cmd (the bundled dnstt-client binary).
// It blocks until the first health probe finishes, so call it off the UI thread.
func Start(configJSON string, tunFd int, dnsttPath string) error {
	mu.Lock()
	defer mu.Unlock()
	if live.Load() != nil {
		return errors.New("already running")
	}
	cfg, err := multislow.Parse([]byte(configJSON))
	if err != nil {
		return err
	}

	ctx, stop := context.WithCancel(context.Background())
	var sessions []multislow.Session
	for _, e := range cfg.Entries {
		var key []byte
		if e.SSH.KeyFile != "" {
			if key, err = os.ReadFile(e.SSH.KeyFile); err != nil {
				stop()
				return fmt.Errorf("entry %s: %w", e.Name, err)
			}
		}
		s, err := sshsession.New(sshsession.Config{
			Name:                e.Name,
			Addr:                e.Listen,
			User:                e.SSH.User,
			Password:            e.SSH.Password,
			PrivateKey:          key,
			HostKeySHA256:       e.SSH.HostKeySHA256,
			InsecureSkipHostKey: e.SSH.InsecureSkipHostKey,
		})
		if err != nil {
			stop()
			return err
		}
		sessions = append(sessions, s)
		if len(e.ClientCmd) > 0 {
			argv := append([]string(nil), e.ClientCmd...)
			if argv[0] == "{dnstt}" {
				argv[0] = dnsttPath
			}
			go multislow.NewSupervisor(e.Name, argv).Run(ctx)
		}
	}

	p, err := multislow.NewPool(sessions, cfg.PoolOptions())
	if err != nil {
		stop()
		return err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		stop()
		return err
	}
	p.Start(ctx)
	go multislow.ServeSOCKS5(ctx, ln, p)

	engine.Insert(&engine.Key{
		Proxy:    "socks5://" + cfg.Listen,
		Device:   fmt.Sprintf("fd://%d", tunFd),
		MTU:      1500,
		LogLevel: "warn",
	})
	engine.Start()

	cancel = stop
	live.Store(p)
	return nil
}

// Stop tears everything down. Safe to call when not running.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	p := live.Load()
	if p == nil {
		return
	}
	engine.Stop()
	cancel()
	p.Close()
	live.Store(nil)
	cancel = nil
}

type statJSON struct {
	Name      string `json:"name"`
	Down      bool   `json:"down"`
	LatencyMS int64  `json:"latency_ms"`
	Active    int64  `json:"active"`
	Served    int64  `json:"served"`
}

// Stats returns per-tunnel status as a JSON array, or "[]" when stopped.
func Stats() string {
	p := live.Load()
	if p == nil {
		return "[]"
	}
	var out []statJSON
	for _, s := range p.Stats() {
		out = append(out, statJSON{
			Name:      s.Name,
			Down:      s.Down,
			LatencyMS: s.Latency.Milliseconds(),
			Active:    s.Active,
			Served:    s.Served,
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}
