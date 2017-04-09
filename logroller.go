// Package logroller descends from Nate Finch's lumberjack.v2
// and provides a similar rolling logger, with three additions:
//
// 1) gzip compression of logs, from https://github.com/natefinch/lumberjack/pull/16 and donovansolms:v2.0
//
// 2) a separate log directory, from https://github.com/natefinch/lumberjack/pull/39 and GJRTimmer:archive
//
// 3) a fixed number of preamble lines that are copied from the first
//    log to every subsequent rotated log, in order to capture
//    version, config info, and command line args.
//
//
//   import "github.com/glycerine/logroller"
//
//
// Logroller is intended to be one part of a logging infrastructure.
// It is not an all-in-one solution, but instead is a pluggable
// component at the bottom of the logging stack that simply controls the files
// to which logs are written.
//
// Logroller plays well with any logging package that can write to an
// io.Writer, including the standard library's log package.
//
// Logroller assumes that only one process is writing to the output files.
// Using the same logroller configuration from multiple processes on the same
// machine will result in improper behavior.
package logroller

import (
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	backupTimeFormat      = time.RFC3339Nano //"2006-01-02T15-04-05.000"
	defaultMaxSize        = 100 * Megabyte
	compressFileExtension = ".gz"
)

// ensure we always implement io.WriteCloser
var _ io.WriteCloser = (*Logger)(nil)

// Logger is an io.WriteCloser that writes to the specified filename.
//
// Logger opens or creates the logfile on first Write.  If the file exists and
// is less than MaxSizeBytes, logroller will open and append to that file.
// If the file exists and its size is >= MaxSizeBytes, the file is renamed
// by putting the current time in a timestamp in the name immediately before the
// file's extension (or the end of the filename if there's no extension). A new
// log file is then created using original filename.
//
// Whenever a write would cause the current log file exceed MaxSizeBytes,
// the current file is closed, renamed, and a new log file created with the
// original name. Thus, the filename you give Logger is always the "current" log
// file.
//
// Backups use the log file name given to Logger, in the form
// `name-timestamp.ext` where name is the filename without the extension,
// timestamp is the time at which the log was rotated formatted with the
// time.Time format of `2006-01-02T15-04-05.000` and the extension is the
// original extension.  For example, if your Logger.Filename is
// `/var/log/foo/server.log`, a backup created at 6:30pm on Nov 11 2016 would
// use the filename `/var/log/foo/server-2016-11-04T18-30-00.000.log`
//
// Cleaning Up Old Log Files
//
// Whenever a new logfile gets created, old log files may be deleted.  The most
// recent files according to the encoded timestamp will be retained, up to a
// number equal to MaxBackups (or all of them if MaxBackups is 0).  Any files
// with an encoded timestamp older than MaxAge days are deleted, regardless of
// MaxBackups.  Note that the time encoded in the timestamp is the rotation
// time, which may differ from the last time that file was written to.
//
// If MaxBackups and MaxAge are both 0, no old log files will be deleted.
type Logger struct {
	// Filename is the file to write logs to.  Backup log files will be retained
	// in the same directory.  It uses <processname>-logroller.log in
	// os.TempDir() if empty.
	Filename string `json:"filename" yaml:"filename"`

	// ArchiveDir is the directory where to write the rotated logs to.
	// If not set it will default to the current directory of the logfile.
	// Logroller will assume the archive directory already exists.
	ArchiveDir string `json:"archivedir,omitempty" yaml:"archivedir,omitempty"`

	// MaxSizeBytes is the maximum size in bytes of the log file before it gets
	// rotated. It defaults to 100 megabytes.
	MaxSizeBytes int `json:"maxsizebytes" yaml:"maxsizebytes"`

	// MaxAge is the maximum number of days to retain old log files based on the
	// timestamp encoded in their filename.  Note that a day is defined as 24
	// hours and may not exactly correspond to calendar days due to daylight
	// savings, leap seconds, etc. The default is not to remove old log files
	// based on age.
	MaxAge int `json:"maxage" yaml:"maxage"`

	// MaxBackups is the maximum number of old log files to retain.  The default
	// is to retain all old log files (though MaxAge may still cause them to get
	// deleted.)
	MaxBackups int `json:"maxbackups" yaml:"maxbackups"`

	// CompressBackups gzips the old log files specified by MaxAge and MaxBackups.
	// The default is to leave backups uncompressed.
	CompressBackups bool `json:"compressbackups" yaml:"compressbackups"`

	// LocalTime determines if the time used for formatting the timestamps in
	// backup files is the computer's local time.  The default is to use UTC
	// time.
	LocalTime bool `json:"localtime" yaml:"localtime"`

	// PreambleLineCount sets the max number of lines
	// that we store in the preamble. If left at the default
	// of zero, then no Preamble will be created or replayed.
	PreambleLineCount int

	// Preamble records the first N logged lines for replay at
	// the top of every new log file, where N is PreambleLineCount.
	Preamble []string

	size int64
	file *os.File
	mu   sync.Mutex
	cmu  sync.Mutex
}

