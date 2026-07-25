package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type uninstallStatus struct {
	Path             string
	CleanupAfterExit bool
	Warnings         []error
}

type uninstallerService interface {
	Uninstall(context.Context, io.Reader, io.Writer, io.Writer) (uninstallStatus, error)
}

type machineUninstaller struct {
	executablePath   func() (string, error)
	runCommand       updateCommandRunner
	removeExecutable func(string) (bool, error)
	cacheDirectory   func() (string, error)
	removeAll        func(string) error
	removeFile       func(string) error
}

func newUninstallerService() uninstallerService {
	return &machineUninstaller{
		executablePath:   resolvedExecutablePath,
		runCommand:       runUpdateCommand,
		removeExecutable: removeRunningExecutable,
		cacheDirectory:   n2kCacheDirectory,
		removeAll:        os.RemoveAll,
		removeFile:       os.Remove,
	}
}

func (service *machineUninstaller) Uninstall(
	ctx context.Context,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (uninstallStatus, error) {
	path, err := service.executablePath()
	if err != nil {
		return uninstallStatus{}, fmt.Errorf("locating n2k executable: %w", err)
	}
	status := uninstallStatus{Path: path}

	if isHomebrewPath(path) {
		if err := service.runCommand(
			ctx,
			"brew",
			[]string{"uninstall", "--cask", homebrewCask},
			nil,
			stdin,
			stdout,
			stderr,
		); err != nil {
			return uninstallStatus{}, fmt.Errorf("uninstalling with Homebrew: %w", err)
		}
	} else {
		cleanupAfterExit, err := service.removeExecutable(path)
		if err != nil {
			return uninstallStatus{}, fmt.Errorf("removing %s: %w", path, err)
		}
		status.CleanupAfterExit = cleanupAfterExit
		if err := service.removeFile(path + ".old"); err != nil && !os.IsNotExist(err) {
			status.Warnings = append(status.Warnings, fmt.Errorf("removing old executable backup: %w", err))
		}
	}

	cacheDirectory, err := service.cacheDirectory()
	if err != nil {
		status.Warnings = append(status.Warnings, fmt.Errorf("locating n2k cache: %w", err))
	} else if err := service.removeAll(cacheDirectory); err != nil {
		status.Warnings = append(status.Warnings, fmt.Errorf("removing n2k cache: %w", err))
	}
	return status, nil
}

func n2kCacheDirectory() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "n2k"), nil
}

func (app *cli) runUninstall(ctx context.Context) error {
	status, err := app.uninstaller.Uninstall(ctx, app.in, app.out, app.errOut)
	if err != nil {
		return err
	}
	for _, warning := range status.Warnings {
		_, _ = fmt.Fprintf(app.errOut, "n2k uninstall: warning: %v\n", warning)
	}
	if status.CleanupAfterExit {
		_, err = fmt.Fprintf(
			app.out,
			"Uninstalled n2k from %s. Final cleanup will finish after this process exits.\n",
			status.Path,
		)
		return err
	}
	_, err = fmt.Fprintf(app.out, "Uninstalled n2k from %s.\n", status.Path)
	return err
}
