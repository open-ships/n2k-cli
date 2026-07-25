package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/open-ships/n2k/pgn"
)

func completionCandidates(args []string) []completionItem {
	if len(args) == 0 {
		return commandCompletions("")
	}

	commandName := args[0]
	spec, found := findCommand(commandName)
	if !found {
		if len(args) == 1 {
			return commandCompletions(commandName)
		}
		return nil
	}

	rest := args[1:]
	if len(rest) == 0 {
		rest = []string{""}
	}
	current := rest[len(rest)-1]
	previous := rest[:len(rest)-1]

	if strings.HasPrefix(current, "--") {
		if nameValue := strings.TrimPrefix(current, "--"); strings.Contains(nameValue, "=") {
			name, prefix, _ := strings.Cut(nameValue, "=")
			if flag, ok := findLongFlag(spec, name); ok {
				items := completeFlagValue(flag, prefix)
				for index := range items {
					items[index].value = "--" + name + "=" + items[index].value
				}
				return items
			}
		}
		return flagCompletions(spec, current, previous)
	}
	if strings.HasPrefix(current, "-") && current != "-" {
		return flagCompletions(spec, current, previous)
	}

	if flag, found := valueFlagBefore(spec, previous); found {
		return completeFlagValue(flag, current)
	}

	switch spec.name {
	case "pgn":
		return completePGNs(current)
	case "completion":
		return filterCompletions([]completionItem{
			{value: "bash", description: "Bash completion"},
			{value: "zsh", description: "Zsh completion"},
			{value: "fish", description: "Fish completion"},
			{value: "powershell", description: "PowerShell completion"},
		}, current)
	case "replay":
		if positionalCount(spec, previous) == 0 {
			return completeFiles(current)
		}
	case "devices":
		if positionalCount(spec, previous) == 0 {
			return filterCompletions([]completionItem{
				{value: "list", description: "Inventory observed devices"},
			}, current)
		}
	}
	return nil
}

func commandCompletions(prefix string) []completionItem {
	items := make([]completionItem, 0, len(commandSpecs())+1)
	for _, spec := range commandSpecs() {
		items = append(items, completionItem{value: spec.name, description: spec.summary})
	}
	items = append(items, completionItem{value: "help", description: "Show root or command help"})
	return filterCompletions(items, prefix)
}

func flagCompletions(spec commandSpec, prefix string, previous []string) []completionItem {
	used := make(map[string]bool)
	for _, token := range previous {
		if strings.HasPrefix(token, "--") {
			name := strings.TrimPrefix(strings.SplitN(token, "=", 2)[0], "--")
			used[name] = true
			continue
		}
		if strings.HasPrefix(token, "-") {
			if flag, found := findShortFlag(spec, strings.TrimPrefix(strings.SplitN(token, "=", 2)[0], "-")); found {
				used[flag.name] = true
			}
		}
	}

	items := []completionItem{{value: "--help", description: "Show command help"}}
	for _, flag := range spec.flags {
		if used[flag.name] {
			continue
		}
		suffix := ""
		if flag.kind != boolFlag {
			suffix = "="
		}
		items = append(items, completionItem{
			value:       "--" + flag.name + suffix,
			description: flag.description,
		})
		if flag.short != "" {
			items = append(items, completionItem{
				value:       "-" + flag.short,
				description: flag.description,
			})
		}
	}
	return filterCompletions(items, prefix)
}

func valueFlagBefore(spec commandSpec, previous []string) (flagSpec, bool) {
	if len(previous) == 0 {
		return flagSpec{}, false
	}
	token := previous[len(previous)-1]
	if strings.HasPrefix(token, "--") {
		name := strings.TrimPrefix(token, "--")
		if strings.Contains(name, "=") {
			return flagSpec{}, false
		}
		flag, found := findLongFlag(spec, name)
		return flag, found && flag.kind != boolFlag
	}
	if strings.HasPrefix(token, "-") {
		flag, found := findShortFlag(spec, strings.TrimPrefix(token, "-"))
		return flag, found && flag.kind != boolFlag
	}
	return flagSpec{}, false
}

