# n2k-cli

[![CI](https://github.com/open-ships/n2k-cli/actions/workflows/test.yaml/badge.svg)](https://github.com/open-ships/n2k-cli/actions/workflows/test.yaml)
[![Release](https://img.shields.io/github/v/release/open-ships/n2k-cli)](https://github.com/open-ships/n2k-cli/releases)

Rich terminal tools for inspecting NMEA 2000 networks.

`n2k` decodes, records, replays, validates, and inspects NMEA 2000 traffic from
SocketCAN interfaces, USB-CAN adapters, network gateways, and capture files.
Run it without arguments for a searchable Bubble Tea command center, or use its
deterministic subcommands in scripts and JSON pipelines.

- Guided interactive workflows with fuzzy search and autocomplete.
- Typed PGN decoding with physical values, units, and exact wire data.
- Live SocketCAN, USB-CAN, TCP, and UDP sources.
- Replayable candump and owned-observation recording.
- CEL filtering, schema exploration, device inventory, and validation.
- Install-aware updates through Homebrew, Go, or verified release binaries.

The CLI is powered by the
[`open-ships/n2k`](https://github.com/open-ships/n2k) Go library.

![n2k sniff decoding NMEA 2000 PGNs as typed JSON lines with exact wire values](.github/demo.svg)

## Install

Choose either installation path. Both provide the same `n2k` command, and the
built-in updater remembers how the binary was installed.

### Homebrew

Homebrew is the simplest option on macOS and Linux:

```bash
brew install --cask open-ships/tap/n2k
n2k version
```

Homebrew upgrades are also available directly:

```bash
brew upgrade --cask open-ships/tap/n2k
```

### Go

With Go 1.25.8 or newer installed:

```bash
go install github.com/open-ships/n2k-cli/cmd/n2k@latest
n2k version
```

Go writes the binary to `GOBIN`, which defaults to `$(go env GOPATH)/bin`;
make sure that directory is on `PATH`:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
```

If you set a custom `GOBIN`, `n2k update` preserves it and replaces that same
installation.

### Release binaries

Checksum-protected archives for macOS, Linux, and Windows are published on the
[`n2k-cli` releases page](https://github.com/open-ships/n2k-cli/releases).

### Build from source

For contributors or local development:

```bash
git clone https://github.com/open-ships/n2k-cli
cd n2k-cli
just build       # writes bin/n2k
./bin/n2k --help
just install     # optional: installs n2k into Go's bin directory
```

## Quick Start — No Boat Required

The repository includes a privacy-scrubbed, six-second sailing-vessel capture,
so you can try the decoder without CAN hardware:

```bash
git clone --depth 1 https://github.com/open-ships/n2k-cli
cd n2k-cli
n2k sniff --file testdata/sample.log --output text
```

Frames containing vessel position, routes, or identity were removed.

## The `n2k` CLI

### Interactive command center

Launch the TUI:

```bash
n2k
```

The command center provides:

- Fuzzy command search with `/`, keyboard navigation, and `1`–`7` shortcuts.
- Guided source selection for SocketCAN, USB-CAN, TCP/UDP gateways, and files.
- Inline validation for paths, durations, addresses, formats, and PGNs.
- Tab-completable capture paths, common interfaces, gateway addresses, CEL
  filters, durations, and every known PGN number.
- A reviewable, copyable command preview before anything runs.
- Terminal-aware colors, contextual key help, cancellation, and a full-screen
  alternate buffer that leaves the shell clean.

For screen readers or terminals that cannot redraw reliably, use accessible
prompts:

```bash
n2k tui --accessible
# Or persist the preference:
export N2K_ACCESSIBLE=1
```

### Scriptable commands

The same workflows retain deterministic stdout and exit behavior:

```bash
# Yacht Devices WiFi gateway (RAW server mode) -- decoded JSON in one command
n2k sniff --tcp 192.168.4.1:1457

# Concrete Go PGN types with scaled physical values and SI units
n2k sniff --file capture.log --output text

# SocketCAN (Linux), USB-CAN serial, UDP, or capture replay
n2k sniff -i can0
n2k sniff -u /dev/ttyUSB0
n2k sniff --udp :1457
n2k sniff --file capture.log            # add --timing to replay at real speed
n2k sniff --file capture.log.gz         # gzip captures are expanded automatically

# CEL filtering, unknown PGNs, jq-friendly output
n2k sniff -i can0 -f 'pgn == 127250' --unknown | jq .

# Record, replay, validate, discover, and inspect schema support
n2k record -i can0 --out capture.log
n2k record --tcp 192.168.4.1:1457 --out observations.jsonl --output-format jsonl
n2k replay --timing=false capture.log
n2k validate --file capture.log --strict
n2k devices --tcp 192.168.4.1:1457 --wait 5s
n2k devices --file capture.log.gz
n2k devices list --file capture.log.gz  # "list" is an optional, readable alias
n2k pgn 127250
n2k pgn list | jq 'select(.complete == true)'
```

| Command | Functionality |
|---------|---------------|
| `n2k` / `n2k tui` | Open the searchable command palette and guided workflow forms. |
| `n2k sniff` | Decode live or recorded traffic into typed JSON lines or concrete PGN text with physical values. |
| `n2k record` | Capture replayable candump or owned observation JSON lines. |
| `n2k replay` | Replay and decode a candump capture, optionally preserving its original timing. |
| `n2k validate` | Count typed and undecodable messages by PGN and optionally fail in strict mode. |
| `n2k devices` | Actively discover a writable bus or passively inventory devices observed in a capture/UDP stream. |
| `n2k pgn` | Query or list typed PGN metadata, fields, ranges, and confidence. |
| `n2k update` | Check for a release and update through Homebrew, Go, or a verified release binary. |

Run `n2k help <command>` for organized flags, defaults, allowed values, and
examples. The purpose-built parser enforces canonical `--long-flags`, supports
`--flag=value`, validates choices before starting I/O, and suggests the nearest
command or flag after a typo.

`devices` accepts the same source vocabulary as the other inspection commands.
SocketCAN, USB, and TCP sources perform active discovery; files are scanned to
completion; UDP is observed for `--wait`. Both `n2k devices --file …` and the
more conversational `n2k devices list --file …` are valid. Capture files may
be plain candump text or gzip-compressed.

### Updates

Check for and install the latest release:

```bash
n2k update
```

The updater preserves the installation method:

- Homebrew installations run `brew upgrade --cask open-ships/tap/n2k`.
- Go installations run `go install` against the exact latest release tag.
- Downloaded release binaries are replaced atomically after their archive
  matches the release's `checksums.txt`.

Useful controls:

```bash
n2k update --check             # report only; do not install
n2k update --force             # reinstall the latest release
n2k update --method homebrew   # override: homebrew, go, or binary
```

The interactive command center checks for updates at most once every 24 hours
and offers to install them before opening. Set `N2K_AUTO_UPDATE=1` to install
without the confirmation prompt, or `N2K_NO_UPDATE_CHECK=1` to disable the
automatic check. Scriptable commands never perform implicit network requests or
updates.

### Shell completion

Enable completion for the current shell with one of:

```bash
source <(n2k completion bash)                              # Bash
source <(n2k completion zsh)                               # Zsh
n2k completion fish | source                              # Fish
n2k completion powershell | Out-String | Invoke-Expression # PowerShell
```

Completion is dynamic rather than a static command list. It understands:

- Commands and unused flags, with descriptions.
- Source and output paths, including directory traversal.
- Stream, message, and capture formats.
- SocketCAN interface names, common gateway addresses, and Go durations.
- CEL filter examples.
- Every known PGN number with its description.

### Output

`sniff` and `replay` default to JSON lines containing typed structs and their
exact wire values. The demo projects the metadata envelope down to its PGN for
readability. Set `--output text` for the concrete `pgn.<Type>`, source address,
scaled physical values, SI units, and lookup type names.

`record` writes replayable candump by default. JSON-lines mode retains each
owned source observation, including adapter and network identity, source and
receipt timestamps, gateway-relative time, direction, and frame bytes. The Go
library's observation stream additionally exposes assembled messages and
decode errors.

## Development

Go 1.25.8 or newer and `just` are required.

```bash
just setup # first checkout only
just test
just lint
just secure
```

Releases follow semantic versioning. [`VERSION`](VERSION) declares the release
baseline, and fully green release automation publishes the tag and prebuilt
binaries.

## License

MIT — see [LICENSE](LICENSE).
