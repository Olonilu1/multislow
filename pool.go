// Package multislow combines several independent tunnel sessions (for example
// three SlowDNS/dnstt tunnels, each with its own domain, key and server) into
// one pool. Callers ask the pool to dial a destination; the pool picks the
// session that should carry it, fails over when a session is broken, and keeps
// per-session health scores up to date in the background.
package multislow

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ErrDestination marks a dial failure caused by the target (refused,
// unreachable, bad address) rather than by the tunnel session itself. The pool
// does not penalise the session for these and does not retry them elsewhere.
var ErrDestination = errors.New("destination unreachable")

// Strategy decides which session carries a new connection.
type Strategy string

const (
	// RoundRobin rotates through healthy sessions in order.
	RoundRobin Strategy = "round-robin"
	// LeastLoaded picks the session with the fewest open connections.
	LeastLoaded Strategy = "least-loaded"
	// Fastest picks the lowest latency, penalised by current load. Faster
	// sessions get proportionally more connections.
	Fastest Strategy = "fastest"
	// Race opens the connection on every healthy session at once and keeps the
	// first to succeed. Lowest connect latency, but multiplies dial load; best
	// kept for small or latency-critical setups.
	Race Strategy = "race"
)

// Session is one tunnel (one domain/key/server) that can open connections.
type Session interface {
	Name() string
	// Dial opens a connection to addr through this tunnel.
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	// Ping measures a round trip through the tunnel and reconnects it if needed.
	Ping(ctx context.Context) (time.Duration, error)
	Close() error
}

// Options tune the pool. Zero values get sensible defaults.
type Options struct {
	Strategy       Strategy
	HealthInterval time.Duration // how often each session is probed
	PingTimeout    time.Duration // per-probe timeout
	DialTimeout    time.Duration // per-dial timeout
	MaxFails       int           // consecutive failures before a session is marked down
}

func (o Options) withDefaults() Options {
	if o.Strategy == "" {
		o.Strategy = Fastest
	}
	if o.HealthInterval <= 0 {
		o.HealthInterval = 10 * time.Second
	}
	if o.PingTimeout <= 0 {
		o.PingTimeout = 8 * time.Second
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 20 * time.Second
	}
	if o.MaxFails <= 0 {
		o.MaxFails = 2
	}
	return o
}

type member struct {
	s      Session
	active atomic.Int64
	served atomic.Int64

	mu    sync.Mutex
	ewma  time.Duration
	fails int
	down  bool
}

func (m *member) markOK(rtt time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rtt > 0 {
		if m.ewma == 0 {
			m.ewma = rtt
		} else {
			m.ewma = (m.ewma*7 + rtt*3) / 10
		}
	}
	m.fails = 0
	m.down = false
}

func (m *member) markFail(maxFails int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fails++
	if m.fails >= maxFails {
		m.down = true
	}
}

func (m *member) isDown() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.down
}

// score is lower-is-better: smoothed latency, inflated by open connections.
func (m *member) score() float64 {
	m.mu.Lock()
	lat := m.ewma
	m.mu.Unlock()
	if lat <= 0 {
		lat = 500 * time.Millisecond // unknown yet: assume middling
	}
	return float64(lat) * (1 + 0.25*float64(m.active.Load()))
}

// Pool spreads connections over several tunnel sessions.
type Pool struct {
	members []*member
	opts    Options
	rr      atomic.Uint64

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPool builds a pool over the given sessions. Call Start to begin health
// checks; Dial works without it but then scores stay at their defaults.
func NewPool(sessions []Session, opts Options) (*Pool, error) {
	if len(sessions) == 0 {
		return nil, errors.New("pool needs at least one session")
	}
	opts = opts.withDefaults()
	switch opts.Strategy {
	case RoundRobin, LeastLoaded, Fastest, Race:
	default:
		return nil, fmt.Errorf("unknown strategy %q", opts.Strategy)
	}
	p := &Pool{opts: opts}
	for _, s := range sessions {
		p.members = append(p.members, &member{s: s})
	}
	return p, nil
}

// Start runs one probe round (so scores are populated), then keeps probing in
// the background until ctx ends or Close is called.
func (p *Pool) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	p.probeAll(ctx)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		t := time.NewTicker(p.opts.HealthInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.probeAll(ctx)
			}
		}
	}()
}

func (p *Pool) probeAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, m := range p.members {
		wg.Add(1)
		go func(m *member) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, p.opts.PingTimeout)
			defer cancel()
			rtt, err := m.s.Ping(pctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				m.markFail(p.opts.MaxFails)
				return
			}
			m.markOK(rtt)
		}(m)
	}
	wg.Wait()
}

