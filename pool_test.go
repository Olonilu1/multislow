package multislow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// fake is a scriptable Session: dials return an in-memory echo connection.
type fake struct {
	name     string
	delay    time.Duration
	failDial atomic.Bool
	failDest atomic.Bool
	failPing atomic.Bool
	dials    atomic.Int64
}

func (f *fake) Name() string { return f.name }

func (f *fake) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if f.failDest.Load() {
		return nil, fmt.Errorf("%w: refused", ErrDestination)
	}
	if f.failDial.Load() {
		return nil, errors.New("boom")
	}
	f.dials.Add(1)
	a, b := net.Pipe()
	go func() {
		io.Copy(b, b)
		b.Close()
	}()
	return a, nil
}

func (f *fake) Ping(ctx context.Context) (time.Duration, error) {
	if f.failPing.Load() {
		return 0, errors.New("ping failed")
	}
	return f.delay, nil
}

func (f *fake) Close() error { return nil }

func mustPoolS(t *testing.T, st Strategy, ss ...Session) *Pool {
	t.Helper()
	p, err := NewPool(ss, Options{
		Strategy:       st,
		HealthInterval: 20 * time.Millisecond,
		PingTimeout:    200 * time.Millisecond,
		DialTimeout:    2 * time.Second,
		MaxFails:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mustPool(t *testing.T, st Strategy, fs ...*fake) *Pool {
	t.Helper()
	ss := make([]Session, len(fs))
	for i, f := range fs {
		ss[i] = f
	}
	return mustPoolS(t, st, ss...)
}

func statOf(p *Pool, name string) Stats {
	for _, s := range p.Stats() {
		if s.Name == name {
			return s
		}
	}
	return Stats{}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestRoundRobinSpreadsEvenly(t *testing.T) {
	fs := []*fake{{name: "a"}, {name: "b"}, {name: "c"}}
	p := mustPool(t, RoundRobin, fs...)
	for i := 0; i < 30; i++ {
		c, err := p.Dial(context.Background(), "tcp", "x:1")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	for _, f := range fs {
		if got := f.dials.Load(); got != 10 {
			t.Errorf("%s served %d, want 10", f.name, got)
		}
	}
}

func TestFailoverAndMarkDown(t *testing.T) {
	bad := &fake{name: "bad"}
	bad.failDial.Store(true)
	g1, g2 := &fake{name: "g1"}, &fake{name: "g2"}
	p := mustPool(t, RoundRobin, bad, g1, g2)
	for i := 0; i < 12; i++ {
		c, err := p.Dial(context.Background(), "tcp", "x:1")
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		c.Close()
	}
	if !statOf(p, "bad").Down {
		t.Error("failing session was not marked down")
	}
	if g1.dials.Load()+g2.dials.Load() != 12 {
		t.Errorf("healthy sessions served %d, want 12", g1.dials.Load()+g2.dials.Load())
	}
}

func TestAllDownReturnsError(t *testing.T) {
	a, b := &fake{name: "a"}, &fake{name: "b"}
	a.failDial.Store(true)
	b.failDial.Store(true)
	p := mustPool(t, Fastest, a, b)
	if _, err := p.Dial(context.Background(), "tcp", "x:1"); err == nil {
		t.Fatal("expected an error when every session fails")
	}
}

func TestLeastLoadedBalancesOpenConns(t *testing.T) {
	fs := []*fake{{name: "a"}, {name: "b"}, {name: "c"}}
	p := mustPool(t, LeastLoaded, fs...)
	var conns []net.Conn
	for i := 0; i < 6; i++ {
		c, err := p.Dial(context.Background(), "tcp", "x:1")
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, s := range p.Stats() {
		if s.Active != 2 {
			t.Errorf("%s has %d open, want 2", s.Name, s.Active)
		}
	}
	for _, c := range conns {
		c.Close()
		c.Close() // double close must not double-decrement
	}
	for _, s := range p.Stats() {
		if s.Active != 0 {
			t.Errorf("%s has %d open after close, want 0", s.Name, s.Active)
		}
	}
}

func TestRaceKeepsFastest(t *testing.T) {
	slow := &fake{name: "slow", delay: 300 * time.Millisecond}
	mid := &fake{name: "mid", delay: 100 * time.Millisecond}
	fast := &fake{name: "fast", delay: 10 * time.Millisecond}
	p := mustPool(t, Race, slow, mid, fast)
	start := time.Now()
	c, err := p.Dial(context.Background(), "tcp", "x:1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if el := time.Since(start); el > 80*time.Millisecond {
		t.Errorf("race took %s, want about the fastest session's 10ms", el)
	}
	time.Sleep(50 * time.Millisecond)
	if fast.dials.Load() != 1 || mid.dials.Load() != 0 || slow.dials.Load() != 0 {
		t.Errorf("dials fast=%d mid=%d slow=%d", fast.dials.Load(), mid.dials.Load(), slow.dials.Load())
	}
}

func TestDestinationErrorsDontPenalise(t *testing.T) {
	d := &fake{name: "d"}
	d.failDest.Store(true)
	p := mustPool(t, RoundRobin, d)
	for i := 0; i < 10; i++ {
		_, err := p.Dial(context.Background(), "tcp", "x:1")
		if !errors.Is(err, ErrDestination) {
			t.Fatalf("got %v, want ErrDestination", err)
		}
	}
	if statOf(p, "d").Down {
		t.Error("session marked down because the destination was unreachable")
	}
}

func TestHealthMarksDownAndRecovers(t *testing.T) {
	a, b := &fake{name: "a"}, &fake{name: "b"}
	p := mustPool(t, RoundRobin, a, b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Close()

	b.failPing.Store(true)
	waitFor(t, 2*time.Second, func() bool { return statOf(p, "b").Down })

	for i := 0; i < 10; i++ {
		c, err := p.Dial(ctx, "tcp", "x:1")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	if a.dials.Load() != 10 || b.dials.Load() != 0 {
		t.Errorf("a=%d b=%d, want all traffic on a while b is down", a.dials.Load(), b.dials.Load())
	}

	b.failPing.Store(false)
	waitFor(t, 2*time.Second, func() bool { return !statOf(p, "b").Down })
}

func TestFastestPrefersLowLatency(t *testing.T) {
	slow := &fake{name: "slow", delay: 80 * time.Millisecond}
	fast := &fake{name: "fast", delay: 5 * time.Millisecond}
	p := mustPool(t, Fastest, slow, fast)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx) // initial probe fills in latencies
	defer p.Close()
	for i := 0; i < 10; i++ {
		c, err := p.Dial(ctx, "tcp", "x:1")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	if fast.dials.Load() <= slow.dials.Load() {
		t.Errorf("fast=%d slow=%d, expected the faster session to carry more", fast.dials.Load(), slow.dials.Load())
	}
}

func validConfig() *Config {
	e := func(name, listen string) Entry {
		return Entry{Name: name, Listen: listen, SSH: SSHConf{User: "u", Password: "p", HostKeySHA256: "SHA256:x"}}
	}
	return &Config{
		Listen:  "127.0.0.1:1080",
		Entries: []Entry{e("a", "127.0.0.1:2201"), e("b", "127.0.0.1:2202"), e("c", "127.0.0.1:2203")},
	}
}

func TestConfigValidate(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := map[string]func(*Config){
		"too many entries": func(c *Config) { c.Entries = append(c.Entries, c.Entries[0]) },
		"no entries":       func(c *Config) { c.Entries = nil },
		"duplicate name":   func(c *Config) { c.Entries[1].Name = "a" },
		"duplicate port":   func(c *Config) { c.Entries[1].Listen = c.Entries[0].Listen },
		"public listen":    func(c *Config) { c.Listen = "0.0.0.0:1080" },
		"no host key":      func(c *Config) { c.Entries[0].SSH.HostKeySHA256 = "" },
		"no credentials":   func(c *Config) { c.Entries[0].SSH.Password = "" },
		"bad strategy":     func(c *Config) { c.Strategy = "nope" },
	}
	for name, mutate := range cases {
		c := validConfig()
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}
