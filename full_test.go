package logroller_test

import (
	"io/ioutil"
	"log"
	"os"
	"testing"

	"github.com/glycerine/logroller"
)

// To use logroller with the standard library's log package, just pass it into
// the SetOutput function when your application starts.
func TestFullLogging(t *testing.T) {

	org, tmp := makeAndMoveToTempDir()
	_, _ = org, tmp
	defer tempDirCleanup(org, tmp)

	log.SetOutput(&logroller.Logger{
		Filename:     "foo.log",
		MaxSizeBytes: 30,
		MaxBackups:   3,
		MaxAge:       2, // days

		// each new log gets this many of the original logs first lines
		PreambleLineCount: 3,
		CompressBackups:   true,
	})

	log.Printf("line0.")
	log.Printf("line1.")
	log.Printf("line2.")
	log.Printf("line3.")
	log.Printf("line4")
	log.Printf("line5")
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
