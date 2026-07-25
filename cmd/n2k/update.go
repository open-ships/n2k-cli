package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

const (
	updateRepositoryOwner = "open-ships"
	updateRepositoryName  = "n2k-cli"
	goInstallPackage      = "github.com/open-ships/n2k-cli/cmd/n2k"
	homebrewCask          = "open-ships/tap/n2k"

	updateCheckInterval = 24 * time.Hour
	updateCheckTimeout  = 3 * time.Second

	maxReleaseMetadataBytes = 2 << 20
	maxChecksumBytes        = 2 << 20
	maxReleaseArchiveBytes  = 200 << 20
	maxReleaseBinaryBytes   = 200 << 20
	maxExpandedArchiveBytes = 500 << 20
)

type updateMethod string

const (
	updateMethodAuto       updateMethod = "auto"
	updateMethodHomebrew   updateMethod = "homebrew"
	updateMethodGoInstall  updateMethod = "go"
	updateMethodSelfUpdate updateMethod = "binary"
)

type updateStatus struct {
	CurrentVersion string
	LatestVersion  string
	ReleaseURL     string
	Found          bool
	Available      bool
	Method         updateMethod
	force          bool
	candidate      releaseCandidate
}

type updaterService interface {
	Check(context.Context, updateMethod) (updateStatus, error)
	Install(context.Context, updateStatus, io.Reader, io.Writer, io.Writer) error
}

type releaseAsset struct {
	name string
	url  string
}

type releaseCandidate struct {
	version  string
	url      string
	archive  releaseAsset
	checksum releaseAsset
}

type releaseProvider interface {
	Latest(context.Context) (releaseCandidate, bool, error)
	InstallBinary(context.Context, releaseCandidate, string) error
}

type githubReleaseProvider struct {
	client     *http.Client
	latestURL  string
	assetHost  string
	goos       string
	goarch     string
	executable string
}

type githubReleaseResponse struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func newUpdaterService() updaterService {
	return &releaseUpdaterService{
		provider: &githubReleaseProvider{
			client:     http.DefaultClient,
			latestURL:  "https://api.github.com/repos/open-ships/n2k-cli/releases/latest",
			assetHost:  "github.com",
			goos:       runtime.GOOS,
			goarch:     runtime.GOARCH,
			executable: executableName(runtime.GOOS),
		},
		executablePath: resolvedExecutablePath,
		runCommand:     runUpdateCommand,
		currentVersion: currentBuildVersion,
	}
}

func (provider *githubReleaseProvider) Latest(ctx context.Context) (releaseCandidate, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.latestURL, http.NoBody)
	if err != nil {
		return releaseCandidate{}, false, fmt.Errorf("creating release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "n2k-cli-updater")

	response, err := provider.client.Do(request)
	if err != nil {
		return releaseCandidate{}, false, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return releaseCandidate{}, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return releaseCandidate{}, false, fmt.Errorf("GitHub release API returned %s", response.Status)
	}
	body, err := readLimited(response.Body, maxReleaseMetadataBytes, "release metadata")
	if err != nil {
		return releaseCandidate{}, false, err
	}
	var release githubReleaseResponse
	if err := json.Unmarshal(body, &release); err != nil {
		return releaseCandidate{}, false, fmt.Errorf("decoding release metadata: %w", err)
	}
	parsedVersion, err := semver.NewVersion(release.TagName)
	if err != nil {
		return releaseCandidate{}, false, fmt.Errorf("release has invalid version %q: %w", release.TagName, err)
	}

	candidate := releaseCandidate{
		version: parsedVersion.String(),
		url:     release.HTMLURL,
	}
	archiveSuffix := "_" + provider.goos + "_" + provider.goarch
	if provider.goos == "windows" {
		archiveSuffix += ".zip"
	} else {
		archiveSuffix += ".tar.gz"
	}
	for _, asset := range release.Assets {
		switch {
		case asset.Name == "checksums.txt":
			candidate.checksum = releaseAsset{name: asset.Name, url: asset.BrowserDownloadURL}
		case strings.HasPrefix(asset.Name, "n2k_") && strings.HasSuffix(asset.Name, archiveSuffix):
			candidate.archive = releaseAsset{name: asset.Name, url: asset.BrowserDownloadURL}
		}
	}
	return candidate, true, nil
}

func (provider *githubReleaseProvider) InstallBinary(
	ctx context.Context,
	candidate releaseCandidate,
	path string,
) error {
	if candidate.archive.url == "" {
		return fmt.Errorf("release %s has no archive for %s/%s", candidate.version, provider.goos, provider.goarch)
	}
	if candidate.checksum.url == "" {
		return fmt.Errorf("release %s has no checksums.txt", candidate.version)
	}
	archive, err := provider.download(ctx, candidate.archive, maxReleaseArchiveBytes)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", candidate.archive.name, err)
	}
	checksums, err := provider.download(ctx, candidate.checksum, maxChecksumBytes)
	if err != nil {
		return fmt.Errorf("downloading checksums.txt: %w", err)
	}
	if err := verifyReleaseChecksum(candidate.archive.name, archive, checksums); err != nil {
		return err
	}
	binary, err := extractReleaseBinary(candidate.archive.name, archive, provider.executable)
	if err != nil {
		return err
	}
	if err := replaceExecutable(path, binary); err != nil {
		return fmt.Errorf("replacing executable: %w", err)
	}
	return nil
}

