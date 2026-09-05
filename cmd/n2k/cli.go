package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/open-ships/n2k"
)

type flagKind uint8

const (
	stringFlag flagKind = iota
	boolFlag
	durationFlag
)

type completionItem struct {
	value       string
	description string
}

type flagSpec struct {
	name        string
	short       string
	valueName   string
	description string
	defaultVal  string
	kind        flagKind
	choices     []completionItem
	file        bool
}

type commandSpec struct {
	name     string
	summary  string
	usage    string
	examples []string
	flags    []flagSpec
	minArgs  int
	maxArgs  int
}

type parsedCommand struct {
	spec        commandSpec
	values      map[string]string
	positionals []string
}

func (parsed parsedCommand) stringValue(name string) string {
	return parsed.values[name]
}

func (parsed parsedCommand) boolValue(name string) bool {
	value, _ := strconv.ParseBool(parsed.values[name])
	return value
}

func (parsed parsedCommand) durationValue(name string) (time.Duration, error) {
	value := parsed.values[name]
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --%s duration %q: %w", name, value, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("--%s must be greater than zero", name)
	}
	return duration, nil
}

type cli struct {
	in          io.Reader
	out         io.Writer
	errOut      io.Writer
	updater     updaterService
	uninstaller uninstallerService
}

func newCLI(in io.Reader, out, errOut io.Writer) *cli {
	return &cli{
		in:          in,
		out:         out,
		errOut:      errOut,
		updater:     newUpdaterService(),
		uninstaller: newUninstallerService(),
	}
}

func (app *cli) ExecuteContext(ctx context.Context, args []string) error {
	if len(args) == 0 {
		if app.canUseTUI() {
			return app.runInteractive(ctx, false)
		}
		return writeRootHelp(app.out)
	}

	switch args[0] {
	case "-h", "--help":
		return writeRootHelp(app.out)
	case "-v", "--version":
		return writeVersion(app.out)
	case "help":
		if len(args) == 1 {
			return writeRootHelp(app.out)
		}
		if len(args) != 2 {
			return errors.New("usage: n2k help [command]")
		}
		spec, found := findCommand(args[1])
		if !found {
			return unknownCommandError(args[1])
		}
		return writeCommandHelp(app.out, spec)
	case "__complete":
		return writeCompletionCandidates(app.out, completionCandidates(args[1:]))
	}

	spec, found := findCommand(args[0])
	if !found {
		return unknownCommandError(args[0])
	}
	for _, arg := range args[1:] {
		if arg == "-h" || arg == "--help" {
			return writeCommandHelp(app.out, spec)
		}
	}
	parsed, err := parseCommand(spec, args[1:])
	if err != nil {
		return err
	}
	return app.runParsed(ctx, parsed)
}

