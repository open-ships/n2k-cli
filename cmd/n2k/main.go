// Command n2k is the NMEA 2000 command-line tool: decode, capture, replay,
// validate, and inspect NMEA 2000 traffic without writing Go code.
//
// Run n2k without arguments for the interactive command center, or use a
// subcommand directly in scripts:
//
//	n2k sniff --tcp 192.168.4.1:1457
//	n2k record -i can0 --out capture.log
//	n2k replay capture.log
//
// Install with: go install github.com/open-ships/n2k-cli/cmd/n2k@latest
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// Version metadata, overridden at release time via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	app := newCLI(os.Stdin, os.Stdout, os.Stderr)
	if err := app.ExecuteContext(ctx, os.Args[1:]); err != nil {
		if ctx.Err() != nil {
			return
		}
		_, _ = fmt.Fprintf(app.errOut, "n2k: %v\n", err)
		os.Exit(1)
	}
}
