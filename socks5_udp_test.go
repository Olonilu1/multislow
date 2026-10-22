package multislow

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// startFakeDNS is a TCP DNS server that answers every query with the query
// upper-cased, so a test can tell the round trip happened.
func startFakeDNS(t *testing.T) net.Listener {
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
				defer c.Close()
				var l [2]byte
				if _, err := io.ReadFull(c, l[:]); err != nil {
					return
				}
				q := make([]byte, binary.BigEndian.Uint16(l[:]))
				if _, err := io.ReadFull(c, q); err != nil {
					return
				}
				c.Write(append(l[:], bytes.ToUpper(q)...))
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

func udpAssociate(t *testing.T, dnsPort uint32) *net.UDPConn {
	t.Helper()
	socksUDPPort.Store(dnsPort)
	t.Cleanup(func() { socksUDPPort.Store(53) })
	_, reply := socksConnect(t, startSOCKS(t), []byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	if reply[1] != 0 {
		t.Fatalf("UDP ASSOCIATE reply code %d", reply[1])
	}
	relay := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:10]))}
	u, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close() })
	return u
}

func TestSOCKS5UDPRelaysDNSOverTCP(t *testing.T) {
	dns := startFakeDNS(t)
	hi, lo := echoPort(t, dns)
	u := udpAssociate(t, uint32(hi)<<8|uint32(lo))

	hdr := []byte{0, 0, 0, 1, 127, 0, 0, 1, hi, lo}
	u.SetDeadline(time.Now().Add(3 * time.Second))
	u.Write(append(append([]byte{}, hdr...), "query"...))
	buf := make([]byte, 512)
	n, err := u.Read(buf)
	if err != nil {
		t.Fatalf("no DNS answer: %v", err)
	}
	want := append(append([]byte{}, hdr...), "QUERY"...)
	if !bytes.Equal(buf[:n], want) {
		t.Fatalf("got %v, want %v", buf[:n], want)
	}
}

func TestSOCKS5UDPDropsNonDNSPorts(t *testing.T) {
	dns := startFakeDNS(t)
	hi, lo := echoPort(t, dns)
	u := udpAssociate(t, uint32(hi)<<8|uint32(lo))

	// Same server, but a different destination port: must be dropped.
	u.Write(append([]byte{0, 0, 0, 1, 127, 0, 0, 1, 0x27, 0x0F}, "query"...))
	u.SetDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := u.Read(make([]byte, 512)); !os.IsTimeout(err) {
		t.Fatalf("expected a timeout (dropped datagram), got %v", err)
	}
}