func (app *cli) runParsed(ctx context.Context, parsed parsedCommand) error {
	if ctx.Err() != nil {
		return nil
	}
	if parsed.spec.name != "tui" {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt)
		defer stop()
	}
	if err := validateFilter(parsed.stringValue("filter")); err != nil {
		return err
	}
	switch parsed.spec.name {
	case "tui":
		return app.runInteractive(ctx, parsed.boolValue("accessible"))
	case "sniff":
		source, _, cleanup, err := preparedSource(ctx, parsed, true)
		if err != nil {
			return err
		}
		defer cleanup()
		return runSniff(
			ctx,
			app.out,
			source,
			parsed.stringValue("filter"),
			parsed.boolValue("unknown"),
			parsed.stringValue("output"),
		)
	case "record":
		if err := checkRecordPaths(parsed.stringValue("file"), parsed.stringValue("out")); err != nil {
			return err
		}
		source, _, cleanup, err := preparedSource(ctx, parsed, true)
		if err != nil {
			return err
		}
		defer cleanup()
		return runRecord(
			ctx,
			app.out,
			source,
			parsed.stringValue("file"),
			parsed.stringValue("out"),
			parsed.stringValue("output-format"),
			parsed.boolValue("overwrite"),
		)
	case "replay":
		file := parsed.stringValue("file")
		if file != "" && len(parsed.positionals) != 0 {
			return errors.New("pass a capture either positionally or with --file, not both")
		}
		if file == "" && len(parsed.positionals) == 1 {
			file = parsed.positionals[0]
		}
		if file == "" {
			return errors.New("a candump capture path is required")
		}
		file, cleanup, err := prepareCapture(ctx, file)
		if err != nil {
			return err
		}
		defer cleanup()
		return runReplay(
			ctx,
			app.out,
			file,
			parsed.boolValue("timing"),
			parsed.stringValue("filter"),
			parsed.boolValue("unknown"),
			parsed.stringValue("output"),
		)
	case "validate":
		source, _, cleanup, err := preparedSource(ctx, parsed, true)
		if err != nil {
			return err
		}
		defer cleanup()
		return runValidate(ctx, app.out, source, parsed.boolValue("strict"), parsed.stringValue("output"))
	case "devices":
		if len(parsed.positionals) == 1 && parsed.positionals[0] != "list" {
			return fmt.Errorf("unknown devices action %q: use \"list\"", parsed.positionals[0])
		}
		source, values, cleanup, err := preparedSource(ctx, parsed, true)
		if err != nil {
			return err
		}
		defer cleanup()
		wait, err := parsed.durationValue("wait")
		if err != nil {
			return err
		}
		claimTimeout, err := parsed.durationValue("claim-timeout")
		if err != nil {
			return err
		}
		if values.file != "" || values.udp != "" {
			return runPassiveDevices(ctx, app.out, source, wait, values.udp != "", parsed.stringValue("output"))
		}
		return runActiveDevices(ctx, app.out, source, values.tcp != "", wait, claimTimeout, parsed.stringValue("output"))
	case "pgn":
		return runPGN(app.out, parsed.positionals[0], parsed.stringValue("output"))
	case "completion":
		return writeShellCompletion(app.out, parsed.positionals[0])
	case "version":
		return writeVersion(app.out)
	case "update":
		return app.runUpdate(ctx, parsed)
	case "uninstall":
		return app.runUninstall(ctx)
	default:
		return fmt.Errorf("command %q is not executable", parsed.spec.name)
	}
}

func preparedSource(ctx context.Context, parsed parsedCommand, allowReadOnly bool) (n2k.Option, sourceFlagValues, func(), error) {
	values := sourceValues(parsed, allowReadOnly)
	if _, err := values.option(); err != nil {
		return nil, values, func() {}, err
	}
	cleanup, err := values.prepareCapture(ctx)
	if err != nil {
		return nil, values, func() {}, err
	}
	source, err := values.option()
	if err != nil {
		cleanup()
		return nil, values, func() {}, err
	}
	return source, values, cleanup, nil
}

func sourceValues(parsed parsedCommand, allowReadOnly bool) sourceFlagValues {
	values := sourceFlagValues{
		iface:  parsed.stringValue("interface"),
		usb:    parsed.stringValue("usb"),
		tcp:    parsed.stringValue("tcp"),
		format: parsed.stringValue("format"),
	}
	if allowReadOnly {
		values.file = parsed.stringValue("file")
		values.udp = parsed.stringValue("udp")
		values.timing = parsed.boolValue("timing")
	}
	return values
}

func (app *cli) canUseTUI() bool {
	input, inputOK := app.in.(*os.File)
	output, outputOK := app.errOut.(*os.File)
	return inputOK && outputOK && term.IsTerminal(input.Fd()) && term.IsTerminal(output.Fd())
}

func writeVersion(out io.Writer) error {
	_, err := fmt.Fprintf(out, "n2k %s (commit %s, built %s)\n", currentBuildVersion().version, commit, date)
	return err
}

