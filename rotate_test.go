// +build linux

package logroller_test

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/glycerine/logroller"
)

// Example of how to rotate in response to SIGHUP.
func ExampleLogger_Rotate() {
	l := &logroller.Logger{}
	log.SetOutput(l)
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)

	go func() {
		for {
			<-c
			l.Rotate()
		}
	}()
}
