package main

import (
	"os"
	"os/signal"
	"syscall"
)

// signalNotify registers for interrupt signals.
func signalNotify(ch chan<- os.Signal) {
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
}
