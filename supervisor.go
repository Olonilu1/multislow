package multislow

import (
	"context"
	"log"
	"os"
	"os/exec"
	"time"
)

// Supervisor keeps an external tunnel client (for example dnstt-client) running,
// restarting it with backoff if it exits.
type Supervisor struct {
	name string
	argv []string
}

func NewSupervisor(name string, argv []string) *Supervisor {
	return &Supervisor{name: name, argv: argv}
}

// Run blocks until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	if len(s.argv) == 0 {
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.CommandContext(ctx, s.argv[0], s.argv[1:]...)
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second // it ran fine for a while: restart quickly
		}
		log.Printf("[%s] tunnel client exited (%v); restarting in %s", s.name, err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
