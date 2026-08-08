//go:build !windows

package shutdown

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

var (
	once     sync.Once
	exitOnce sync.Once
	done     = make(chan struct{})
)

// OnExit — на не-Windows системах достаточно обычных сигналов.
func OnExit(fn func()) <-chan struct{} {
	once.Do(func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			<-sigCh
			exitOnce.Do(func() {
				fn()
				close(done)
			})
		}()
	})
	return done
}
