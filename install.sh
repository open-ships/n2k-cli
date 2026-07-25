#!/bin/sh

set -eu

repository="open-ships/n2k-cli"
program="n2k"

say() {
	printf '%s\n' "$*"
}

fail() {
	printf 'n2k installer: %s\n' "$*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

curl_https() {
	curl --proto '=https' --tlsv1.2 --retry 3 --retry-delay 1 "$@"
}

detect_platform() {
	case "$(uname -s)" in
	Darwin)
		os="darwin"
		archive_format="tar.gz"
		binary_name="$program"
		;;
	Linux)
		os="linux"
		archive_format="tar.gz"
		binary_name="$program"
		;;
	MINGW* | MSYS* | CYGWIN*)
		os="windows"
		archive_format="zip"
		binary_name="${program}.exe"
		;;
	*)
		fail "unsupported operating system: $(uname -s)"
		;;
	esac

	case "$(uname -m)" in
	x86_64 | amd64)
		arch="amd64"
		;;
	arm64 | aarch64)
		arch="arm64"
		;;
	*)
		fail "unsupported architecture: $(uname -m)"
		;;
	esac
}

latest_version() {
	latest_url="$(
		curl_https -fsSL \
			-o /dev/null \
			-w '%{url_effective}' \
			"https://github.com/${repository}/releases/latest"
	)"
	version="${latest_url##*/}"
	version="${version#v}"
	[ -n "$version" ] || fail "could not determine the latest release"
}

sha256_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		fail "sha256sum, shasum, or openssl is required to verify the download"
	fi
}

verify_archive() {
	expected="$(
		awk -v archive="$archive_name" \
			'$2 == archive || $2 == ("*" archive) { print $1; exit }' \
			"$temporary_dir/checksums.txt"
	)"
	[ -n "$expected" ] || fail "checksums.txt has no entry for $archive_name"

	actual="$(sha256_file "$temporary_dir/$archive_name")"
	[ "$actual" = "$expected" ] || fail "SHA-256 verification failed for $archive_name"
}

install_binary() {
	case "$archive_format" in
	tar.gz)
		need tar
		tar -xOzf "$temporary_dir/$archive_name" "$binary_name" >"$extracted"
		;;
	zip)
		need unzip
		unzip -p "$temporary_dir/$archive_name" "$binary_name" >"$extracted"
		;;
	esac

	[ -s "$extracted" ] || fail "release archive does not contain $binary_name"

	mkdir -p "$install_dir"
	[ -w "$install_dir" ] || fail "$install_dir is not writable; set N2K_INSTALL_DIR to a writable directory"

	staged="$install_dir/.${binary_name}.new.$$"
	cp "$extracted" "$staged"
	chmod 755 "$staged"
	mv -f "$staged" "$install_dir/$binary_name"
}

need curl
need awk
need uname
detect_platform

if [ -n "${N2K_VERSION:-}" ]; then
	version="${N2K_VERSION#v}"
else
	latest_version
fi

install_dir="${N2K_INSTALL_DIR:-${HOME:?HOME is required}/.local/bin}"
archive_name="${program}_${version}_${os}_${arch}.${archive_format}"
download_root="https://github.com/${repository}/releases/download/v${version}"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/n2k-install.XXXXXX")"
extracted="$temporary_dir/$binary_name"
trap 'rm -rf "$temporary_dir"' 0 HUP INT TERM

say "Installing n2k v${version} for ${os}/${arch}..."
curl_https -fsSL \
	-o "$temporary_dir/$archive_name" \
	"$download_root/$archive_name"
curl_https -fsSL \
	-o "$temporary_dir/checksums.txt" \
	"$download_root/checksums.txt"

verify_archive
install_binary

say "Installed $install_dir/$binary_name"
case ":${PATH:-}:" in
*":$install_dir:"*) ;;
*)
	say ""
	say "Add n2k to your PATH:"
	say "  export PATH=\"$install_dir:\$PATH\""
	;;
esac
say ""
say "Run: n2k --help"