func parseCommand(spec commandSpec, args []string) (parsedCommand, error) {
	parsed := parsedCommand{
		spec:   spec,
		values: make(map[string]string, len(spec.flags)),
	}
	longFlags := make(map[string]flagSpec, len(spec.flags))
	shortFlags := make(map[string]flagSpec, len(spec.flags))
	for _, flag := range spec.flags {
		parsed.values[flag.name] = flag.defaultVal
		longFlags[flag.name] = flag
		if flag.short != "" {
			shortFlags[flag.short] = flag
		}
	}

	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			parsed.positionals = append(parsed.positionals, args[index+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			parsed.positionals = append(parsed.positionals, arg)
			continue
		}

		var (
			flag     flagSpec
			found    bool
			inline   string
			hasValue bool
		)
		switch {
		case strings.HasPrefix(arg, "--"):
			name := strings.TrimPrefix(arg, "--")
			if before, after, ok := strings.Cut(name, "="); ok {
				name, inline, hasValue = before, after, true
			}
			flag, found = longFlags[name]
			if !found {
				return parsedCommand{}, unknownFlagError(spec, "--"+name)
			}
		default:
			name := strings.TrimPrefix(arg, "-")
			if before, after, ok := strings.Cut(name, "="); ok {
				name, inline, hasValue = before, after, true
			}
			flag, found = shortFlags[name]
			if !found {
				return parsedCommand{}, unknownFlagError(spec, "-"+name)
			}
		}

		if flag.kind == boolFlag {
			value := "true"
			if hasValue {
				value = inline
			} else if index+1 < len(args) && (args[index+1] == "true" || args[index+1] == "false") {
				index++
				value = args[index]
			}
			if _, err := strconv.ParseBool(value); err != nil {
				return parsedCommand{}, fmt.Errorf("--%s expects true or false, got %q", flag.name, value)
			}
			parsed.values[flag.name] = value
			continue
		}

		value := inline
		if !hasValue {
			if index+1 >= len(args) || (strings.HasPrefix(args[index+1], "-") && args[index+1] != "-") {
				return parsedCommand{}, fmt.Errorf("--%s requires <%s>", flag.name, flag.valueName)
			}
			index++
			value = args[index]
		}
		if err := validateFlagChoice(flag, value); err != nil {
			return parsedCommand{}, err
		}
		parsed.values[flag.name] = value
	}

	if len(parsed.positionals) < spec.minArgs {
		return parsedCommand{}, fmt.Errorf("missing argument: usage: n2k %s", spec.usage)
	}
	if spec.maxArgs >= 0 && len(parsed.positionals) > spec.maxArgs {
		return parsedCommand{}, fmt.Errorf("too many arguments: usage: n2k %s", spec.usage)
	}
	return parsed, nil
}

func validateFlagChoice(flag flagSpec, value string) error {
	if len(flag.choices) == 0 {
		return nil
	}
	for _, choice := range flag.choices {
		if value == choice.value {
			return nil
		}
	}
	values := make([]string, 0, len(flag.choices))
	for _, choice := range flag.choices {
		values = append(values, choice.value)
	}
	return fmt.Errorf("unknown --%s value %q: use %s", flag.name, value, strings.Join(values, " or "))
}

func findCommand(name string) (commandSpec, bool) {
	for _, spec := range commandSpecs() {
		if spec.name == name {
			return spec, true
		}
	}
	return commandSpec{}, false
}