const Megabyte = 1024 * 1024

var (
	// currentTime exists so it can be mocked out by tests.
	currentTime = time.Now

	// os_Stat exists so it can be mocked out by tests.
	os_Stat = os.Stat
)

// Write implements io.Writer.  If a write would cause the log file to be larger
// than MaxSize, the file is closed, renamed to include a timestamp of the
// current time, and a new log file is created using the original log file name.
// If the length of the write is greater than MaxSize, an error is returned.
func (l *Logger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	writeLen := int64(len(p))
	if writeLen > l.max() {
		return 0, fmt.Errorf(
			"write length %d exceeds maximum file size %d", writeLen, l.max(),
		)
	}

	if l.file == nil {
		if err = l.openExistingOrNew(len(p)); err != nil {
			return 0, err
		}
		if l.CompressBackups {
			l.compressLogs()
		}
	}

	if l.size+writeLen > l.max() {
		if err := l.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = l.file.Write(p)
	l.size += int64(n)
	//fmt.Printf("Write wrote %v '%s' to file %s\n", n, string(p), l.file.Name())

	if len(l.Preamble) < l.PreambleLineCount {
		l.Preamble = append(l.Preamble, string(p))
	}

	return n, err
}

// Close implements io.Closer, and closes the current logfile.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.close()
}

// close closes the file if it is open.
func (l *Logger) close() error {
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// Rotate causes Logger to close the existing log file and immediately create a
// new one.  This is a helper function for applications that want to initiate
// rotations outside of the normal rotation rules, such as in response to
// SIGHUP.  After rotating, this initiates a cleanup of old log files according
// to the normal rules.
func (l *Logger) Rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rotate()
}

// rotate closes the current file, moves it aside with a timestamp in the name,
// (if it exists), opens a new file with the original filename, and then runs
// cleanup.
func (l *Logger) rotate() error {
	//fmt.Printf("rotate() happening\n")
	if err := l.close(); err != nil {
		return err
	}

	if err := l.openNew(); err != nil {
		return err
	}
	return l.cleanup()
}

// openNew opens a new log file for writing, moving any old log file out of the
// way.  This methods assumes the file has already been closed.
func (l *Logger) openNew() error {
	err := os.MkdirAll(l.currentLogDir(), 0744)
	if err != nil {
		return fmt.Errorf("can't make directory for new logfile: %s", err)
	}
	err = os.MkdirAll(l.archiveDir(), 0744)
	if err != nil {
		return fmt.Errorf("can't make directory for rotated logfiles: %s", err)
	}
	name := l.filename()

	mode := os.FileMode(0644)
	info, err := os_Stat(name)
	if err == nil {
		// Copy the mode off the old logfile.
		mode = info.Mode()
		// move the existing file
		newname := backupName(name, l.archiveDir(), l.LocalTime)
		if err := os.Rename(name, newname); err != nil {
			return fmt.Errorf("can't rename log file: %s", err)
		}
		//fmt.Printf("openNew has renamed %s -> %s\n", name, newname)

		// this is a no-op anywhere but linux
		if err := chown(name, info); err != nil {
			return err
		}
	}

	// we use truncate here because this should only get called when we've moved
	// the file ourselves. if someone else creates the file in the meantime,
	// just wipe out the contents.
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("can't open new logfile: %s", err)
	}
	l.file = f
	l.size = 0

	// replay the Preamble, so that the original version/config
	// lines (the first l.PreambleLineCount lines logged) are retained at
	// the top of each log file.
	if len(l.Preamble) > 0 {
		for i := range l.Preamble {
			writeme := []byte(l.Preamble[i])
			n, err := l.file.Write(writeme)
			l.size += int64(n)
			if err != nil {
				return err
			}
		}
		n, err := l.file.WriteString("___***___END_OF_PREAMBLE___***___\n")
		l.size += int64(n)
		if err != nil {
			return err
		}
	}

	return nil
}

