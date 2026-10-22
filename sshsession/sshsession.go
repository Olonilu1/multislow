// Package sshsession adapts one tunnel to the multislow.Session interface: it
// logs in over SSH to a local port where a DNS-tunnel client (dnstt style) is
// listening, and opens onward connections as SSH direct-tcpip channels.
package sshsession

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"multislow"
)

// Config describes one SSH-over-tunnel session.
type Config struct {
	Name string
	// Addr is the local address the DNS-tunnel client listens on.
	Addr       string
	User       string
	Password   string
	PrivateKey []byte
	// HostKeySHA256 pins the server's key ("SHA256:...").
	HostKeySHA256 string
	// InsecureSkipHostKey disables pinning. Opt-in only.
	InsecureSkipHostKey bool
	ConnectTimeout      time.Duration
}

// Session is a reconnecting SSH client that satisfies multislow.Session.
type Session struct {
	cfg  Config
	auth []ssh.AuthMethod
	hk   ssh.HostKeyCallback

	mu     sync.Mutex
	client *ssh.Client
	closed bool
}

func New(cfg Config) (*Session, error) {
	var auth []ssh.AuthMethod
	if len(cfg.PrivateKey) > 0 {
		signer, err := ssh.ParsePrivateKey(cfg.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("%s: private key: %w", cfg.Name, err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		auth = append(auth, ssh.Password(cfg.Password))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("%s: no password or key given", cfg.Name)
	}

	var hk ssh.HostKeyCallback
	switch {
	case cfg.HostKeySHA256 != "":
		want := cfg.HostKeySHA256
		hk = func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if got := ssh.FingerprintSHA256(key); got != want {
				return fmt.Errorf("host key mismatch: got %s, want %s", got, want)
			}
			return nil
		}
	case cfg.InsecureSkipHostKey:
		hk = ssh.InsecureIgnoreHostKey()
	default:
		return nil, fmt.Errorf("%s: pin a host key or opt in to skipping the check", cfg.Name)
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 20 * time.Second
	}
	return &Session{cfg: cfg, auth: auth, hk: hk}, nil
}

func (s *Session) Name() string { return s.cfg.Name }

// conn returns the live SSH client, connecting if there is none.
func (s *Session) conn(ctx context.Context) (*ssh.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("session closed")
	}
	if s.client != nil {
		return s.client, nil
	}
	dctx, cancel := context.WithTimeout(ctx, s.cfg.ConnectTimeout)
	defer cancel()
	var d net.Dialer
	nc, err := d.DialContext(dctx, "tcp", s.cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("tunnel client at %s: %w", s.cfg.Addr, err)
	}
	// The SSH handshake has no context hook; a deadline bounds it instead.
	nc.SetDeadline(time.Now().Add(s.cfg.ConnectTimeout))
	sc, chans, reqs, err := ssh.NewClientConn(nc, s.cfg.Addr, &ssh.ClientConfig{
		User:            s.cfg.User,
		Auth:            s.auth,
		HostKeyCallback: s.hk,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	nc.SetDeadline(time.Time{})
	c := ssh.NewClient(sc, chans, reqs)
	s.client = c
	go func() { // forget the client when its connection dies
		c.Wait()
		s.mu.Lock()
		if s.client == c {
			s.client = nil
		}
		s.mu.Unlock()
	}()
	return c, nil
}

func (s *Session) reset(c *ssh.Client) {
	s.mu.Lock()
	if s.client == c {
		s.client = nil
	}
	s.mu.Unlock()
	c.Close()
}

type dialResult struct {
	c   net.Conn
	err error
}

// Dial opens network/addr from the SSH server's side. One reconnect attempt is
// made if the existing SSH connection turns out to be dead.
func (s *Session) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		cl, err := s.conn(ctx)
		if err != nil {
			return nil, err
		}
		ch := make(chan dialResult, 1)
		go func() {
			c, err := cl.Dial(network, addr)
			ch <- dialResult{c, err}
		}()
		select {
		case r := <-ch:
			if r.err == nil {
				return r.c, nil
			}
			var oce *ssh.OpenChannelError
			if errors.As(r.err, &oce) {
				// The server answered but refused/couldn't reach the target.
				return nil, fmt.Errorf("%w: %v", multislow.ErrDestination, r.err)
			}
			lastErr = r.err
			s.reset(cl) // transport-level failure: reconnect and retry once
		case <-ctx.Done():
			go func() { // don't leak a connection that finishes late
				if r := <-ch; r.c != nil {
					r.c.Close()
				}
			}()
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

// Ping sends an SSH keepalive and reports its round-trip time. A failed or
// timed-out ping drops the SSH connection so the next call reconnects.
func (s *Session) Ping(ctx context.Context) (time.Duration, error) {
	cl, err := s.conn(ctx)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	ch := make(chan error, 1)
	go func() {
		_, _, err := cl.SendRequest("keepalive@openssh.com", true, nil)
		ch <- err
	}()
	select {
	case err := <-ch:
		if err != nil {
			s.reset(cl)
			return 0, err
		}
		return time.Since(start), nil
	case <-ctx.Done():
		s.reset(cl)
		return 0, ctx.Err()
	}
}

func (s *Session) Close() error {
	s.mu.Lock()
	s.closed = true
	c := s.client
	s.client = nil
	s.mu.Unlock()
	if c != nil {
		return c.Close()
	}
	return nil
}