func commandSpecs() []commandSpec {
	return []commandSpec{
		{
			name:    "tui",
			summary: "Open the interactive command center",
			usage:   "tui [--accessible]",
			flags: []flagSpec{
				{name: "accessible", kind: boolFlag, defaultVal: "false", description: "use screen-reader-friendly prompts"},
			},
		},
		{
			name:    "sniff",
			summary: "Decode traffic to typed JSON lines or rich text",
			usage:   "sniff [source flags] [options]",
			examples: []string{
				"n2k sniff --tcp 192.168.4.1:1457",
				"n2k sniff -i can0 --filter 'pgn == 127250'",
				"n2k sniff --file capture.log --timing --unknown",
			},
			flags: append(
				readSourceFlags(),
				flagSpec{name: "filter", short: "f", valueName: "expression", description: "CEL filter, e.g. pgn == 127250"},
				flagSpec{name: "unknown", kind: boolFlag, defaultVal: "false", description: "include undecodable messages"},
				messageOutputFlag(),
			),
		},
		{
			name:    "record",
			summary: "Record a replayable capture or detailed JSON export",
			usage:   "record [source flags] [options]",
			examples: []string{
				"n2k record -i can0 --out capture.log",
				"n2k record --tcp 192.168.4.1:1457 --out observations.jsonl --output-format jsonl",
			},
			flags: append(
				readSourceFlags(),
				flagSpec{name: "overwrite", kind: boolFlag, defaultVal: "false", description: "replace an existing output file (never the input capture)"},
				flagSpec{name: "out", short: "o", valueName: "path", defaultVal: "-", description: "output path, or - for stdout", file: true},
				flagSpec{
					name:        "output-format",
					valueName:   "format",
					defaultVal:  "candump",
					description: "capture format",
					choices: []completionItem{
						{value: "candump", description: "replayable candump text"},
						{value: "jsonl", description: "detailed JSON export (cannot replay)"},
					},
				},
			),
		},
		{
			name:    "replay",
			summary: "Replay and decode a candump capture",
			usage:   "replay [capture] [options]",
			minArgs: 0,
			maxArgs: 1,
			examples: []string{
				"n2k replay capture.log",
				"n2k replay --file capture.log --timing=false --unknown",
			},
			flags: []flagSpec{
				{name: "file", valueName: "path", description: "capture file (or pass it positionally)", file: true},
				{name: "timing", kind: boolFlag, defaultVal: "true", description: "pace by capture timestamps"},
				{name: "filter", short: "f", valueName: "expression", description: "CEL filter expression"},
				{name: "unknown", kind: boolFlag, defaultVal: "false", description: "include undecodable messages"},
				messageOutputFlag(),
			},
		},
		{
			name:    "validate",
			summary: "Check a source for undecodable messages",
			usage:   "validate [source flags] [--strict]",
			examples: []string{
				"n2k validate --file capture.log",
				"n2k validate --file capture.log --strict",
			},
			flags: append(
				readSourceFlags(),
				summaryOutputFlag(),
				flagSpec{name: "strict", kind: boolFlag, defaultVal: "false", description: "fail when undecodable messages are found"},
			),
		},
		{
			name:    "devices",
			summary: "Inventory devices from a live network or capture",
			usage:   "devices [list] [source flags] [options]",
			minArgs: 0,
			maxArgs: 1,
			examples: []string{
				"n2k devices --tcp 192.168.4.1:1457 --wait 5s",
				"n2k devices -i can0",
				"n2k devices --file capture.log.gz",
				"n2k devices list --file capture.log.gz",
			},
			flags: append(
				readSourceFlags(),
				summaryOutputFlag(),
				flagSpec{name: "wait", valueName: "duration", defaultVal: "3s", kind: durationFlag, description: "live/UDP observation window"},
				flagSpec{name: "claim-timeout", valueName: "duration", defaultVal: "2s", kind: durationFlag, description: "writable-source address-claim timeout"},
			),
		},
		{
			name:    "pgn",
			summary: "Describe or list typed PGN support",
			usage:   "pgn <number|name|list> [--output json|text]",
			flags:   []flagSpec{summaryOutputFlag()},
			minArgs: 1,
			maxArgs: 1,
			examples: []string{
				"n2k pgn 127250",
				"n2k pgn list",
				"n2k pgn heading --output text",
			},
		},
		{
			name:    "completion",
			summary: "Generate shell completion",
			usage:   "completion <bash|zsh|fish|powershell>",
			minArgs: 1,
			maxArgs: 1,
		},
		{
			name:    "version",
			summary: "Print version information",
			usage:   "version",
			maxArgs: 0,
		},
		{
			name:    "update",
			summary: "Check for and install the latest n2k release",
			usage:   "update [--check] [--force] [--method <method>]",
			maxArgs: 0,
			examples: []string{
				"n2k update",
				"n2k update --check",
				"n2k update --method homebrew",
			},
			flags: []flagSpec{
				{name: "check", kind: boolFlag, defaultVal: "false", description: "check without installing"},
				{name: "force", kind: boolFlag, defaultVal: "false", description: "reinstall the latest release"},
				{
					name:        "method",
					valueName:   "method",
					defaultVal:  string(updateMethodAuto),
					description: "installation method",
					choices: []completionItem{
						{value: string(updateMethodAuto), description: "detect Homebrew, go install, or release binary"},
						{value: string(updateMethodHomebrew), description: "upgrade the Homebrew cask"},
						{value: string(updateMethodGoInstall), description: "build the release with go install"},
						{value: string(updateMethodSelfUpdate), description: "replace this binary from a verified release asset"},
					},
				},
			},
		},
		{
			name:    "uninstall",
			summary: "Remove n2k from this machine",
			usage:   "uninstall",
			maxArgs: 0,
		},
	}
}

