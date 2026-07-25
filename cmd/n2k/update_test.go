package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitHubReleaseProviderInstallsVerifiedArchive(t *testing.T) {
	const archiveName = "n2k_1.2.3_darwin_arm64.tar.gz"
	newBinary := []byte("replacement n2k binary")
	archive := tarGzipTestBinary(t, "n2k", newBinary)
	checksum := fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), archiveName)

	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/latest":
			require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{
				"tag_name": "v1.2.3",
				"html_url": server.URL + "/release/v1.2.3",
				"assets": []map[string]string{
					{"name": archiveName, "browser_download_url": server.URL + "/assets/" + archiveName},
					{"name": "checksums.txt", "browser_download_url": server.URL + "/assets/checksums.txt"},
				},
			}))
		case "/assets/" + archiveName:
			_, _ = writer.Write(archive)
		case "/assets/checksums.txt":
			_, _ = writer.Write([]byte(checksum))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	provider := &githubReleaseProvider{
		client:     server.Client(),
		latestURL:  server.URL + "/latest",
		assetHost:  serverURL.Hostname(),
		goos:       "darwin",
		goarch:     "arm64",
		executable: "n2k",
	}
	candidate, found, err := provider.Latest(context.Background())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "1.2.3", candidate.version)
	require.Equal(t, archiveName, candidate.archive.name)

	target := filepath.Join(t.TempDir(), "n2k")
	require.NoError(t, os.WriteFile(target, []byte("old binary"), 0o755))
	require.NoError(t, provider.InstallBinary(context.Background(), candidate, target))
	installed, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, newBinary, installed)
	_, err = os.Stat(target + ".old")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestReleaseChecksumRejectsTampering(t *testing.T) {
	archive := []byte("expected archive")
	checksum := fmt.Sprintf("%x  n2k_1.0.0_linux_amd64.tar.gz\n", sha256.Sum256(archive))
	require.NoError(t, verifyReleaseChecksum(
		"n2k_1.0.0_linux_amd64.tar.gz",
		archive,
		[]byte(checksum),
	))
	require.ErrorContains(t, verifyReleaseChecksum(
		"n2k_1.0.0_linux_amd64.tar.gz",
		[]byte("tampered"),
		[]byte(checksum),
	), "SHA-256 verification failed")
}

func TestReleaseZIPExtraction(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("n2k.exe")
	require.NoError(t, err)
	_, err = file.Write([]byte("windows binary"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	binary, err := extractReleaseBinary("n2k_1.0.0_windows_amd64.zip", archive.Bytes(), "n2k.exe")
	require.NoError(t, err)
	require.Equal(t, []byte("windows binary"), binary)
}

func TestReleaseProviderTreatsMissingReleaseAsEmpty(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	provider := &githubReleaseProvider{
		client:    server.Client(),
		latestURL: server.URL,
		goos:      "linux",
		goarch:    "amd64",
	}
	_, found, err := provider.Latest(context.Background())
	require.NoError(t, err)
	require.False(t, found)
}

func tarGzipTestBinary(t *testing.T, name string, binary []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	writer := tar.NewWriter(compressed)
	require.NoError(t, writer.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(binary)),
	}))
	_, err := writer.Write(binary)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, compressed.Close())
	return archive.Bytes()
}
