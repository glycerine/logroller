package logroller_test

import (
	"log"

	"github.com/glycerine/logroller"
)

// To use logroller with the standard library's log package, just pass it into
// the SetOutput function when your application starts.
func Example() {
	log.SetOutput(&logroller.Logger{
		Filename:     "/var/log/myapp/foo.log",
		MaxSizeBytes: 500 * 1024 * 1024,
		MaxBackups:   3,
		MaxAge:       28, // days

		// each new log gets this many of the original logs first lines
		PreambleLineCount: 10,
	})
}