func readSourceFlags() []flagSpec {
	return append(
		writableSourceFlags(),
		flagSpec{name: "file", valueName: "path", description: "candump -L/-l capture file (plain or gzip)", file: true},
		flagSpec{name: "udp", valueName: "address", description: "UDP listen address, e.g. :1457"},
		flagSpec{name: "timing", kind: boolFlag, defaultVal: "false", description: "with --file, pace by capture timestamps"},
	)
}

func writableSourceFlags() []flagSpec {
	return []flagSpec{
		{name: "interface", short: "i", valueName: "name", description: "SocketCAN interface, e.g. can0"},
		{name: "usb", short: "u", valueName: "path", description: "USB-CAN serial port, e.g. /dev/ttyUSB0", file: true},
		{name: "tcp", valueName: "address", description: "TCP gateway address, e.g. 192.168.4.1:1457"},
		{
			name:        "format",
			valueName:   "format",
			defaultVal:  "raw",
			description: "stream format for network gateways",
			choices: []completionItem{
				{value: "raw", description: "Yacht Devices RAW ASCII"},
				{value: "actisense", description: "Actisense binary framing"},
			},
		},
	}
}

func messageOutputFlag() flagSpec {
	return flagSpec{
		name:        "output",
		valueName:   "format",
		defaultVal:  messageOutputJSON,
		description: "message output",
		choices: []completionItem{
			{value: messageOutputJSON, description: "typed JSON lines with wire values"},
			{value: messageOutputText, description: "physical values, units, and concrete PGN types"},
		},
	}
}

func writeRootHelp(out io.Writer) error {
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "n2k — NMEA 2000 capture, diagnostics, and schema tools")
	_, _ = fmt.Fprintln(writer)
	_, _ = fmt.Fprintln(writer, "Usage:")
	_, _ = fmt.Fprintln(writer, "  n2k                     Open the interactive command center")
	_, _ = fmt.Fprintln(writer, "  n2k <command> [flags]   Run a scriptable command")
	_, _ = fmt.Fprintln(writer)
	_, _ = fmt.Fprintln(writer, "Commands:")
	for _, spec := range commandSpecs() {
		_, _ = fmt.Fprintf(writer, "  %s\t%s\n", spec.name, spec.summary)
	}
	_, _ = fmt.Fprintln(writer)
	_, _ = fmt.Fprintln(writer, "Developer experience:")
	_, _ = fmt.Fprintln(writer, "  • Run without arguments for a searchable Bubble Tea command palette.")
	_, _ = fmt.Fprintln(writer, "  • Use n2k help <command> for flags, defaults, choices, and examples.")
	_, _ = fmt.Fprintln(writer, "  • Enable shell completion with n2k completion <shell>.")
	_, _ = fmt.Fprintln(writer, "  • Check and install releases with n2k update.")
	_, _ = fmt.Fprintln(writer, "  • Remove n2k and its update cache with n2k uninstall.")
	_, _ = fmt.Fprintln(writer)
	_, _ = fmt.Fprintln(writer, "Examples:")
	_, _ = fmt.Fprintln(writer, "  n2k sniff --tcp 192.168.4.1:1457")
	_, _ = fmt.Fprintln(writer, "  n2k record -i can0 --out capture.log")
	_, _ = fmt.Fprintln(writer, "  n2k replay capture.log --timing=false")
	_, _ = fmt.Fprintln(writer, "  n2k pgn 127250")
	return writer.Flush()
}