func (provider *githubReleaseProvider) download(
	ctx context.Context,
	asset releaseAsset,
	limit int64,
) ([]byte, error) {
	parsed, err := url.Parse(asset.url)
	if err != nil {
		return nil, fmt.Errorf("parsing asset URL: %w", err)
	}
	if parsed.Scheme != "https" || (provider.assetHost != "" && !strings.EqualFold(parsed.Hostname(), provider.assetHost)) {
		return nil, fmt.Errorf("refusing untrusted release asset URL %q", asset.url)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.url, http.NoBody)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "n2k-cli-updater")
	response, err := provider.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %s", response.Status)
	}
	return readLimited(response.Body, limit, asset.name)
}

func readLimited(reader io.Reader, limit int64, label string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", label, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	return data, nil
}

func verifyReleaseChecksum(name string, archive, checksums []byte) error {
	var expected string
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		filename := strings.TrimPrefix(fields[1], "*")
		if filename == name {
			expected = fields[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksums.txt has no entry for %s", name)
	}
	expectedBytes, err := hex.DecodeString(expected)
	if err != nil || len(expectedBytes) != sha256.Size {
		return fmt.Errorf("checksums.txt has an invalid SHA-256 for %s", name)
	}
	actual := sha256.Sum256(archive)
	if subtle.ConstantTimeCompare(actual[:], expectedBytes) != 1 {
		return fmt.Errorf("SHA-256 verification failed for %s", name)
	}
	return nil
}

func extractReleaseBinary(archiveName string, archive []byte, executable string) ([]byte, error) {
	switch {
	case strings.HasSuffix(archiveName, ".tar.gz"):
		return extractTarGzipBinary(archive, executable)
	case strings.HasSuffix(archiveName, ".zip"):
		return extractZIPBinary(archive, executable)
	default:
		return nil, fmt.Errorf("unsupported release archive %q", archiveName)
	}
}

func extractTarGzipBinary(archive []byte, executable string) ([]byte, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("opening release archive: %w", err)
	}
	defer func() { _ = compressed.Close() }()
	reader := tar.NewReader(io.LimitReader(compressed, maxExpandedArchiveBytes))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading release archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != executable {
			continue
		}
		return readLimited(reader, maxReleaseBinaryBytes, executable)
	}
	return nil, fmt.Errorf("release archive does not contain %s", executable)
}

func extractZIPBinary(archive []byte, executable string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("opening release archive: %w", err)
	}
	for _, file := range reader.File {
		if filepath.Base(file.Name) != executable || file.FileInfo().IsDir() {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("opening %s in release archive: %w", executable, err)
		}
		binary, readErr := readLimited(stream, maxReleaseBinaryBytes, executable)
		closeErr := stream.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, fmt.Errorf("closing %s in release archive: %w", executable, closeErr)
		}
		return binary, nil
	}
	return nil, fmt.Errorf("release archive does not contain %s", executable)
}

func executableName(goos string) string {
	if goos == "windows" {
		return "n2k.exe"
	}
	return "n2k"
}

func resolvedExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func replaceExecutable(path string, binary []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o755
	}

	directory := filepath.Dir(path)
	replacement, err := os.CreateTemp(directory, "."+filepath.Base(path)+".new-*")
	if err != nil {
		return err
	}
	replacementPath := replacement.Name()
	defer func() { _ = os.Remove(replacementPath) }()
	if err := replacement.Chmod(mode); err != nil {
		_ = replacement.Close()
		return err
	}
	if _, err := io.Copy(replacement, bytes.NewReader(binary)); err != nil {
		_ = replacement.Close()
		return err
	}
	if err := replacement.Sync(); err != nil {
		_ = replacement.Close()
		return err
	}
	if err := replacement.Close(); err != nil {
		return err
	}

	backupPath := path + ".old"
	_ = os.Remove(backupPath)
	if err := os.Rename(path, backupPath); err != nil {
		return err
	}
	if err := os.Rename(replacementPath, path); err != nil {
		if rollbackErr := os.Rename(backupPath, path); rollbackErr != nil {
			return fmt.Errorf("%w (rollback also failed: %v)", err, rollbackErr)
		}
		return err
	}
	_ = os.Remove(backupPath) // Windows retains the running binary until the next update.
	return nil
}

type updateCommandRunner func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error

type releaseUpdaterService struct {
	provider       releaseProvider
	executablePath func() (string, error)
	runCommand     updateCommandRunner
	currentVersion func() buildVersion
}

func (service *releaseUpdaterService) Check(ctx context.Context, requested updateMethod) (updateStatus, error) {
	current := service.currentVersion()
	method, err := service.resolveMethod(requested, current.installedWithGo)
	if err != nil {
		return updateStatus{}, err
	}
	status := updateStatus{
		CurrentVersion: current.version,
		Method:         method,
	}
	candidate, found, err := service.provider.Latest(ctx)
	if err != nil {
		return status, fmt.Errorf("checking GitHub releases: %w", err)
	}
	if !found {
		return status, nil
	}
	latest, err := semver.NewVersion(candidate.version)
	if err != nil {
		return status, fmt.Errorf("release has invalid version %q: %w", candidate.version, err)
	}
	status.Found = true
	status.LatestVersion = latest.String()
	status.ReleaseURL = candidate.url
	status.candidate = candidate

	running, err := semver.NewVersion(current.version)
	if err != nil {
		status.Available = true
		return status, nil
	}
	status.Available = latest.GreaterThan(running)
	return status, nil
}

func (service *releaseUpdaterService) resolveMethod(requested updateMethod, installedWithGo bool) (updateMethod, error) {
	switch requested {
	case updateMethodHomebrew, updateMethodGoInstall, updateMethodSelfUpdate:
		return requested, nil
	case updateMethodAuto:
	default:
		return "", fmt.Errorf("unknown update method %q", requested)
	}

	path, err := service.executablePath()
	if err == nil && isHomebrewPath(path) {
		return updateMethodHomebrew, nil
	}
	if installedWithGo {
		return updateMethodGoInstall, nil
	}
	return updateMethodSelfUpdate, nil
}

func (service *releaseUpdaterService) Install(
	ctx context.Context,
	status updateStatus,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	if !status.Found {
		return errors.New("no published release is available to install")
	}
	switch status.Method {
	case updateMethodHomebrew:
		action := "upgrade"
		if status.force {
			action = "reinstall"
		}
		if err := service.runCommand(
			ctx,
			"brew",
			[]string{action, "--cask", homebrewCask},
			nil,
			stdin,
			stdout,
			stderr,
		); err != nil {
			return fmt.Errorf("updating with Homebrew: %w", err)
		}
	case updateMethodGoInstall:
		tag := "v" + status.LatestVersion
		path, err := service.executablePath()
		if err != nil {
			return fmt.Errorf("locating n2k executable: %w", err)
		}
		if err := service.runCommand(
			ctx,
			"go",
			[]string{"install", goInstallPackage + "@" + tag},
			[]string{"GOBIN=" + filepath.Dir(path)},
			stdin,
			stdout,
			stderr,
		); err != nil {
			return fmt.Errorf("updating with go install: %w", err)
		}
	case updateMethodSelfUpdate:
		path, err := service.executablePath()
		if err != nil {
			return fmt.Errorf("locating n2k executable: %w", err)
		}
		if err := service.provider.InstallBinary(ctx, status.candidate, path); err != nil {
			return fmt.Errorf("installing verified release binary: %w", err)
		}
	default:
		return fmt.Errorf("unknown resolved update method %q", status.Method)
	}
	return nil
}

