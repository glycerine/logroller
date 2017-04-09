package logroller_test

import (
	"io/ioutil"
	"log"
	"os"
	"testing"

	"github.com/glycerine/logroller"
)

var utclog *log.Logger

func init() {
	utclog = log.New(
		os.Stderr,
		"",
		log.LstdFlags|log.LUTC|log.Lmicroseconds,
	)
}

// To use logroller with the standard library's log package, just pass it into
// the SetOutput function when your application starts.
func TestFullLogging(t *testing.T) {

	org, tmp := makeAndMoveToTempDir()
	_, _ = org, tmp
	defer tempDirCleanup(org, tmp)

	utclog.SetOutput(&logroller.Logger{
		Filename:     "foo.log",
		MaxSizeBytes: 114,
		MaxBackups:   2,
		MaxAge:       2, // days

		// each new log gets this many of the original logs first lines
		PreambleLineCount: 3,
		CompressBackups:   true,
	})

	utclog.Printf("line0.")
	utclog.Printf("line1.")
	utclog.Printf("line2.")
	utclog.Printf("line3.")
	utclog.Printf("line4.") // at size 114, each line past 4 gets its own log file
	utclog.Printf("line5.")
	utclog.Printf("line6.")
	utclog.Printf("line7.")

	// comment out the defer above and manually inspect the results
}

func makeAndMoveToTempDir() (origdir string, tmpdir string) {

	// make new temp dir that will have no ".goqclusterid files in it
	var err error
	origdir, err = os.Getwd()
	if err != nil {
		panic(err)
	}
	tmpdir, err = ioutil.TempDir(origdir, "temp-logroller-test-dir")
	if err != nil {
		panic(err)
	}
	err = os.Chdir(tmpdir)
	if err != nil {
		panic(err)
	}

	return origdir, tmpdir
}

func tempDirCleanup(origdir string, tmpdir string) {
	// cleanup
	os.Chdir(origdir)
	err := os.RemoveAll(tmpdir)
	if err != nil {
		panic(err)
	}
}