func writeCommandHelp(out io.Writer, spec commandSpec) error {
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(writer, "n2k %s — %s\n\n", spec.name, spec.summary)
	_, _ = fmt.Fprintf(writer, "Usage:\n  n2k %s\n", spec.usage)
	if len(spec.flags) > 0 {
		_, _ = fmt.Fprintln(writer)
		_, _ = fmt.Fprintln(writer, "Flags:")
		_, _ = fmt.Fprintln(writer, "  -h, --help\tShow command help")
		for _, flag := range spec.flags {
			label := "      --" + flag.name
			if flag.short != "" {
				label = "  -" + flag.short + ", --" + flag.name
			}
			if flag.kind != boolFlag {
				label += " <" + flag.valueName + ">"
			}
			description := flag.description
			if flag.defaultVal != "" {
				description += " (default " + flag.defaultVal + ")"
			}
			if len(flag.choices) > 0 {
				values := make([]string, 0, len(flag.choices))
				for _, choice := range flag.choices {
					values = append(values, choice.value)
				}
				description += " [" + strings.Join(values, "|") + "]"
			}
			_, _ = fmt.Fprintf(writer, "%s\t%s\n", label, description)
		}
	}
	if len(spec.examples) > 0 {
		_, _ = fmt.Fprintln(writer)
		_, _ = fmt.Fprintln(writer, "Examples:")
		for _, example := range spec.examples {
			_, _ = fmt.Fprintf(writer, "  %s\n", example)
		}
	}
	return writer.Flush()
}

func unknownCommandError(name string) error {
	names := make([]string, 0, len(commandSpecs()))
	for _, spec := range commandSpecs() {
		names = append(names, spec.name)
	}
	if suggestion := closest(name, names); suggestion != "" {
		return fmt.Errorf("unknown command %q; did you mean %q?", name, suggestion)
	}
	return fmt.Errorf("unknown command %q; run n2k --help", name)
}

func unknownFlagError(spec commandSpec, name string) error {
	names := make([]string, 0, len(spec.flags)*2)
	for _, flag := range spec.flags {
		names = append(names, "--"+flag.name)
		if flag.short != "" {
			names = append(names, "-"+flag.short)
		}
	}
	if suggestion := closest(name, names); suggestion != "" {
		return fmt.Errorf("unknown flag %q for n2k %s; did you mean %q?", name, spec.name, suggestion)
	}
	return fmt.Errorf("unknown flag %q for n2k %s; run n2k help %s", name, spec.name, spec.name)
}

func closest(value string, candidates []string) string {
	best := ""
	bestDistance := len(value) + 1
	for _, candidate := range candidates {
		distance := editDistance(value, candidate)
		if distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	threshold := 2
	if len(value) >= 8 {
		threshold = 3
	}
	if bestDistance > threshold {
		return ""
	}
	return best
}

func editDistance(left, right string) int {
	a, b := []rune(left), []rune(right)
	previous := make([]int, len(b)+1)
	for index := range previous {
		previous[index] = index
	}
	for i, leftRune := range a {
		current := make([]int, len(b)+1)
		current[0] = i + 1
		for j, rightRune := range b {
			cost := 0
			if leftRune != rightRune {
				cost = 1
			}
			current[j+1] = min(
				current[j]+1,
				previous[j+1]+1,
				previous[j]+cost,
			)
		}
		previous = current
	}
	return previous[len(b)]
}
