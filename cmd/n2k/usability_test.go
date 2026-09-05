package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/open-ships/n2k"
	"github.com/stretchr/testify/require"
)

func TestRecordRequiresExplicitOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.log")
	original := []byte("previous capture\n")
	require.NoError(t, os.WriteFile(path, original, 0o600))
	args := []string{"record", "--file", "../../testdata/sample.log", "--out", path}
	_, _, err := executeCommand(context.Background(), args...)
	require.ErrorContains(t, err, "--overwrite")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, data)
	_, _, err = executeCommand(context.Background(), append(args, "--overwrite")...)
	require.NoError(t, err)
	_, _, err = executeCommand(context.Background(), "replay", path, "--timing=false")
	require.NoError(t, err)
}

func TestRecordNeverOverwritesItsInput(t *testing.T) {
	for _, alias := range []string{"same path", "hard link", "symbolic link", "gzip"} {
		t.Run(alias, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "source.log")
			data, err := os.ReadFile("../../testdata/sample.log")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(input, data, 0o600))
			output := input
			switch alias {
			case "hard link", "symbolic link":
				output = filepath.Join(dir, "alias.log")
				if alias == "hard link" {
					err = os.Link(input, output)
				} else {
					err = os.Symlink(input, output)
				}
				if err != nil {
					t.Skipf("link unavailable: %v", err)
				}
			case "gzip":
				input = gzipTestCapture(t, input)
				output = input
				data, err = os.ReadFile(input)
				require.NoError(t, err)
			}
			_, _, err = executeCommand(context.Background(), "record", "--file", input, "--out", output, "--overwrite")
			require.ErrorContains(t, err, "same capture")
			remaining, err := os.ReadFile(input)
			require.NoError(t, err)
			require.Equal(t, data, remaining)
		})
	}
}

func TestCapturePreflightExplainsUnreadableInput(t *testing.T) {
	for _, test := range []struct{ name, body, want string }{
		{"empty", "", "is empty"},
		{"text", "this is not a capture\n", "no readable CAN frames"},
		{"json", "  {\"kind\":\"frame\"}\n", "contains JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "capture.log")
			require.NoError(t, os.WriteFile(path, []byte(test.body), 0o600))
			for _, input := range []string{path, gzipTestCapture(t, path)} {
				for _, command := range []string{"sniff", "replay", "validate", "devices"} {
					stdout, _, err := executeCommand(context.Background(), command, "--file", input)
					require.ErrorContains(t, err, test.want, command)
					require.Empty(t, stdout)
				}
			}
		})
	}
}

func TestJSONExportIsExplicitlyRejectedByReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	_, _, err := executeCommand(context.Background(), "record", "--file", "../../testdata/sample.log", "--out", path, "--output-format", "jsonl")
	require.NoError(t, err)
	_, _, err = executeCommand(context.Background(), "replay", path)
	require.ErrorContains(t, err, "--output-format candump")
}

func TestCapturePreflightAllowsCommentsBeforeFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commented.log")
	sample, err := os.ReadFile("../../testdata/sample.log")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append([]byte("[capture notes]\n"), sample...), 0o600))
	stdout, _, err := executeCommand(context.Background(), "sniff", "--file", path, "--output", "text")
	require.NoError(t, err)
	require.Contains(t, stdout, "WaterDepth")
}

func TestInvalidSourceDoesNotReplaceExistingRecording(t *testing.T) {
	dir := t.TempDir()
	input, output := filepath.Join(dir, "invalid.log"), filepath.Join(dir, "keep.log")
	require.NoError(t, os.WriteFile(input, []byte("invalid capture"), 0o600))
	require.NoError(t, os.WriteFile(output, []byte("keep this"), 0o600))
	_, _, err := executeCommand(context.Background(), "record", "--file", input, "--out", output, "--overwrite")
	require.Error(t, err)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "keep this", string(data))
}

func TestFilterAndAddressErrorsPrecedeSourceIO(t *testing.T) {
	for _, filter := range []string{"pgn = 127250", "pgn", "unknown_variable == 1"} {
		_, _, err := executeCommand(context.Background(), "sniff", "--file", "missing.log", "--filter", filter)
		require.ErrorContains(t, err, "filter")
		require.NotContains(t, err.Error(), "opening capture")
	}
	for _, expression := range []string{"", "pgn == 127250", "msg.heading > 1 && source == 11"} {
		require.NoError(t, validateFilter(expression))
	}
	for _, address := range []string{"gateway", "localhost:0", "localhost:99999", ":1457"} {
		require.Error(t, addressValidator("TCP")(address))
	}
	require.NoError(t, addressValidator("UDP")(":1457"))
	require.NoError(t, addressValidator("TCP")("[::1]:1457"))
	_, _, err := executeCommand(context.Background(), "sniff", "--file", "--output", "text")
	require.ErrorContains(t, err, "--file requires <path>")
}

func TestPaletteEscapeClearsAppliedFilterBeforeQuitting(t *testing.T) {
	model := newPaletteModel()
	model.list.SetFilterText("pgn")
	model.list.SetFilterState(list.FilterApplied)
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(paletteModel)
	require.False(t, model.canceled)
	require.Equal(t, list.Unfiltered, model.list.FilterState())
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	require.True(t, updated.(paletteModel).canceled)
}