// backupName creates a new filename from the given name, inserting a timestamp
// between the filename and the extension, using the local time if requested
// (otherwise UTC).
func backupName(name, archiveDir string, local bool) string {
	dir := filepath.Dir(name)
	if len(archiveDir) > 0 {
		dir = archiveDir
	}
	filename := filepath.Base(name)
	ext := filepath.Ext(filename)
	prefix := filename[:len(filename)-len(ext)]
	t := currentTime()
	if !local {
		t = t.UTC()
	}

	timestamp := t.Format(backupTimeFormat)
	return filepath.Join(dir, fmt.Sprintf("%s-%s%s", prefix, timestamp, ext))
}

// openExistingOrNew opens the logfile if it exists and if the current write
// would not put it over MaxSize.  If there is no such file or the write would
// put it over the MaxSize, a new file is created.
func (l *Logger) openExistingOrNew(writeLen int) error {
	filename := l.filename()
	info, err := os_Stat(filename)
	if os.IsNotExist(err) {
		return l.openNew()
	}
	if err != nil {
		return fmt.Errorf("error getting log file info: %s", err)
	}

	if info.Size()+int64(writeLen) >= l.max() {
		return l.rotate()
	}

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		// if we fail to open the old log file for some reason, just ignore
		// it and open a new log file.
		return l.openNew()
	}
	l.file = file
	l.size = info.Size()

	return nil
}

// genFilename generates the name of the logfile from the current time.
func (l *Logger) filename() string {
	if l.Filename != "" {
		return l.Filename
	}
	name := filepath.Base(os.Args[0]) + "-logroller.log"
	return filepath.Join(os.TempDir(), name)
}

// cleanup deletes old log files, keeping at most l.MaxBackups files, as long as
// none of them are older than MaxAge.
func (l *Logger) cleanup() error {
	if l.CompressBackups {
		l.compressLogs()
	}

	if l.MaxBackups == 0 && l.MaxAge == 0 {
		return nil
	}

	files, err := l.oldLogFiles(l.CompressBackups)
	if err != nil {
		return err
	}

	var deletes []logInfo

	if l.MaxBackups > 0 && l.MaxBackups < len(files) {
		deletes = files[l.MaxBackups:]
		files = files[:l.MaxBackups]
	}
	if l.MaxAge > 0 {
		diff := time.Duration(int64(24*time.Hour) * int64(l.MaxAge))

		cutoff := currentTime().Add(-1 * diff)

		for _, f := range files {
			if f.timestamp.Before(cutoff) {
				deletes = append(deletes, f)
			}
		}
	}

	if len(deletes) == 0 {
		return nil
	}

	go deleteAll(l.archiveDir(), deletes)

	return nil
}

func deleteAll(dir string, files []logInfo) {
	// remove files on a separate goroutine
	for _, f := range files {
		// what am I going to do, log this?
		_ = os.Remove(filepath.Join(dir, f.Name()))
	}
}

