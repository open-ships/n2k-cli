package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

func TestSniffContextCancellationIsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := executeCommand(ctx, "sniff", "--file", "../../testdata/sample.log", "--timing")
	require.NoError(t, err)
}

func TestRecordContextWritesReplayableCandump(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture.log")
	_, _, err := executeCommand(
		context.Background(),
		"record", "--file", "../../testdata/sample.log", "--out", output,
	)
	require.NoError(t, err)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	text := string(data)
	require.Contains(t, text, "#")
	require.True(t, strings.HasPrefix(text, "("), text)
}

func TestRootHelpIsDiscoverableWithoutATerminal(t *testing.T) {
	stdout, _, err := executeCommand(context.Background())
	require.NoError(t, err)
	require.Contains(t, stdout, "NMEA 2000 capture, diagnostics, and schema tools")
	require.Contains(t, stdout, "interactive command center")
	require.Contains(t, stdout, "completion")
	require.Contains(t, stdout, "devices")
	require.Contains(t, stdout, "validate")
}

func TestCommandHelpUsesCanonicalLongFlags(t *testing.T) {
	stdout, _, err := executeCommand(context.Background(), "sniff", "--help")
	require.NoError(t, err)
	require.Contains(t, stdout, "--tcp <address>")
	require.Contains(t, stdout, "--file <path>")
	require.Contains(t, stdout, "-i, --interface <name>")
	require.Contains(t, stdout, "--output <format>")
	require.NotContains(t, stdout, " -tcp")
	require.NotContains(t, stdout, " -file")
}

func TestDevicesHelpAndSourcesDescribeTheSameCommand(t *testing.T) {
	stdout, _, err := executeCommand(context.Background(), "help", "devices")
	require.NoError(t, err)
	require.Contains(t, stdout, "live network or capture")
	require.Contains(t, stdout, "devices [list]")
	require.Contains(t, stdout, "--file <path>")
	require.Contains(t, stdout, "--udp <address>")
	require.Contains(t, stdout, "capture.log.gz")

	_, _, err = executeCommand(context.Background(), "devices")
	require.EqualError(t, err, "exactly one source is required: --interface, --usb, --file, --tcp, or --udp")
}

func TestDevicesInventoriesPlainAndGzipCaptures(t *testing.T) {
	gzipPath := gzipTestCapture(t, "../../testdata/sample.log")
	tests := map[string][]string{
		"direct": {"devices", "--file", "../../testdata/sample.log"},
		"alias":  {"devices", "list", "--file", gzipPath},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			stdout, _, err := executeCommand(context.Background(), args...)
			require.NoError(t, err)

			var devices []deviceRecord
			require.NoError(t, json.Unmarshal([]byte(stdout), &devices))
			require.NotEmpty(t, devices)
			require.True(t, sortDevicesByAddress(devices), "device output is not sorted: %s", stdout)

			var claimed *deviceRecord
			for index := range devices {
				if devices[index].Address == 15 {
					claimed = &devices[index]
					break
				}
			}
			require.NotNil(t, claimed)
			require.NotNil(t, claimed.RawName)
			require.NotNil(t, claimed.Name)
			require.Equal(t, uint32(1450328), claimed.Name.IdentityNumber)
		})
	}
}

func TestGzipCaptureWorksWithReplay(t *testing.T) {
	gzipPath := gzipTestCapture(t, "../../testdata/sample.log")
	stdout, _, err := executeCommand(
		context.Background(),
		"replay", gzipPath, "--timing=false", "--filter", "pgn == 127257",
	)
	require.NoError(t, err)
	require.Contains(t, stdout, `"pgn":127257`)
}

func TestDevicesRejectsUnknownAction(t *testing.T) {
	_, _, err := executeCommand(
		context.Background(),
		"devices", "scan", "--file", "../../testdata/sample.log",
	)
	require.EqualError(t, err, `unknown devices action "scan": use "list"`)
}

func TestTextOutputUsesConcreteTypesAndPhysicalValues(t *testing.T) {
	stdout, _, err := executeCommand(
		context.Background(),
		"sniff", "--file", "../../testdata/sample.log", "--output", "text",
	)
	require.NoError(t, err)
	require.Contains(t, stdout, "pgn.Attitude {pgn=127257 src=11 yaw=1.9624 rad pitch=0.0415 rad roll=-0.0305 rad}")
	require.Contains(t, stdout, "pgn.WaterDepth {pgn=128267 src=3 sid=189 depth=2.7 m offset=0 m}")
	require.Contains(t, stdout, "pgn.RateOfTurn {pgn=127251 src=11 rate=0.0000215 rad/s}")
	require.NotContains(t, stdout, "000000000000")
}

