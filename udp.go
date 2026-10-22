package multislow

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"time"
)

// socksUDPPort is the only UDP destination port the proxy relays: DNS.
// It is a variable so tests can point it at a free port.
var socksUDPPort atomic.Uint32

func init() { socksUDPPort.Store(53) }

// handleUDPAssociate serves a SOCKS5 UDP ASSOCIATE request. Phones send their
// DNS lookups as UDP, but SSH tunnels only carry TCP, so each DNS datagram is
// relayed as DNS-over-TCP (RFC 1035 section 4.2.2) through the dialer. All
// other UDP traffic is dropped. The association lives as long as the TCP
// control connection stays open.
func handleUDPAssociate(ctx context.Context, c net.Conn, d Dialer) {
	laddr, ok1 := c.LocalAddr().(*net.TCPAddr)
	raddr, ok2 := c.RemoteAddr().(*net.TCPAddr)
	if !ok1 || !ok2 {
		socksReply(c, repGeneralFailure)
		return
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: laddr.IP, Port: 0})
	if err != nil {
		socksReply(c, repGeneralFailure)
		return
	}
	defer pc.Close()

	bound := pc.LocalAddr().(*net.UDPAddr)
	reply := []byte{5, repSuccess, 0}
	if ip4 := bound.IP.To4(); ip4 != nil {
		reply = append(reply, 1)
		reply = append(reply, ip4...)
	} else {
		reply = append(reply, 4)
		reply = append(reply, bound.IP.To16()...)
	}
	reply = binary.BigEndian.AppendUint16(reply, uint16(bound.Port))
	c.SetDeadline(time.Time{})
	if _, err := c.Write(reply); err != nil {
		return
	}

	// When the client closes the control connection, end the association.
	go func() {
		io.Copy(io.Discard, c)
		pc.Close()
	}()

	sem := make(chan struct{}, 64) // cap on in-flight lookups
	buf := make([]byte, 65535)
	for {
		n, from, err := pc.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !from.IP.Equal(raddr.IP) {
			continue // only the client that opened the association may use it
		}
		hdrLen, host, port, ok := parseUDPHeader(buf[:n])
		if !ok || uint32(port) != socksUDPPort.Load() {
			continue
		}
		hdr := append([]byte(nil), buf[:hdrLen]...)
		query := append([]byte(nil), buf[hdrLen:n]...)
		select {
		case sem <- struct{}{}:
		default:
			continue // overloaded: drop, DNS clients retry
		}
		go func() {
			defer func() { <-sem }()
			resp, err := dnsOverTCP(ctx, d, net.JoinHostPort(host, strconv.Itoa(int(port))), query)
			if err != nil {
				return
			}
			pc.WriteToUDP(append(hdr, resp...), from)
		}()
	}
}

// parseUDPHeader decodes a SOCKS5 UDP datagram header:
// RSV(2) FRAG(1) ATYP ADDR PORT. Fragmented datagrams are not supported.
func parseUDPHeader(b []byte) (hdrLen int, host string, port uint16, ok bool) {
	if len(b) < 4 || b[0] != 0 || b[1] != 0 || b[2] != 0 {
		return
	}
	i := 4
	switch b[3] {
	case 0x01:
		if len(b) < i+4 {
			return
		}
		host = net.IP(b[i : i+4]).String()
		i += 4
	case 0x04:
		if len(b) < i+16 {
			return
		}
		host = net.IP(b[i : i+16]).String()
		i += 16
	case 0x03:
		if len(b) < i+1 {
			return
		}
		l := int(b[i])
		i++
		if len(b) < i+l {
			return
		}
		host = string(b[i : i+l])
		i += l
	default:
		return
	}
	if len(b) < i+2 {
		return
	}
	port = binary.BigEndian.Uint16(b[i:])
	i += 2
	return i, host, port, true
}

// dnsOverTCP sends one DNS query to target over a tunnelled TCP connection and
// returns the response.
func dnsOverTCP(ctx context.Context, d Dialer, target string, query []byte) ([]byte, error) {
	if len(query) == 0 || len(query) > 65535 {
		return nil, errors.New("bad DNS query size")
	}
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	c, err := d.Dial(dctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	// Tunnel connections may not support deadlines, so a timer enforces one.
	t := time.AfterFunc(8*time.Second, func() { c.Close() })
	defer t.Stop()

	msg := binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(query)), uint16(len(query)))
	msg = append(msg, query...)
	if _, err := c.Write(msg); err != nil {
		return nil, err
	}
	var l [2]byte
	if _, err := io.ReadFull(c, l[:]); err != nil {
		return nil, err
	}
	resp := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(c, resp); err != nil {
		return nil, err
	}
	return resp, nil
}
