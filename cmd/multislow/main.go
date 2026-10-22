// Command multislow runs a local SOCKS5 proxy that spreads connections over
// several DNS-tunnel sessions at once.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"multislow"
	"multislow/sshsession"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to the JSON config")
	flag.Parse()

	cfg, err := multislow.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var sessions []multislow.Session
	for _, e := range cfg.Entries {
		if len(e.ClientCmd) > 0 {
			go multislow.NewSupervisor(e.Name, e.ClientCmd).Run(ctx)
		}
		var key []byte
		if e.SSH.KeyFile != "" {
			if key, err = os.ReadFile(e.SSH.KeyFile); err != nil {
				log.Fatalf("entry %s: %v", e.Name, err)
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
			log.Fatalf("entry %s: %v", e.Name, err)
		}
		sessions = append(sessions, s)
	}

	pool, err := multislow.NewPool(sessions, cfg.PoolOptions())
	if err != nil {
		log.Fatal(err)
	}
	pool.Start(ctx)
	defer pool.Close()

	go func() { // periodic status line
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, s := range pool.Stats() {
					log.Printf("[%s] down=%v latency=%s active=%d served=%d", s.Name, s.Down, s.Latency.Round(time.Millisecond), s.Active, s.Served)
				}
			}
		}
	}()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("SOCKS5 on %s, %d tunnels, strategy %s", cfg.Listen, len(sessions), cfg.Strategy)
	if err := multislow.ServeSOCKS5(ctx, ln, pool); err != nil {
		log.Fatal(err)
	}
}
