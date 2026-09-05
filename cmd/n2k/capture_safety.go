package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/open-ships/n2k"
)

func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

// Probe using the library's parser so accepted candump syntax stays consistent
// with replay. Stop at the first observation; never pace the probe by timestamps.
func validateCapture(ctx context.Context, path, displayPath string) error {
	file, err := os.Open(path) // #nosec G304 -- operator-selected capture.
	if err != nil {
		return fmt.Errorf("opening capture %q: %w", displayPath, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("capture %q must be a regular candump file", displayPath)
	}
	if info.Size() == 0 {
		return fmt.Errorf("capture %q is empty; record some traffic before inspecting it", displayPath)
	}
	prefix := make([]byte, 4096)
	n, err := file.Read(prefix)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	prefix = bytes.TrimSpace(prefix[:n])
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for observation, err := range n2k.Observe(probeCtx, n2k.File(path)) {
		if err != nil {
			return fmt.Errorf("reading capture %q: %w", displayPath, err)
		}
		if observation.Frame != nil {
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if bytes.HasPrefix(prefix, []byte("{")) || bytes.HasPrefix(prefix, []byte("[")) {
		return fmt.Errorf("capture %q contains JSON; replay requires candump text (record with --output-format candump)", displayPath)
	}
	return fmt.Errorf("capture %q contains no readable CAN frames; use candump -L/-l text or a gzip capture", displayPath)
}

func checkRecordPaths(inputPath, outputPath string) error {
	if inputPath == "" || outputPath == "" || outputPath == "-" {
		return nil
	}
	input, err := os.Stat(expandPath(inputPath))
	if err != nil {
		return fmt.Errorf("opening capture %q: %w", inputPath, err)
	}
	output, err := os.Stat(expandPath(outputPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking output %q: %w", outputPath, err)
	}
	if os.SameFile(input, output) {
		return errors.New("input and output refer to the same capture; choose a different output path")
	}
	return nil
}

// Recheck the opened destination before truncation, including symlinks and
// hard links that resolve to the source capture.
func checkOutputFile(inputPath string, output *os.File) error {
	info, err := output.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("output must be a regular file; use --out - to stream to stdout")
	}
	if inputPath != "" {
		input, err := os.Stat(expandPath(inputPath))
		if err != nil {
			return err
		}
		if os.SameFile(input, info) {
			return errors.New("input and output refer to the same capture; choose a different output path")
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(data)
}