func TestUnknownMessageOutputFailsClearly(t *testing.T) {
	_, _, err := executeCommand(
		context.Background(),
		"replay", "../../testdata/sample.log", "--timing=false", "--output", "yaml",
	)
	require.EqualError(t, err, `unknown --output value "yaml": use json or text`)
}

func TestCompletionCommandGeneratesShellScripts(t *testing.T) {
	tests := map[string]string{
		"bash":       "_n2k_complete",
		"fish":       "__n2k_complete",
		"powershell": "Register-ArgumentCompleter",
		"zsh":        "#compdef n2k",
	}
	for shell, marker := range tests {
		t.Run(shell, func(t *testing.T) {
			stdout, _, err := executeCommand(context.Background(), "completion", shell)
			require.NoError(t, err)
			require.Contains(t, stdout, marker)
			require.Contains(t, stdout, "n2k __complete")
		})
	}
}

func TestDynamicCompletionUnderstandsCommandsFlagsValuesAndPGNs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "command", args: []string{"__complete", "sn"}, want: "sniff\tDecode traffic"},
		{name: "flag", args: []string{"__complete", "sniff", "--ou"}, want: "--output=\tmessage output"},
		{name: "value", args: []string{"__complete", "sniff", "--output", "t"}, want: "text\tphysical values"},
		{name: "pgn", args: []string{"__complete", "pgn", "12725"}, want: "127250\tVessel Heading"},
		{name: "devices action", args: []string{"__complete", "devices", "li"}, want: "list\tInventory observed devices"},
		{name: "update method", args: []string{"__complete", "update", "--method", "h"}, want: "homebrew\tupgrade the Homebrew cask"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout, _, err := executeCommand(context.Background(), test.args...)
			require.NoError(t, err)
			require.Contains(t, stdout, test.want)
		})
	}
}

func TestUnknownCommandAndFlagSuggestClosestMatch(t *testing.T) {
	_, _, err := executeCommand(context.Background(), "snif")
	require.EqualError(t, err, `unknown command "snif"; did you mean "sniff"?`)

	_, _, err = executeCommand(context.Background(), "sniff", "--interfce", "can0")
	require.EqualError(t, err, `unknown flag "--interfce" for n2k sniff; did you mean "--interface"?`)
}

func TestSingleDashLongFlagsAreRejected(t *testing.T) {
	_, _, err := executeCommand(context.Background(), "sniff", "-file", "../../testdata/sample.log")
	require.ErrorContains(t, err, `unknown flag "-file"`)
	require.ErrorContains(t, err, `"--file"`)
}

func TestPGNCompletionIncludesDescriptions(t *testing.T) {
	completions := completePGNs("12725")
	require.NotEmpty(t, completions)
	var rendered strings.Builder
	require.NoError(t, writeCompletionCandidates(&rendered, completions))
	require.Contains(t, rendered.String(), "127250\tVessel Heading")
}

func TestVersionCommandUsesConfiguredWriter(t *testing.T) {
	stdout, _, err := executeCommand(context.Background(), "version")
	require.NoError(t, err)
	require.Equal(t, "n2k dev (commit none, built unknown)\n", stdout)
}

func TestUpdateChecksWithoutInstalling(t *testing.T) {
	updater := &fakeUpdaterService{status: updateStatus{
		CurrentVersion: "1.0.0",
		LatestVersion:  "1.1.0",
		ReleaseURL:     "https://github.com/open-ships/n2k-cli/releases/tag/v1.1.0",
		Found:          true,
		Available:      true,
		Method:         updateMethodGoInstall,
	}}
	stdout, _, err := executeCommandWithUpdater(context.Background(), updater, "update", "--check")
	require.NoError(t, err)
	require.Contains(t, stdout, "Update available: 1.0.0 → 1.1.0 (go)")
	require.Contains(t, stdout, updater.status.ReleaseURL)
	require.False(t, updater.installed)
	require.Equal(t, updateMethodAuto, updater.requested)
}

func TestUpdateInstallsWithResolvedMethod(t *testing.T) {
	updater := &fakeUpdaterService{status: updateStatus{
		CurrentVersion: "1.0.0",
		LatestVersion:  "1.1.0",
		Found:          true,
		Available:      true,
		Method:         updateMethodHomebrew,
	}}
	stdout, stderr, err := executeCommandWithUpdater(
		context.Background(),
		updater,
		"update", "--method", "homebrew",
	)
	require.NoError(t, err)
	require.True(t, updater.installed)
	require.Equal(t, updateMethodHomebrew, updater.requested)
	require.Contains(t, stderr, "via homebrew")
	require.Contains(t, stdout, "Updated n2k to 1.1.0")
}

