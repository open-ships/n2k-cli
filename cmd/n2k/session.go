package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"charm.land/huh/v2"
)

type progressKey struct{}
type executionProgress struct{ count atomic.Int64 }

func countProgress(ctx context.Context) {
	if progress, ok := ctx.Value(progressKey{}).(*executionProgress); ok {
		progress.count.Add(1)
	}
}

func setProgress(ctx context.Context, count int) {
	if progress, ok := ctx.Value(progressKey{}).(*executionProgress); ok {
		progress.count.Store(int64(count))
	}
}

func (app *cli) executeInteractive(ctx context.Context, config *wizardConfig) error {
	args := config.args()
	_, _ = fmt.Fprintf(app.errOut, "\n%s\n", renderCommandPreview(args))
	if config.command == "pgn" || config.command == "update" {
		return app.ExecuteContext(ctx, args)
	}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	progress := &executionProgress{}
	runCtx = context.WithValue(runCtx, progressKey{}, progress)
	started := time.Now()
	_, _ = fmt.Fprintln(app.errOut, "Opening source. Ctrl+C stops this run and keeps captured data.")
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				count := progress.count.Load()
				state := "receiving"
				if count == 0 {
					state = "waiting for matching traffic"
				}
				if config.command == "devices" {
					state = "discovering devices"
				}
				_, _ = fmt.Fprintf(app.errOut, "  %s · %d %s · %s elapsed\n", state, count, config.progressUnit(), time.Since(started).Round(time.Second))
			}
		}
	}()
	err := app.ExecuteContext(runCtx, args)
	close(done)
	<-finished // Finish status writes before starting the next interactive form.
	count := progress.count.Load()
	state := "Finished"
	if runCtx.Err() != nil {
		state = "Stopped"
	}
	if err != nil {
		state = "Failed"
	}
	_, _ = fmt.Fprintf(app.errOut, "%s: %d %s in %s.\n", state, count, config.progressUnit(), time.Since(started).Round(time.Millisecond))
	if config.command == "record" && err == nil {
		if config.outputPath == "-" {
			_, _ = fmt.Fprintln(app.errOut, "Capture written to stdout.")
		} else {
			_, _ = fmt.Fprintf(app.errOut, "Saved capture: %s\n", expandPath(config.outputPath))
		}
	}
	if count == 0 && err == nil {
		_, _ = fmt.Fprintln(app.errOut, "No matching traffic was observed. Check the source and filter; for decoding, try including unknown PGNs.")
	}
	return err
}

func (config *wizardConfig) progressUnit() string {
	if config.command == "record" {
		return "observations saved"
	}
	if config.command == "devices" {
		return "devices"
	}
	return "messages"
}

func (app *cli) nextWorkflowAction(ctx context.Context, failed, accessible bool) (string, error) {
	action := "another"
	options := []huh.Option[string]{
		huh.NewOption("Edit settings", "edit"),
		huh.NewOption("Run again", "retry"),
		huh.NewOption("Another workflow", "another"),
		huh.NewOption("Quit", "quit"),
	}
	if failed {
		action = "edit"
		options[1] = huh.NewOption("Retry", "retry")
	}
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("What next?").Options(options...).Value(&action)))
	ok, err := app.runForm(ctx, form, accessible)
	if !ok {
		return "quit", err
	}
	return action, err
}

func freshCapturePath(now time.Time) string {
	base := "capture-" + now.Format("20060102-150405")
	for index := 0; ; index++ {
		path := base + ".log"
		if index > 0 {
			path = fmt.Sprintf("%s-%d.log", base, index)
		}
		if _, err := os.Lstat(path); err != nil {
			return path
		}
	}
}

func (app *cli) confirmOverwrite(ctx context.Context, config *wizardConfig, accessible bool) (bool, error) {
	if config.command != "record" {
		return true, nil
	}
	config.overwrite = false // Each run needs a fresh, explicit replacement choice.
	inputPath := ""
	if config.source == "file" {
		inputPath = config.file
	}
	if err := checkRecordPaths(inputPath, config.outputPath); err != nil {
		return false, err
	}
	if config.outputPath == "-" {
		return true, nil
	}
	_, err := os.Lstat(expandPath(config.outputPath))
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().
		Title("Replace the existing capture?").
		Description(fmt.Sprintf("%s already exists. Replacing it erases its current contents.", config.outputPath)).
		Affirmative("Replace").Negative("Keep file").Value(&config.overwrite)))
	ok, err := app.runForm(ctx, form, accessible)
	return ok && config.overwrite, err
}