func TestFilteredPaletteDoesNotOpenHiddenNumberShortcut(t *testing.T) {
	model := newPaletteModel()
	model.list.SetFilterText("pgn")
	model.list.SetFilterState(list.FilterApplied)
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: '3', Text: "3"}))
	require.Empty(t, updated.(paletteModel).selected)
}

func TestWizardTabCompletesWithoutSubmittingPartialInput(t *testing.T) {
	value := "test"
	input := huh.NewInput().Value(&value).Suggestions([]string{"testdata/sample.log"}).Validate(existingPath)
	input.WithKeyMap(wizardKeyMap())
	input.Focus()
	updated, _ := input.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	require.Equal(t, "testdata/sample.log", value)
	require.NotContains(t, updated.View(), "cannot read")
}

func TestFormEscapeLetsSearchHandleItsOwnKey(t *testing.T) {
	field := huh.NewSelect[string]().Options(huh.NewOption("Vessel Heading", "127250"))
	form := huh.NewForm(huh.NewGroup(field))
	filter := formKeyFilter(form)
	field.Focus()
	field.Update(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	escape := tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	require.Equal(t, "esc", filter(nil, escape).(tea.KeyPressMsg).String())
	field.Focus()
	field.Update(tea.KeyPressMsg(tea.Key{Code: 'h', Text: "h"}))
	field.Update(escape) // Finish entering the filter.
	require.Equal(t, "esc", filter(nil, escape).(tea.KeyPressMsg).String())
	field.Update(escape) // Clear the applied filter.
	require.Equal(t, "ctrl+c", filter(nil, escape).(tea.KeyPressMsg).String())
}

func TestCompactPaletteKeepsControlsInsideTerminal(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 12}, {30, 10}, {20, 6}} {
		model := newPaletteModel()
		updated, _ := model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := updated.(paletteModel).View().Content
		require.LessOrEqual(t, lipgloss.Width(view), size[0])
		require.LessOrEqual(t, lipgloss.Height(view), size[1])
		if size[0] >= 30 {
			require.Contains(t, view, "enter")
			require.Contains(t, view, "quit")
		}
	}
}

func TestReadableInspectionAndJSONDefaults(t *testing.T) {
	stdout, _, err := executeCommand(context.Background(), "pgn", "127250", "--output", "text")
	require.NoError(t, err)
	require.Contains(t, stdout, "Vessel Heading")
	require.Contains(t, stdout, "Heading")
	require.Less(t, len(strings.Split(stdout, "\n")), 25)
	stdout, _, err = executeCommand(context.Background(), "pgn", "heading", "--output", "text")
	require.NoError(t, err)
	require.Contains(t, stdout, "127250")
	stdout, _, err = executeCommand(context.Background(), "pgn", "127250")
	require.NoError(t, err)
	require.True(t, json.Valid([]byte(stdout)))
	stdout, _, err = executeCommand(context.Background(), "devices", "--file", "../../testdata/sample.log", "--output", "text")
	require.NoError(t, err)
	require.Contains(t, stdout, "MANUFACTURER")
	require.Contains(t, stdout, "LAST SEEN")
	stdout, _, err = executeCommand(context.Background(), "validate", "--file", "../../testdata/sample.log")
	require.NoError(t, err)
	var summary validationSummary
	require.NoError(t, json.Unmarshal([]byte(stdout), &summary))
	total := 0
	for _, count := range summary.UndecodableByPGN {
		total += count
	}
	require.Equal(t, summary.Undecodable, total)
	require.Positive(t, total)
	stdout, _, err = executeCommand(context.Background(), "validate", "--file", "../../testdata/sample.log", "--output", "text", "--strict")
	require.Error(t, err)
	require.Contains(t, stdout, "UNDECODABLE")
	require.Contains(t, stdout, "--unknown --filter")
}

func TestPathSuggestionsUseActualFilesystem(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "weekend-sail.log")
	require.NoError(t, os.WriteFile(file, []byte("capture"), 0o600))
	require.Contains(t, pathSuggestions(filepath.Join(dir, "week")), file)
}

func TestInteractiveExecutionReportsSaveAndEmptyFilter(t *testing.T) {
	var out, status bytes.Buffer
	app := newCLI(strings.NewReader(""), &out, &status)
	config := defaultWizardConfig("record")
	config.file = "../../testdata/sample.log"
	config.outputPath = filepath.Join(t.TempDir(), "saved.log")
	require.NoError(t, app.executeInteractive(context.Background(), config))
	require.Empty(t, out.String())
	require.Contains(t, status.String(), "Saved capture: "+config.outputPath)
	require.Contains(t, status.String(), "observations saved")
	status.Reset()
	config = defaultWizardConfig("sniff")
	config.file = "../../testdata/sample.log"
	config.filter = "pgn == 999999"
	require.NoError(t, app.executeInteractive(context.Background(), config))
	require.Contains(t, status.String(), "No matching traffic")
}

func TestStoppedRecordingFlushesAndRemainsReplayable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	path := filepath.Join(t.TempDir(), "partial.log")
	source := n2k.File("../../testdata/sample.log", n2k.OriginalTiming())
	require.NoError(t, runRecord(ctx, io.Discard, source, "../../testdata/sample.log", path, "candump", false))
	stdout, _, err := executeCommand(context.Background(), "replay", path, "--timing=false", "--unknown")
	require.NoError(t, err)
	require.NotEmpty(t, stdout)
}
