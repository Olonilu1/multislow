# multislow

Combines up to 3 independent DNS-tunnel sessions (each with its own domain,
key and server) behind one local SOCKS5 proxy, and spreads connections across
them with health checks and failover.

```
apps -> SOCKS5 127.0.0.1:1080 -> Pool -> SSH session 1 -> dnstt-client :2201 -> DNS -> server 1
                                      -> SSH session 2 -> dnstt-client :2202 -> DNS -> server 2
                                      -> SSH session 3 -> dnstt-client :2203 -> DNS -> server 3
```

## Files

| File | Role |
| --- | --- |
| `pool.go` | Pool: strategies, health scoring, failover, race |
| `socks5.go` | Local SOCKS5 server (CONNECT, no auth, loopback only) |
| `config.go` | JSON config + validation (max 3 entries) |
| `supervisor.go` | Keeps each `dnstt-client` process running |
| `sshsession/` | SSH-over-tunnel session adapter (needs `golang.org/x/crypto`) |
| `cmd/multislow/` | Command-line entry point |

## Build and run

```
go mod tidy                      # fetches golang.org/x/crypto
go test -race ./...
go build -o multislow ./cmd/multislow
cp config.example.json config.json   # then fill in your own values
./multislow -config config.json
```

Point apps at SOCKS5 `127.0.0.1:1080` (use remote DNS / "proxy DNS").

## Strategies

- `fastest` (default): lowest smoothed latency, penalised by open connections.
- `least-loaded`: fewest open connections.
- `round-robin`: strict rotation.
- `race`: dial on all healthy sessions, keep the first. Costs extra dials.

## Limits

- One TCP connection rides one tunnel. Many parallel connections (browsing)
  use all three; one big single download does not reach 3x.
- Speed is bounded by each DNS path's query rate and payload size.
- The SOCKS5 listener has no auth, so config validation refuses non-loopback addresses.
- Each entry pins the SSH host key (`host_key_sha256`); skipping it needs an explicit opt-in.