// compressLogs compresses any uncompressed logs during the cleanup process
func (l *Logger) compressLogs() {
	l.cmu.Lock()
	defer l.cmu.Unlock()
	files, err := l.oldLogFiles(false)
	if err != nil {
		fmt.Errorf("Unable to compress log files: %s", err)
	}

	for _, file := range files {
		_, ext := l.prefixAndExt()
		if ext != compressFileExtension {
			if err := compressLog(filepath.Join(l.archiveDir(), file.Name())); err != nil {
				fmt.Errorf("Unable to compress backup log file: %s", err)
			}
		}
	}
}

// oldLogFiles returns the list of backup log files stored in the same
// directory as the current log file, sorted by ModTime. Setting
// includeCompressed to true will include files with the given
// compressFileExtension into the returned list
func (l *Logger) oldLogFiles(includeCompressed bool) ([]logInfo, error) {
	files, err := ioutil.ReadDir(l.archiveDir())
	if err != nil {
		return nil, fmt.Errorf("can't read log file directory: %s", err)
	}
	logFiles := []logInfo{}

	prefix, ext := l.prefixAndExt()

	if includeCompressed {
		ext = ext + compressFileExtension
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := l.timeFromName(f.Name(), prefix, ext)
		if name == "" {
			continue
		}
		t, err := time.Parse(backupTimeFormat, name)
		if err == nil {
			logFiles = append(logFiles, logInfo{t, f})
		}
		// error parsing means that the suffix at the end was not generated
		// by logroller, and therefore it's not a backup file.
	}

	sort.Sort(byFormatTime(logFiles))

	return logFiles, nil
}

// compressLog compresses the log with given filename using Gzip compression
func compressLog(filename string) error {

	reader, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer reader.Close()

	writer, err := os.Create(filename + compressFileExtension)
	if err != nil {
		return err
	}
	defer writer.Close()

	gzwriter := gzip.NewWriter(writer)
	defer gzwriter.Close()

	if _, err := io.Copy(gzwriter, reader); err != nil {
		return err
	}

	// Explicitly closing the reader in addition to defer reader.Close so that
	// we don't get 'file is being used by another process' errors on Windows
	reader.Close()
	if err := os.Remove(filename); err != nil {
		return err
	}
	return nil
}

// timeFromName extracts the formatted time from the filename by stripping off
// the filename's prefix and extension. This prevents someone's filename from
// confusing time.parse.
func (l *Logger) timeFromName(filename, prefix, ext string) string {
	if !strings.HasPrefix(filename, prefix) {
		return ""
	}
	filename = filename[len(prefix):]

	if !strings.HasSuffix(filename, ext) {
		return ""
	}
	filename = filename[:len(filename)-len(ext)]
	return filename
}

// max returns the maximum size in bytes of log files before rolling.
func (l *Logger) max() int64 {
	if l.MaxSizeBytes == 0 {
		return int64(defaultMaxSize)
	}
	return int64(l.MaxSizeBytes)
}

// archiveDir returns the archive directory for the current filename.
// if ArchiveDir is set it will return the archive directory
// so the old log files will be located at the correct location
func (l *Logger) archiveDir() string {
	if len(l.ArchiveDir) > 0 {
		return l.ArchiveDir
	}
	return l.filename() + ".rotated"
}

func (l *Logger) currentLogDir() string {
	return filepath.Dir(l.filename())
}

// prefixAndExt returns the filename part and extension part from the Logger's
// filename.
func (l *Logger) prefixAndExt() (prefix, ext string) {
	filename := filepath.Base(l.filename())
	ext = filepath.Ext(filename)
	prefix = filename[:len(filename)-len(ext)] + "-"
	return prefix, ext
}

// logInfo is a convenience struct to return the filename and its embedded
// timestamp.
type logInfo struct {
	timestamp time.Time
	os.FileInfo
}

// byFormatTime sorts by newest time formatted in the name.
type byFormatTime []logInfo

func (b byFormatTime) Less(i, j int) bool {
	return b[i].timestamp.After(b[j].timestamp)
}

func (b byFormatTime) Swap(i, j int) {
	b[i], b[j] = b[j], b[i]
}

func (b byFormatTime) Len() int {
	return len(b)
}
