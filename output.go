package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// ANSI color codes
const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cCyan   = "\033[36m"
)

var useColor bool

func initColor() {
	if os.Getenv("NO_COLOR") != "" {
		useColor = false
		return
	}
	if flagNoColor {
		useColor = false
		return
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		useColor = false
		return
	}
	useColor = fi.Mode()&os.ModeCharDevice != 0
}

// c wraps text in a color code if colors are enabled.
func c(color, text string) string {
	if !useColor {
		return text
	}
	return color + text + cReset
}

const fieldWidth = 14

// printField prints a labeled field with aligned values.
func printField(label, value string) {
	if useColor {
		fmt.Printf("  %s%-*s%s %s\n", cDim, fieldWidth, label, cReset, value)
	} else {
		fmt.Printf("  %-*s %s\n", fieldWidth, label, value)
	}
}

// printHeader prints a section header.
func printHeader(text string) {
	if useColor {
		fmt.Printf("\n%s%s%s\n", cBold, text, cReset)
	} else {
		fmt.Printf("\n%s\n", text)
	}
}

// printOK prints a success message.
func printOK(msg string) {
	if useColor {
		fmt.Printf("  %s✓%s %s\n", cGreen, cReset, msg)
	} else {
		fmt.Printf("  OK: %s\n", msg)
	}
}

// printFail prints a failure message.
func printFail(msg string) {
	if useColor {
		fmt.Printf("  %s✗%s %s\n", cRed, cReset, msg)
	} else {
		fmt.Printf("  FAIL: %s\n", msg)
	}
}

// printWarn prints a warning message.
func printWarn(msg string) {
	if useColor {
		fmt.Printf("  %s!%s %s\n", cYellow, cReset, msg)
	} else {
		fmt.Printf("  WARN: %s\n", msg)
	}
}

// fatal prints an error and exits.
func fatal(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if useColor {
		fmt.Fprintf(os.Stderr, "%serror:%s %s\n", cRed, cReset, msg)
	} else {
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
	}
	os.Exit(1)
}

// printTable prints rows with aligned columns.
func printTable(headers []string, rows [][]string) {
	if len(rows) == 0 {
		return
	}

	// Compute column widths
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	// Print header
	if useColor {
		fmt.Print("  ")
		for i, h := range headers {
			fmt.Printf("%s%-*s%s", cDim, widths[i]+2, h, cReset)
		}
		fmt.Println()
	} else {
		fmt.Print("  ")
		for i, h := range headers {
			fmt.Printf("%-*s", widths[i]+2, h)
		}
		fmt.Println()
	}

	// Print separator
	fmt.Print("  ")
	for i := range headers {
		fmt.Print(strings.Repeat("-", widths[i]))
		fmt.Print("  ")
	}
	fmt.Println()

	// Print rows
	for _, row := range rows {
		fmt.Print("  ")
		for i, cell := range row {
			if i < len(widths) {
				fmt.Printf("%-*s", widths[i]+2, cell)
			}
		}
		fmt.Println()
	}
}

// addWatchFlags registers --watch/-w and --interval flags on a FlagSet.
func addWatchFlags(fs *flag.FlagSet) (*bool, *int) {
	watch := fs.Bool("watch", false, "auto-refresh output")
	fs.BoolVar(watch, "w", false, "alias for --watch")
	interval := fs.Int("interval", 2, "refresh interval in seconds")
	return watch, interval
}

// watchLoop clears the screen and calls displayFn repeatedly until interrupted.
// If watch is false, calls displayFn once and returns.
func watchLoop(watch bool, intervalSec int, displayFn func()) {
	if !watch {
		displayFn()
		return
	}

	sigCh := make(chan os.Signal, 1)
	signalNotify(sigCh)

	for {
		// Clear screen and move cursor to top-left
		fmt.Print("\033[2J\033[H")
		displayFn()
		fmt.Printf("\n%s", c(cDim, fmt.Sprintf("  refreshing every %ds — Ctrl-C to exit", intervalSec)))

		select {
		case <-sigCh:
			fmt.Println()
			return
		case <-time.After(time.Duration(intervalSec) * time.Second):
		}
	}
}
