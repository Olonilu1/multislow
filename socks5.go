package multislow

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"time"
)

// Dialer is anything that can open outbound connections (the Pool, normally).
type Dialer interface {
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
}

// ServeSOCKS5 accepts SOCKS5 clients on ln (no authentication; CONNECT, plus
// UDP ASSOCIATE for DNS only) and tunnels each one through d. Domain names are passed to the tunnel
// unresolved, so DNS lookups happen at the far end, not on the device.
//
// There is no authentication: only listen on a loopback address.
func ServeSOCKS5(ctx context.Context, ln net.Listener, d Dialer) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Printf("socks5: accept: %v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go handleSOCKS5(ctx, c, d)
	}
}

const (
	repSuccess         = 0x00
	repGeneralFailure  = 0x01
	repHostUnreachable = 0x04
	repCmdUnsupported  = 0x07
	repAddrUnsupported = 0x08
)

func socksReply(c net.Conn, code byte) {
	c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
}

func handleSOCKS5(ctx context.Context, c net.Conn, d Dialer) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second)) // handshake deadline

	// Greeting: VER NMETHODS METHODS...
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	noAuth := false
	for _, m := range methods {
		if m == 0x00 {
			noAuth = true
		}
	}
	if !noAuth {
		c.Write([]byte{5, 0xFF})
		return
	}
	c.Write([]byte{5, 0x00})

	// Request: VER CMD RSV ATYP DST.ADDR DST.PORT
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 {
		return
	}
	var host string
	switch req[3] {
	case 0x01:
		var ip [4]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 0x04:
		var ip [16]byte
		if _, err := io.ReadFull(c, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return
		}
		host = string(name)
	default:
		socksReply(c, repAddrUnsupported)
		return
	}
	var pb [2]byte
	if _, err := io.ReadFull(c, pb[:]); err != nil {
		return
	}
	switch req[1] {
	case 0x01: // CONNECT
	case 0x03: // UDP ASSOCIATE: relays DNS only, see udp.go
		handleUDPAssociate(ctx, c, d)
		return
	default:
		socksReply(c, repCmdUnsupported)
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb[:]))))

	up, err := d.Dial(ctx, "tcp", target)
	if err != nil {
		if errors.Is(err, ErrDestination) {
			socksReply(c, repHostUnreachable)
		} else {
			socksReply(c, repGeneralFailure)
		}
		return
	}
	defer up.Close()
	socksReply(c, repSuccess)
	c.SetDeadline(time.Time{})
	relay(c, up)
}

// relay copies both directions until both are done. If a side cannot
// half-close, it is closed fully so the opposite copy unblocks.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