func findLongFlag(spec commandSpec, name string) (flagSpec, bool) {
	for _, flag := range spec.flags {
		if flag.name == name {
			return flag, true
		}
	}
	return flagSpec{}, false
}

func findShortFlag(spec commandSpec, name string) (flagSpec, bool) {
	for _, flag := range spec.flags {
		if flag.short == name {
			return flag, true
		}
	}
	return flagSpec{}, false
}

func completeFlagValue(flag flagSpec, prefix string) []completionItem {
	if flag.name == "usb" {
		return completeUSBDevices(prefix)
	}
	if flag.file {
		return completeFiles(prefix)
	}
	if len(flag.choices) > 0 {
		return filterCompletions(flag.choices, prefix)
	}
	var items []completionItem
	switch flag.name {
	case "interface":
		items = socketCANInterfaces()
	case "tcp":
		items = []completionItem{
			{value: "192.168.4.1:1457", description: "common Yacht Devices gateway address"},
			{value: "localhost:1457", description: "local gateway endpoint"},
		}
	case "udp":
		items = []completionItem{
			{value: ":1457", description: "listen on all interfaces"},
			{value: "127.0.0.1:1457", description: "local UDP endpoint"},
		}
	case "filter":
		items = []completionItem{
			{value: "pgn == 127250", description: "Vessel Heading only"},
			{value: "source == 0", description: "source address zero"},
			{value: "priority <= 3", description: "high-priority traffic"},
		}
	case "wait":
		items = durationCompletions()
	case "claim-timeout":
		items = []completionItem{
			{value: "1500ms", description: "library default contention window"},
			{value: "2s", description: "CLI default"},
			{value: "3s", description: "busy network"},
		}
	}
	return filterCompletions(items, prefix)
}

func durationCompletions() []completionItem {
	return []completionItem{
		{value: "1s", description: "quick sample"},
		{value: "3s", description: "default discovery window"},
		{value: "5s", description: "normal discovery"},
		{value: "10s", description: "extended discovery"},
	}
}

func completePGNs(prefix string) []completionItem {
	items := make([]completionItem, 0, len(pgn.PgnInfoLookup)+1)
	if strings.HasPrefix("list", prefix) {
		items = append(items, completionItem{value: "list", description: "List every typed PGN variant"})
	}
	for _, number := range sortedPGNNumbers() {
		value := strconv.FormatUint(uint64(number), 10)
		if !strings.HasPrefix(value, prefix) {
			continue
		}
		description := "typed PGN"
		if infos := pgn.PgnInfoLookup[number]; len(infos) > 0 && infos[0].Description != "" {
			description = sanitizeDescription(infos[0].Description)
		}
		items = append(items, completionItem{value: value, description: description})
	}
	return items
}

func completeFiles(prefix string) []completionItem {
	displayDir, scanDir, base := splitCompletionPath(prefix)
	entries, err := os.ReadDir(scanDir)
	if err != nil {
		return nil
	}
	items := make([]completionItem, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), base) {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		value := filepath.Join(displayDir, entry.Name())
		if displayDir == "" {
			value = entry.Name()
		}
		description := "file"
		if entry.IsDir() {
			value += string(filepath.Separator)
			description = "directory"
		}
		items = append(items, completionItem{value: value, description: description})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].value < items[j].value })
	return items
}

func splitCompletionPath(prefix string) (displayDir, scanDir, base string) {
	if prefix == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return "~" + string(filepath.Separator), home, ""
		}
	}
	expanded := prefix
	if strings.HasPrefix(prefix, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, strings.TrimPrefix(prefix, "~/"))
		}
	}
	displayDir, base = filepath.Split(prefix)
	scanDir, _ = filepath.Split(expanded)
	if scanDir == "" {
		scanDir = "."
	}
	return displayDir, scanDir, base
}

func socketCANInterfaces() []completionItem {
	var items []completionItem
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, networkInterface := range interfaces {
			name := networkInterface.Name
			if !strings.Contains(strings.ToLower(name), "can") {
				continue
			}
			items = append(items, completionItem{
				value:       name,
				description: "detected CAN network interface",
			})
		}
	}
	if len(items) == 0 {
		items = []completionItem{
			{value: "can0", description: "primary SocketCAN interface"},
			{value: "vcan0", description: "virtual SocketCAN interface"},
		}
	}
	return items
}

