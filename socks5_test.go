package multislow

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// directSession dials the real network, standing in for a tunnel.
type directSession struct{}

func (directSession) Name() string { return "direct" }
func (directSession) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}
func (directSession) Ping(context.Context) (time.Duration, error) { return time.Millisecond, nil }
func (directSession) Close() error                                { return nil }

func startEcho(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(c, c)
				c.Close()
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

func startSOCKS(t *testing.T) string {
	t.Helper()
	p := mustPoolS(t, RoundRobin, directSession{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ServeSOCKS5(ctx, ln, p)
	return ln.Addr().String()
}

func socksConnect(t *testing.T, proxy string, request []byte) (net.Conn, []byte) {
	t.Helper()
	c, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte{5, 1, 0})
	greet := make([]byte, 2)
	if _, err := io.ReadFull(c, greet); err != nil || greet[0] != 5 || greet[1] != 0 {
		t.Fatalf("greeting reply %v err %v", greet, err)
	}
	c.Write(request)
	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatalf("reply: %v", err)
	}
	return c, reply
}

func echoPort(t *testing.T, ln net.Listener) (byte, byte) {
	_, ps, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(ps)
	return byte(port >> 8), byte(port)
}

func TestSOCKS5ConnectIPv4Echo(t *testing.T) {
	echo := startEcho(t)
	hi, lo := echoPort(t, echo)
	c, reply := socksConnect(t, startSOCKS(t), []byte{5, 1, 0, 1, 127, 0, 0, 1, hi, lo})
	if reply[1] != 0 {
		t.Fatalf("reply code %d, want success", reply[1])
	}
	c.Write([]byte("hello"))
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "hello" {
		t.Fatalf("echo got %q err %v", got, err)
	}
}

func TestSOCKS5ConnectDomainEcho(t *testing.T) {
	echo := startEcho(t)
	hi, lo := echoPort(t, echo)
	name := "localhost"
	req := append([]byte{5, 1, 0, 3, byte(len(name))}, name...)
	req = append(req, hi, lo)
	c, reply := socksConnect(t, startSOCKS(t), req)
	if reply[1] != 0 {
		t.Fatalf("reply code %d, want success", reply[1])
	}
	c.Write([]byte("ping"))
	got := make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo got %q err %v", got, err)
	}
}

func TestSOCKS5RejectsBind(t *testing.T) {
	_, reply := socksConnect(t, startSOCKS(t), []byte{5, 2, 0, 1, 127, 0, 0, 1, 0, 80})
	if reply[1] != repCmdUnsupported {
		t.Fatalf("reply code %d, want %d", reply[1], repCmdUnsupported)
	}
}

func TestSOCKS5DialFailureReply(t *testing.T) {
	// Grab a free port, then close it so nothing listens there.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	hi, lo := echoPort(t, ln)
	ln.Close()
	_, reply := socksConnect(t, startSOCKS(t), []byte{5, 1, 0, 1, 127, 0, 0, 1, hi, lo})
	if reply[1] == 0 {
		t.Fatal("expected a failure reply for a closed port")
	}
}