// Close stops health checks and closes every session.
func (p *Pool) Close() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	var errs []error
	for _, m := range p.members {
		if err := m.s.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Stats is a point-in-time view of one session.
type Stats struct {
	Name    string
	Active  int64
	Served  int64
	Latency time.Duration
	Down    bool
}

func (p *Pool) Stats() []Stats {
	out := make([]Stats, 0, len(p.members))
	for _, m := range p.members {
		m.mu.Lock()
		lat, down := m.ewma, m.down
		m.mu.Unlock()
		out = append(out, Stats{
			Name:    m.s.Name(),
			Active:  m.active.Load(),
			Served:  m.served.Load(),
			Latency: lat,
			Down:    down,
		})
	}
	return out
}

// candidates returns sessions in the order they should be tried.
func (p *Pool) candidates() []*member {
	up := make([]*member, 0, len(p.members))
	for _, m := range p.members {
		if !m.isDown() {
			up = append(up, m)
		}
	}
	if len(up) == 0 {
		// Everything is marked down: try anyway, a session may have recovered.
		up = append(up, p.members...)
	}

	if p.opts.Strategy == RoundRobin {
		start := int((p.rr.Add(1) - 1) % uint64(len(up)))
		out := make([]*member, 0, len(up))
		out = append(out, up[start:]...)
		return append(out, up[:start]...)
	}

	type ranked struct {
		m      *member
		active int64
		score  float64
	}
	rs := make([]ranked, len(up))
	for i, m := range up {
		rs[i] = ranked{m: m, active: m.active.Load(), score: m.score()}
	}
	leastLoaded := p.opts.Strategy == LeastLoaded
	sort.SliceStable(rs, func(i, j int) bool {
		if leastLoaded && rs[i].active != rs[j].active {
			return rs[i].active < rs[j].active
		}
		return rs[i].score < rs[j].score
	})
	out := make([]*member, len(rs))
	for i, r := range rs {
		out[i] = r.m
	}
	return out
}

type trackedConn struct {
	net.Conn
	m    *member
	once sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.m.active.Add(-1) })
	return c.Conn.Close()
}

func (p *Pool) wrap(m *member, c net.Conn, took time.Duration) net.Conn {
	m.markOK(took)
	m.active.Add(1)
	m.served.Add(1)
	return &trackedConn{Conn: c, m: m}
}

// Dial opens a connection through the session the strategy selects, failing
// over to the next candidate if that session cannot connect.
func (p *Pool) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	cands := p.candidates()
	if p.opts.Strategy == Race && len(cands) > 1 {
		return p.race(ctx, cands, network, addr)
	}
	var errs []error
	for _, m := range cands {
		dctx, cancel := context.WithTimeout(ctx, p.opts.DialTimeout)
		start := time.Now()
		c, err := m.s.Dial(dctx, network, addr)
		cancel()
		if err == nil {
			return p.wrap(m, c, time.Since(start)), nil
		}
		err = fmt.Errorf("%s: %w", m.s.Name(), err)
		if errors.Is(err, ErrDestination) {
			return nil, err // the target is the problem; another tunnel won't help
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		m.markFail(p.opts.MaxFails)
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

func (p *Pool) race(ctx context.Context, cands []*member, network, addr string) (net.Conn, error) {
	rctx, cancel := context.WithTimeout(ctx, p.opts.DialTimeout)
	defer cancel()

	type result struct {
		c    net.Conn
		m    *member
		err  error
		took time.Duration
	}
	ch := make(chan result, len(cands))
	for _, m := range cands {
		go func(m *member) {
			start := time.Now()
			c, err := m.s.Dial(rctx, network, addr)
			ch <- result{c: c, m: m, err: err, took: time.Since(start)}
		}(m)
	}

	var errs []error
	for i := 0; i < len(cands); i++ {
		r := <-ch
		if r.err == nil {
			cancel()
			// Losers that still finish must not leak their connections.
			go func(remaining int) {
				for j := 0; j < remaining; j++ {
					if x := <-ch; x.err == nil {
						x.c.Close()
					}
				}
			}(len(cands) - i - 1)
			return p.wrap(r.m, r.c, r.took), nil
		}
		err := fmt.Errorf("%s: %w", r.m.s.Name(), r.err)
		if !errors.Is(err, ErrDestination) && ctx.Err() == nil && rctx.Err() == nil {
			r.m.markFail(p.opts.MaxFails)
		}
		errs = append(errs, err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, errors.Join(errs...)
}