func TestUpdateHandlesNoReleaseAndCurrentRelease(t *testing.T) {
	updater := &fakeUpdaterService{status: updateStatus{CurrentVersion: "dev"}}
	stdout, _, err := executeCommandWithUpdater(context.Background(), updater, "update")
	require.NoError(t, err)
	require.Contains(t, stdout, "No published n2k releases")

	updater.status = updateStatus{
		CurrentVersion: "1.1.0",
		LatestVersion:  "1.1.0",
		Found:          true,
		Method:         updateMethodSelfUpdate,
	}
	stdout, _, err = executeCommandWithUpdater(context.Background(), updater, "update")
	require.NoError(t, err)
	require.Equal(t, "n2k 1.1.0 is up to date.\n", stdout)
	require.False(t, updater.installed)
}

func TestGoInstallBuildVersionIsDiscovered(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{
			Path:    "github.com/open-ships/n2k-cli",
			Version: "v1.2.3",
		},
	}
	resolved := resolveBuildVersion("dev", info)
	require.Equal(t, "1.2.3", resolved.version)
	require.True(t, resolved.installedWithGo)

	linked := resolveBuildVersion("v2.0.0", info)
	require.Equal(t, "2.0.0", linked.version)
	require.False(t, linked.installedWithGo)
}

func TestCheckoutPseudoVersionIsNotAnInstalledRelease(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{
			Path:    "github.com/open-ships/n2k-cli",
			Version: "v0.0.0-20260725111237-e2d10346063b+dirty",
		},
	}
	resolved := resolveBuildVersion("dev", info)
	require.Equal(t, "dev", resolved.version)
	require.False(t, resolved.installedWithGo)
	require.True(t, isPseudoVersion(info.Main.Version))
	require.True(t, isPseudoVersion("v1.2.4-0.20260725111237-e2d10346063b"))
	require.False(t, isPseudoVersion("v1.2.3-beta.1"))
}

func TestHomebrewInstallPathsAreDetected(t *testing.T) {
	require.True(t, isHomebrewPath("/opt/homebrew/Caskroom/n2k/1.2.3/n2k"))
	require.True(t, isHomebrewPath("/usr/local/Cellar/n2k/1.2.3/bin/n2k"))
	require.False(t, isHomebrewPath("/Users/developer/go/bin/n2k"))
}

func TestUpdaterDetectsGoInstallAndRunsPinnedRelease(t *testing.T) {
	provider := &fakeReleaseProvider{
		candidate: releaseCandidate{
			version: "1.2.0",
			url:     "https://github.com/open-ships/n2k-cli/releases/tag/v1.2.0",
		},
		found: true,
	}
	var commandName string
	var commandArgs []string
	service := &releaseUpdaterService{
		provider:       provider,
		executablePath: func() (string, error) { return "/Users/developer/go/bin/n2k", nil },
		currentVersion: func() buildVersion {
			return buildVersion{version: "1.1.0", installedWithGo: true}
		},
		runCommand: func(
			_ context.Context,
			name string,
			args []string,
			_ io.Reader,
			_, _ io.Writer,
		) error {
			commandName = name
			commandArgs = args
			return nil
		},
	}

	status, err := service.Check(context.Background(), updateMethodAuto)
	require.NoError(t, err)
	require.True(t, status.Available)
	require.Equal(t, updateMethodGoInstall, status.Method)
	require.NoError(t, service.Install(
		context.Background(),
		status,
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	))
	require.Equal(t, "go", commandName)
	require.Equal(t, []string{
		"install",
		"github.com/open-ships/n2k-cli/cmd/n2k@v1.2.0",
	}, commandArgs)
	require.False(t, provider.binaryInstalled)
}

func TestUpdaterPrefersHomebrewAndVerifiesDirectBinary(t *testing.T) {
	provider := &fakeReleaseProvider{
		candidate: releaseCandidate{version: "1.2.0"},
		found:     true,
	}
	service := &releaseUpdaterService{
		provider:       provider,
		executablePath: func() (string, error) { return "/opt/homebrew/Caskroom/n2k/1.1.0/n2k", nil },
		currentVersion: func() buildVersion {
			return buildVersion{version: "1.1.0", installedWithGo: true}
		},
		runCommand: func(
			_ context.Context,
			name string,
			args []string,
			_ io.Reader,
			_, _ io.Writer,
		) error {
			require.Equal(t, "brew", name)
			require.Equal(t, []string{"upgrade", "--cask", "open-ships/tap/n2k"}, args)
			return nil
		},
	}

	status, err := service.Check(context.Background(), updateMethodAuto)
	require.NoError(t, err)
	require.Equal(t, updateMethodHomebrew, status.Method)
	require.NoError(t, service.Install(context.Background(), status, nil, io.Discard, io.Discard))

	status.Method = updateMethodSelfUpdate
	require.NoError(t, service.Install(context.Background(), status, nil, io.Discard, io.Discard))
	require.True(t, provider.binaryInstalled)
	require.Equal(t, "/opt/homebrew/Caskroom/n2k/1.1.0/n2k", provider.binaryPath)
}

func TestAutomaticUpdateCheckCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "n2k", "update-check.json")
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	require.True(t, updateCheckDue(path, now))
	require.NoError(t, recordUpdateCheck(path, now))
	require.False(t, updateCheckDue(path, now.Add(23*time.Hour)))
	require.True(t, updateCheckDue(path, now.Add(24*time.Hour)))
}

func TestPaletteSupportsKeyboardShortcuts(t *testing.T) {
	model := newPaletteModel()
	require.Contains(t, model.View().Content, "N2K COMMAND CENTER")
	require.Contains(t, model.View().Content, "Sniff decoded traffic")
	require.Equal(t, "Update n2k", commandPaletteItems()[6].title)

	updated, command := model.Update(tea.KeyPressMsg(tea.Key{Text: "3", Code: '3'}))
	require.NotNil(t, command)
	final := updated.(paletteModel)
	require.Equal(t, "replay", final.selected)
	require.False(t, final.canceled)

	model = newPaletteModel()
	updated, command = model.Update(tea.KeyPressMsg(tea.Key{Text: "7", Code: '7'}))
	require.NotNil(t, command)
	final = updated.(paletteModel)
	require.Equal(t, "update", final.selected)
}

func TestWizardBuildsAReviewableCanonicalCommand(t *testing.T) {
	config := defaultWizardConfig("sniff")
	config.source = "tcp"
	config.tcp = "192.168.4.1:1457"
	config.format = "raw"
	config.output = "text"
	config.filter = "pgn == 127250"
	config.unknown = true

	args := config.args()
	require.Equal(t, []string{
		"sniff",
		"--tcp", "192.168.4.1:1457",
		"--format", "raw",
		"--output", "text",
		"--filter", "pgn == 127250",
		"--unknown",
	}, args)
	require.Equal(
		t,
		"$ n2k sniff --tcp 192.168.4.1:1457 --format raw --output text --filter 'pgn == 127250' --unknown",
		renderCommandPreview(args),
	)
}

func TestDevicesWizardBuildsPassiveCaptureInventory(t *testing.T) {
	config := defaultWizardConfig("devices")
	config.file = "capture.log.gz"

	require.True(t, config.allowReadOnly)
	require.Equal(t, []string{
		"devices",
		"--file", "capture.log.gz",
	}, config.args())
}

type fakeUpdaterService struct {
	status     updateStatus
	checkErr   error
	installErr error
	requested  updateMethod
	installed  bool
}

func (service *fakeUpdaterService) Check(_ context.Context, requested updateMethod) (updateStatus, error) {
	service.requested = requested
	return service.status, service.checkErr
}

func (service *fakeUpdaterService) Install(
	_ context.Context,
	_ updateStatus,
	_ io.Reader,
	_, _ io.Writer,
) error {
	service.installed = true
	return service.installErr
}

type fakeReleaseProvider struct {
	candidate       releaseCandidate
	found           bool
	err             error
	binaryInstalled bool
	binaryPath      string
}

func (provider *fakeReleaseProvider) Latest(context.Context) (releaseCandidate, bool, error) {
	return provider.candidate, provider.found, provider.err
}

func (provider *fakeReleaseProvider) InstallBinary(
	_ context.Context,
	_ releaseCandidate,
	path string,
) error {
	provider.binaryInstalled = true
	provider.binaryPath = path
	return provider.err
}

func executeCommand(ctx context.Context, args ...string) (string, string, error) {
	return executeCommandWithUpdater(ctx, nil, args...)
}

func executeCommandWithUpdater(
	ctx context.Context,
	updater updaterService,
	args ...string,
) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newCLI(strings.NewReader(""), &stdout, &stderr)
	if updater != nil {
		app.updater = updater
	}
	err := app.ExecuteContext(ctx, args)
	return stdout.String(), stderr.String(), err
}

func gzipTestCapture(t *testing.T, source string) string {
	t.Helper()
	input, err := os.ReadFile(source)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "capture.log.gz")
	file, err := os.Create(path) // #nosec G304 -- the test controls this temporary path.
	require.NoError(t, err)
	compressed := gzip.NewWriter(file)
	_, err = compressed.Write(input)
	require.NoError(t, err)
	require.NoError(t, compressed.Close())
	require.NoError(t, file.Close())
	return path
}

func sortDevicesByAddress(devices []deviceRecord) bool {
	for index := 1; index < len(devices); index++ {
		if devices[index-1].Address > devices[index].Address {
			return false
		}
	}
	return true
}