func completeUSBDevices(prefix string) []completionItem {
	patterns := []string{
		"/dev/ttyUSB*",
		"/dev/ttyACM*",
		"/dev/cu.usb*",
		"/dev/cu.SLAB*",
	}
	seen := make(map[string]bool)
	var items []completionItem
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			if seen[match] || !strings.HasPrefix(match, prefix) {
				continue
			}
			seen[match] = true
			items = append(items, completionItem{
				value:       match,
				description: "detected serial device",
			})
		}
	}
	if len(items) == 0 && prefix != "" {
		return completeFiles(prefix)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].value < items[j].value })
	return items
}

func positionalCount(spec commandSpec, tokens []string) int {
	count := 0
	expectingValue := false
	for _, token := range tokens {
		if expectingValue {
			expectingValue = false
			continue
		}
		if strings.HasPrefix(token, "--") {
			name := strings.TrimPrefix(strings.SplitN(token, "=", 2)[0], "--")
			if flag, found := findLongFlag(spec, name); found && flag.kind != boolFlag && !strings.Contains(token, "=") {
				expectingValue = true
			}
			continue
		}
		if strings.HasPrefix(token, "-") {
			if flag, found := findShortFlag(spec, strings.TrimPrefix(token, "-")); found && flag.kind != boolFlag {
				expectingValue = true
			}
			continue
		}
		count++
	}
	return count
}

func filterCompletions(items []completionItem, prefix string) []completionItem {
	filtered := make([]completionItem, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.value, prefix) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func writeCompletionCandidates(out io.Writer, items []completionItem) error {
	for _, item := range items {
		if _, err := fmt.Fprintf(out, "%s\t%s\n", item.value, sanitizeDescription(item.description)); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeDescription(value string) string {
	return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(value)
}

func writeShellCompletion(out io.Writer, shell string) error {
	var script string
	switch shell {
	case "bash":
		script = bashCompletion
	case "zsh":
		script = zshCompletion
	case "fish":
		script = fishCompletion
	case "powershell":
		script = powershellCompletion
	default:
		return fmt.Errorf("unknown shell %q: use bash, zsh, fish, or powershell", shell)
	}
	_, err := io.WriteString(out, script)
	return err
}

const bashCompletion = `# n2k dynamic completion
_n2k_complete() {
    local line value description
    COMPREPLY=()
    while IFS=$'\t' read -r value description; do
        COMPREPLY+=("$value")
    done < <(command n2k __complete "${COMP_WORDS[@]:1:$COMP_CWORD}")
    compopt -o filenames 2>/dev/null || true
}
complete -F _n2k_complete n2k
`

const zshCompletion = `#compdef n2k
_n2k() {
    local line value description
    local -a candidates descriptions
    while IFS=$'\t' read -r value description; do
        candidates+=("$value")
        descriptions+=("$description")
    done < <(command n2k __complete "${words[@]:2}")
    compadd -d descriptions -- "${candidates[@]}"
}
compdef _n2k n2k
`

const fishCompletion = `function __n2k_complete
    set -l tokens (commandline -opc)
    set -e tokens[1]
    set -a tokens (commandline -ct)
    command n2k __complete $tokens
end
complete -c n2k -f -a '(__n2k_complete)'
`

const powershellCompletion = `Register-ArgumentCompleter -Native -CommandName n2k -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $arguments = @($commandAst.CommandElements | Select-Object -Skip 1 | ForEach-Object { $_.Extent.Text })
    if ($arguments.Count -eq 0 -or $arguments[-1] -ne $wordToComplete) {
        $arguments += $wordToComplete
    }
    n2k __complete @arguments | ForEach-Object {
        $parts = $_ -split [char]9, 2
        [System.Management.Automation.CompletionResult]::new(
            $parts[0], $parts[0], 'ParameterValue', $(if ($parts.Count -gt 1) { $parts[1] } else { $parts[0] })
        )
    }
}
`
