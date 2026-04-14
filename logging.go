package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
)

// Log levels for structured logging used by long-running commands
// (server, bridge, relay).
type logLevel int

const (
	levelDebug logLevel = iota
	levelInfo
	levelError
)

// Package-level logger state. Safe for concurrent use via logMu.
var (
	logMu        sync.Mutex
	logOut       io.Writer = os.Stderr
	logMinLevel  logLevel  = levelInfo
	logJSON      bool
	logComponent string
	logFilePath  string
	logFile      *os.File
)

// addLogFlags registers --log-level, --log-format, --log-file on a flag set.
// Returns pointers to the flag values. Call setupLogging after fs.Parse.
func addLogFlags(fs *flag.FlagSet) (level, format, file *string) {
	level = fs.String("log-level", os.Getenv("LOG_LEVEL"), "log level: debug, info, error")
	format = fs.String("log-format", os.Getenv("LOG_FORMAT"), "log format: human, json")
	file = fs.String("log-file", os.Getenv("LOG_FILE"), "log to file instead of stderr")
	return
}

// setupLogging configures the package-level logger. Must be called before
// any log output. The component name is included in every log line.
func setupLogging(level, format, file, component string) error {
	logMu.Lock()
	defer logMu.Unlock()

	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
	logOut = os.Stderr
	logFilePath = ""
	logComponent = component

	switch strings.ToLower(level) {
	case "debug":
		logMinLevel = levelDebug
	case "info", "":
		logMinLevel = levelInfo
	case "error":
		logMinLevel = levelError
	default:
		return fmt.Errorf("unknown log level %q (use debug, info, or error)", level)
	}

	switch strings.ToLower(format) {
	case "human", "text", "":
		logJSON = false
	case "json":
		logJSON = true
	default:
		return fmt.Errorf("unknown log format %q (use human or json)", format)
	}

	if file != "" {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		logOut = f
		logFile = f
		logFilePath = file
	}

	return nil
}

func reopenLogFile() error {
	logMu.Lock()
	path := logFilePath
	oldFile := logFile
	logMu.Unlock()

	if path == "" {
		return nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	logMu.Lock()
	logOut = f
	logFile = f
	logMu.Unlock()

	if oldFile != nil {
		oldFile.Close()
	}
	return nil
}

func startLogReopenWatcher() func() {
	logMu.Lock()
	hasFile := logFilePath != ""
	logMu.Unlock()
	if !hasFile {
		return func() {}
	}

	ch := make(chan os.Signal, 1)
	sighupNotify(ch)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				signal.Stop(ch)
				return
			case <-ch:
				if err := reopenLogFile(); err != nil {
					logErrorf("reopen log file: %v", err)
				} else {
					logInfof("reopened log file")
				}
			}
		}
	}()
	return func() { close(done) }
}

// logf writes a structured log entry at the given level.
func logf(level logLevel, format string, args ...interface{}) {
	if level < logMinLevel {
		return
	}

	msg := fmt.Sprintf(format, args...)
	now := time.Now().UTC()

	logMu.Lock()
	defer logMu.Unlock()

	if logJSON {
		var levelStr string
		switch level {
		case levelDebug:
			levelStr = "debug"
		case levelInfo:
			levelStr = "info"
		default:
			levelStr = "error"
		}
		json.NewEncoder(logOut).Encode(struct {
			Time      string `json:"time"`
			Level     string `json:"level"`
			Component string `json:"component"`
			Msg       string `json:"msg"`
		}{
			Time:      now.Format(time.RFC3339),
			Level:     levelStr,
			Component: logComponent,
			Msg:       msg,
		})
	} else {
		var levelStr string
		switch level {
		case levelDebug:
			levelStr = "DEBUG"
		case levelInfo:
			levelStr = "INFO "
		default:
			levelStr = "ERROR"
		}
		fmt.Fprintf(logOut, "%s %s %s: %s\n",
			now.Format("2006-01-02 15:04:05"),
			levelStr, logComponent, msg)
	}
}

func logDebugf(format string, args ...interface{}) { logf(levelDebug, format, args...) }
func logInfof(format string, args ...interface{})  { logf(levelInfo, format, args...) }
func logErrorf(format string, args ...interface{}) { logf(levelError, format, args...) }

// logFatalf logs an error message and exits with code 1.
func logFatalf(format string, args ...interface{}) {
	logf(levelError, format, args...)
	os.Exit(1)
}

// displayListenAddr normalizes a listen address for display.
// Replaces [::] (dual-stack wildcard) with 0.0.0.0 for clarity.
func displayListenAddr(s string) string {
	if strings.HasPrefix(s, "[::]:") {
		return "0.0.0.0:" + s[5:]
	}
	return s
}