func runUpdateCommand(
	ctx context.Context,
	name string,
	args []string,
	environment []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	command := exec.CommandContext(ctx, name, args...) // #nosec G204 -- executable and arguments are fixed update strategies.
	command.Env = append(os.Environ(), environment...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func isHomebrewPath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(path, "/caskroom/n2k/") || strings.Contains(path, "/cellar/n2k/")
}

type buildVersion struct {
	version         string
	installedWithGo bool
}

func currentBuildVersion() buildVersion {
	info, _ := debug.ReadBuildInfo()
	return resolveBuildVersion(version, info)
}

func resolveBuildVersion(linkedVersion string, info *debug.BuildInfo) buildVersion {
	if linkedVersion != "" && linkedVersion != "dev" {
		return buildVersion{version: strings.TrimPrefix(linkedVersion, "v")}
	}
	if info != nil &&
		info.Main.Path == "github.com/open-ships/n2k-cli" &&
		info.Main.Version != "" &&
		info.Main.Version != "(devel)" &&
		!isPseudoVersion(info.Main.Version) {
		if parsed, err := semver.NewVersion(info.Main.Version); err == nil {
			return buildVersion{version: parsed.String(), installedWithGo: true}
		}
	}
	return buildVersion{version: "dev"}
}

func isPseudoVersion(value string) bool {
	parsed, err := semver.NewVersion(value)
	if err != nil {
		return false
	}
	prerelease := strings.TrimPrefix(parsed.Prerelease(), "0.")
	parts := strings.Split(prerelease, "-")
	if len(parts) != 2 || len(parts[0]) != 14 || len(parts[1]) < 7 {
		return false
	}
	for _, character := range parts[0] {
		if character < '0' || character > '9' {
			return false
		}
	}
	for _, character := range parts[1] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func (app *cli) runUpdate(ctx context.Context, parsed parsedCommand) error {
	requested := updateMethod(parsed.stringValue("method"))
	status, err := app.updater.Check(ctx, requested)
	if err != nil {
		return err
	}
	status.force = parsed.boolValue("force")
	if !status.Found {
		_, err := fmt.Fprintln(app.out, "No published n2k releases are available yet.")
		return err
	}

	if !status.Available && !status.force {
		_, err := fmt.Fprintf(app.out, "n2k %s is up to date.\n", status.CurrentVersion)
		return err
	}

	if parsed.boolValue("check") {
		if status.Available {
			_, err := fmt.Fprintf(
				app.out,
				"Update available: %s → %s (%s)\n%s\n",
				status.CurrentVersion,
				status.LatestVersion,
				status.Method,
				status.ReleaseURL,
			)
			return err
		}
		_, err := fmt.Fprintf(app.out, "Latest release: %s (%s)\n", status.LatestVersion, status.ReleaseURL)
		return err
	}

	_, _ = fmt.Fprintf(
		app.errOut,
		"Updating n2k %s → %s via %s…\n",
		status.CurrentVersion,
		status.LatestVersion,
		status.Method,
	)
	if err := app.updater.Install(ctx, status, app.in, app.out, app.errOut); err != nil {
		return err
	}
	_, err = fmt.Fprintf(app.out, "Updated n2k to %s. Restart n2k to use the new version.\n", status.LatestVersion)
	return err
}

type updateCheckState struct {
	CheckedAt time.Time `json:"checkedAt"`
}

func updateCheckCachePath() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "n2k", "update-check.json"), nil
}

func updateCheckDue(path string, now time.Time) bool {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the fixed n2k user-cache location.
	if err != nil {
		return true
	}
	var state updateCheckState
	if json.Unmarshal(data, &state) != nil {
		return true
	}
	return state.CheckedAt.IsZero() || now.Sub(state.CheckedAt) >= updateCheckInterval
}

func recordUpdateCheck(path string, checkedAt time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(updateCheckState{CheckedAt: checkedAt})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
