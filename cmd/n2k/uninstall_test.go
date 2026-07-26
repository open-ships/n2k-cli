package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMachineUninstallerRemovesDirectInstallAndCache(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, executableName(runtime.GOOS))
	cacheDirectory := filepath.Join(root, "cache", "n2k")
	require.NoError(t, os.MkdirAll(cacheDirectory, 0o700))
	require.NoError(t, os.WriteFile(executable, []byte("n2k"), 0o755))
	require.NoError(t, os.WriteFile(executable+".old", []byte("old n2k"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(cacheDirectory, "update-check.json"),
		[]byte("{}"),
		0o600,
	))

	service := &machineUninstaller{
		executablePath: func() (string, error) { return executable, nil },
		runCommand: func(
			context.Context,
			string,
			[]string,
			[]string,
			io.Reader,
			io.Writer,
			io.Writer,
		) error {
			return errors.New("Homebrew must not run for a direct install")
		},
		removeExecutable: func(path string) (bool, error) {
			return false, os.Remove(path)
		},
		cacheDirectory: func() (string, error) { return cacheDirectory, nil },
		removeAll:      os.RemoveAll,
		removeFile:     os.Remove,
	}

	status, err := service.Uninstall(
		context.Background(),
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	)
	require.NoError(t, err)
	require.Equal(t, executable, status.Path)
	require.False(t, status.CleanupAfterExit)
	require.NoFileExists(t, executable)
	require.NoFileExists(t, executable+".old")
	require.NoDirExists(t, cacheDirectory)
}

func TestMachineUninstallerDelegatesHomebrewInstall(t *testing.T) {
	cacheDirectory := filepath.Join(t.TempDir(), "cache", "n2k")
	require.NoError(t, os.MkdirAll(cacheDirectory, 0o700))

	var commandName string
	var commandArgs []string
	service := &machineUninstaller{
		executablePath: func() (string, error) {
			return "/opt/homebrew/Caskroom/n2k/1.2.3/n2k", nil
		},
		runCommand: func(
			_ context.Context,
			name string,
			args []string,
			environment []string,
			_ io.Reader,
			_, _ io.Writer,
		) error {
			commandName = name
			commandArgs = args
			require.Empty(t, environment)
			return nil
		},
		removeExecutable: func(string) (bool, error) {
			return false, errors.New("Homebrew must own executable removal")
		},
		cacheDirectory: func() (string, error) { return cacheDirectory, nil },
		removeAll:      os.RemoveAll,
		removeFile:     os.Remove,
	}

	status, err := service.Uninstall(context.Background(), nil, io.Discard, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "/opt/homebrew/Caskroom/n2k/1.2.3/n2k", status.Path)
	require.Equal(t, "brew", commandName)
	require.Equal(t, []string{"uninstall", "--cask", homebrewCask}, commandArgs)
	require.NoDirExists(t, cacheDirectory)
}

func TestUninstallCommandReportsDeferredCleanupAndWarnings(t *testing.T) {
	var stdout strings.Builder
	var stderr strings.Builder
	app := newCLI(strings.NewReader(""), &stdout, &stderr)
	app.uninstaller = &fakeUninstaller{
		status: uninstallStatus{
			Path:             `C:\Tools\n2k.exe`,
			CleanupAfterExit: true,
			Warnings:         []error{errors.New("cache is locked")},
		},
	}

	require.NoError(t, app.ExecuteContext(context.Background(), []string{"uninstall"}))
	require.Contains(t, stdout.String(), `Uninstalled n2k from C:\Tools\n2k.exe`)
	require.Contains(t, stdout.String(), "cleanup will finish after this process exits")
	require.Contains(t, stderr.String(), "warning: cache is locked")
}

func TestUninstallHelpDoesNotRunUninstaller(t *testing.T) {
	stdout, _, err := executeCommand(context.Background(), "uninstall", "--help")
	require.NoError(t, err)
	require.Contains(t, stdout, "Usage:\n  n2k uninstall")
	require.Contains(t, stdout, "Remove n2k from this machine")
}

type fakeUninstaller struct {
	status uninstallStatus
	err    error
}

func (service *fakeUninstaller) Uninstall(
	context.Context,
	io.Reader,
	io.Writer,
	io.Writer,
) (uninstallStatus, error) {
	return service.status, service.err
}
