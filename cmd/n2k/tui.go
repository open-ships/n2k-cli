package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/open-ships/n2k/pgn"
)

type paletteItem struct {
	command     string
	title       string
	description string
}

func (item paletteItem) Title() string       { return item.title }
func (item paletteItem) Description() string { return item.description }
func (item paletteItem) FilterValue() string {
	return item.command + " " + item.title + " " + item.description
}

type paletteModel struct {
	list     list.Model
	selected string
	canceled bool
	width    int
	height   int
}

func newPaletteModel() paletteModel {
	items := commandPaletteItems()
	listItems := make([]list.Item, 0, len(items))
	for _, item := range items {
		listItems = append(listItems, item)
	}
	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(1)
	model := list.New(listItems, delegate, 84, 24)
	model.Title = "N2K COMMAND CENTER"
	model.SetShowStatusBar(true)
	model.SetFilteringEnabled(true)
	model.SetStatusBarItemName("workflow", "workflows")
	model.InfiniteScrolling = true
	return paletteModel{list: model, width: 84, height: 24}
}

func commandPaletteItems() []paletteItem {
	return []paletteItem{
		{
			command:     "sniff",
			title:       "Sniff decoded traffic",
			description: "Stream typed JSON or physical-value text from CAN, USB, gateway, or capture",
		},
		{
			command:     "record",
			title:       "Record raw observations",
			description: "Capture replayable candump or context-rich observation JSON lines",
		},
		{
			command:     "replay",
			title:       "Replay a capture",
			description: "Decode a candump file with optional source timing and CEL filtering",
		},
		{
			command:     "validate",
			title:       "Validate a source",
			description: "Summarize typed and undecodable messages; optionally enforce strict mode",
		},
		{
			command:     "devices",
			title:       "Inventory network devices",
			description: "Actively discover a live bus or inspect device identities captured in a file",
		},
		{
			command:     "pgn",
			title:       "Explore the PGN schema",
			description: "Autocomplete known PGNs and inspect fields, ranges, units, and confidence",
		},
		{
			command:     "update",
			title:       "Update n2k",
			description: "Check GitHub releases and update through Homebrew, Go, or a verified binary",
		},
	}
}

func (model paletteModel) Init() tea.Cmd {
	return tea.RequestBackgroundColor
}

func (model paletteModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		model.width = max(48, message.Width)
		model.height = max(16, message.Height)
		model.list.SetSize(model.width-6, model.height-8)
	case tea.BackgroundColorMsg:
		model.list.Styles = list.DefaultStyles(message.IsDark())
		delegate := list.NewDefaultDelegate()
		delegate.Styles = list.NewDefaultItemStyles(message.IsDark())
		delegate.SetSpacing(1)
		model.list.SetDelegate(delegate)
	case tea.KeyPressMsg:
		key := message.String()
		if model.list.FilterState() != list.Filtering {
			switch key {
			case "q", "esc", "ctrl+c":
				model.canceled = true
				return model, tea.Quit
			case "enter":
				if item, ok := model.list.SelectedItem().(paletteItem); ok {
					model.selected = item.command
					return model, tea.Quit
				}
			case "1", "2", "3", "4", "5", "6", "7":
				index := int(key[0] - '1')
				items := commandPaletteItems()
				if index >= 0 && index < len(items) {
					model.selected = items[index].command
					return model, tea.Quit
				}
			}
		}
	}

	var command tea.Cmd
	model.list, command = model.list.Update(message)
	return model, command
}

func (model paletteModel) View() tea.View {
	accent := lipgloss.Color("#20C997")
	muted := lipgloss.Color("#7C8799")
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(accent).
		Render("n2k")
	subtitle := lipgloss.NewStyle().
		Foreground(muted).
		Render("NMEA 2000 developer console")
	hint := lipgloss.NewStyle().
		Foreground(muted).
		Render("1–7 jump  •  / filter  •  enter configure  •  ? help  •  q quit")
	body := lipgloss.JoinVertical(
		lipgloss.Left,
		title+"  "+subtitle,
		"",
		model.list.View(),
		"",
		hint,
	)
	view := tea.NewView(lipgloss.NewStyle().Padding(1, 3).Render(body))
	view.AltScreen = true
	view.WindowTitle = "n2k command center"
	return view
}

func (app *cli) runInteractive(ctx context.Context, accessible bool) error {
	accessible = accessible || envTruthy("N2K_ACCESSIBLE")
	updated, err := app.maybeOfferUpdate(ctx, accessible)
	if err != nil || updated {
		return err
	}
	command, selected, err := app.chooseCommand(ctx, accessible)
	if err != nil || !selected {
		return err
	}
	args, confirmed, err := app.configureCommand(ctx, command, accessible)
	if err != nil || !confirmed {
		return err
	}
	_, _ = fmt.Fprintf(app.errOut, "\n%s\n\n", renderCommandPreview(args))
	return app.ExecuteContext(ctx, args)
}

func (app *cli) chooseCommand(ctx context.Context, accessible bool) (string, bool, error) {
	if accessible {
		var command string
		options := make([]huh.Option[string], 0, len(commandPaletteItems()))
		for _, item := range commandPaletteItems() {
			options = append(options, huh.NewOption(item.title+" — "+item.description, item.command))
		}
		form := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Choose an NMEA 2000 workflow").
				Description("Type a number or use the arrow keys").
				Options(options...).
				Value(&command),
		))
		ok, err := app.runForm(ctx, form, true)
		return command, ok, err
	}

	program := tea.NewProgram(
		newPaletteModel(),
		tea.WithContext(ctx),
		tea.WithInput(app.in),
		tea.WithOutput(app.errOut),
	)
	finalModel, err := program.Run()
	if err != nil {
		if ctx.Err() != nil {
			return "", false, nil
		}
		return "", false, fmt.Errorf("running command center: %w", err)
	}
	model, ok := finalModel.(paletteModel)
	if !ok {
		return "", false, errors.New("command center returned an unexpected model")
	}
	return model.selected, !model.canceled && model.selected != "", nil
}

type wizardConfig struct {
	command       string
	source        string
	file          string
	iface         string
	usb           string
	tcp           string
	udp           string
	format        string
	timing        bool
	filter        string
	unknown       bool
	output        string
	outputPath    string
	recordFormat  string
	strict        bool
	wait          string
	claimTimeout  string
	pgnAction     string
	pgnNumber     string
	run           bool
	allowReadOnly bool
	allowTiming   bool
}

func defaultWizardConfig(command string) *wizardConfig {
	return &wizardConfig{
		command:       command,
		source:        "file",
		format:        "raw",
		output:        "json",
		outputPath:    "-",
		recordFormat:  "candump",
		wait:          "3s",
		claimTimeout:  "2s",
		pgnAction:     "describe",
		pgnNumber:     "127250",
		run:           true,
		allowReadOnly: true,
		allowTiming:   true,
	}
}

func (app *cli) configureCommand(ctx context.Context, command string, accessible bool) ([]string, bool, error) {
	config := defaultWizardConfig(command)
	var groups []*huh.Group
	switch command {
	case "sniff", "record", "validate", "devices":
		groups = append(groups, sourceWizardGroups(config)...)
		groups = append(groups, commandOptionGroups(config)...)
	case "replay":
		config.timing = true
		groups = append(groups, replayWizardGroups(config)...)
	case "pgn":
		groups = append(groups, pgnWizardGroups(config)...)
	case "update":
	default:
		return nil, false, fmt.Errorf("no interactive workflow for %q", command)
	}

	groups = append(groups, confirmationGroup(config))
	form := huh.NewForm(groups...).WithShowHelp(true).WithShowErrors(true)
	ok, err := app.runForm(ctx, form, accessible)
	if err != nil || !ok || !config.run {
		return nil, false, err
	}
	return config.args(), true, nil
}

func (app *cli) maybeOfferUpdate(ctx context.Context, accessible bool) (bool, error) {
	if app.updater == nil ||
		!app.canUseTUI() ||
		envTruthy("N2K_NO_UPDATE_CHECK") ||
		envTruthy("CI") ||
		currentBuildVersion().version == "dev" {
		return false, nil
	}
	cachePath, err := updateCheckCachePath()
	if err != nil || !updateCheckDue(cachePath, time.Now()) {
		return false, nil
	}

	checkedAt := time.Now()
	checkCtx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	status, checkErr := app.updater.Check(checkCtx, updateMethodAuto)
	cancel()
	_ = recordUpdateCheck(cachePath, checkedAt)
	if checkErr != nil || !status.Found || !status.Available {
		return false, nil
	}

	install := envTruthy("N2K_AUTO_UPDATE")
	if !install {
		install = true
		form := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("n2k %s is available", status.LatestVersion)).
				Description(fmt.Sprintf(
					"You have %s. Install via %s now?\n%s",
					status.CurrentVersion,
					status.Method,
					status.ReleaseURL,
				)).
				Affirmative("Update").
				Negative("Later").
				Value(&install),
		))
		ok, formErr := app.runForm(ctx, form, accessible)
		if formErr != nil || !ok || !install {
			return false, formErr
		}
	}

	_, _ = fmt.Fprintf(app.errOut, "Updating n2k to %s via %s…\n", status.LatestVersion, status.Method)
	if err := app.updater.Install(ctx, status, app.in, app.out, app.errOut); err != nil {
		return false, fmt.Errorf("automatic update failed: %w", err)
	}
	_, err = fmt.Fprintf(app.out, "Updated n2k to %s. Run n2k again to use the new version.\n", status.LatestVersion)
	return true, err
}

func sourceWizardGroups(config *wizardConfig) []*huh.Group {
	options := []huh.Option[string]{
		huh.NewOption("Capture file — candump replay", "file"),
		huh.NewOption("SocketCAN — Linux CAN interface", "interface"),
		huh.NewOption("USB-CAN — serial adapter", "usb"),
		huh.NewOption("TCP — Yacht Devices or Actisense gateway", "tcp"),
	}
	if config.allowReadOnly {
		options = append(options, huh.NewOption("UDP — gateway broadcast listener", "udp"))
	}
	if !config.allowReadOnly {
		config.source = "interface"
	}

	groups := []*huh.Group{
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Choose a traffic source").
				Description("Search with /, navigate with arrows or j/k").
				Options(options...).
				Value(&config.source),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Capture path").
				Description("Plain or gzip candump -L/-l format; Tab accepts a suggestion").
				Placeholder("capture.log").
				Suggestions([]string{"testdata/sample.log", "capture.log", "capture.log.gz"}).
				Value(&config.file).
				Validate(existingPath),
		).WithHideFunc(func() bool { return config.source != "file" }),
		huh.NewGroup(
			huh.NewInput().
				Title("SocketCAN interface").
				Placeholder("can0").
				Suggestions([]string{"can0", "vcan0"}).
				Value(&config.iface).
				Validate(required("interface")),
		).WithHideFunc(func() bool { return config.source != "interface" }),
		huh.NewGroup(
			huh.NewInput().
				Title("USB-CAN serial port").
				Placeholder("/dev/ttyUSB0").
				Suggestions([]string{"/dev/ttyUSB0", "/dev/ttyACM0"}).
				Value(&config.usb).
				Validate(required("serial port")),
		).WithHideFunc(func() bool { return config.source != "usb" }),
		huh.NewGroup(
			huh.NewInput().
				Title("TCP gateway address").
				Placeholder("192.168.4.1:1457").
				Suggestions([]string{"192.168.4.1:1457", "localhost:1457"}).
				Value(&config.tcp).
				Validate(required("TCP address")),
		).WithHideFunc(func() bool { return config.source != "tcp" }),
	}
	if config.allowReadOnly {
		groups = append(groups,
			huh.NewGroup(
				huh.NewInput().
					Title("UDP listen address").
					Placeholder(":1457").
					Suggestions([]string{":1457", "127.0.0.1:1457"}).
					Value(&config.udp).
					Validate(required("UDP address")),
			).WithHideFunc(func() bool { return config.source != "udp" }),
		)
	}
	groups = append(groups,
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Gateway stream format").
				Options(
					huh.NewOption("Yacht Devices RAW ASCII", "raw"),
					huh.NewOption("Actisense binary framing", "actisense"),
				).
				Value(&config.format),
		).WithHideFunc(func() bool { return config.source != "tcp" && config.source != "udp" }),
	)
	if config.allowTiming {
		groups = append(groups,
			huh.NewGroup(
				huh.NewConfirm().
					Title("Replay with original capture timing?").
					Description("Off reads the file as fast as possible").
					Value(&config.timing),
			).WithHideFunc(func() bool { return config.source != "file" }),
		)
	}
	return groups
}

func commandOptionGroups(config *wizardConfig) []*huh.Group {
	switch config.command {
	case "sniff":
		return messageOptionGroups(config)
	case "record":
		return []*huh.Group{
			huh.NewGroup(
				huh.NewInput().
					Title("Output path").
					Description("Use - to stream to stdout").
					Value(&config.outputPath).
					Suggestions([]string{"-", "capture.log", "observations.jsonl"}).
					Validate(required("output path")),
				huh.NewSelect[string]().
					Title("Capture format").
					Options(
						huh.NewOption("Replayable candump text", "candump"),
						huh.NewOption("Owned observation JSON lines", "jsonl"),
					).
					Value(&config.recordFormat),
			),
		}
	case "validate":
		return []*huh.Group{
			huh.NewGroup(
				huh.NewConfirm().
					Title("Enable strict mode?").
					Description("Exit non-zero when undecodable messages are found").
					Value(&config.strict),
			),
		}
	case "devices":
		return []*huh.Group{
			huh.NewGroup(
				huh.NewInput().
					Title("Discovery window").
					Description("How long to observe a live source").
					Suggestions([]string{"3s", "5s", "10s"}).
					Value(&config.wait).
					Validate(durationValidator("discovery window")),
			).WithHideFunc(func() bool { return config.source == "file" }),
			huh.NewGroup(
				huh.NewInput().
					Title("Address-claim timeout").
					Description("Used when the CLI joins a writable bus").
					Suggestions([]string{"1500ms", "2s", "3s"}).
					Value(&config.claimTimeout).
					Validate(durationValidator("claim timeout")),
			).WithHideFunc(func() bool { return config.source == "file" || config.source == "udp" }),
		}
	default:
		return nil
	}
}

func messageOptionGroups(config *wizardConfig) []*huh.Group {
	return []*huh.Group{
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Message output").
				Options(
					huh.NewOption("Typed JSON lines with exact wire values", "json"),
					huh.NewOption("Concrete PGN types with physical values and units", "text"),
				).
				Value(&config.output),
			huh.NewInput().
				Title("CEL filter").
				Description("Optional; metadata-only filters avoid decode work").
				Placeholder("pgn == 127250").
				Suggestions([]string{"pgn == 127250", "source == 0", "priority <= 3"}).
				Value(&config.filter),
			huh.NewConfirm().
				Title("Include unknown PGNs?").
				Value(&config.unknown),
		),
	}
}

func replayWizardGroups(config *wizardConfig) []*huh.Group {
	return append(
		[]*huh.Group{
			huh.NewGroup(
				huh.NewInput().
					Title("Capture path").
					Placeholder("capture.log").
					Suggestions([]string{"testdata/sample.log", "capture.log", "capture.log.gz"}).
					Value(&config.file).
					Validate(existingPath),
				huh.NewConfirm().
					Title("Preserve original timing?").
					Description("Disable to decode as fast as possible").
					Value(&config.timing),
			),
		},
		messageOptionGroups(config)...,
	)
}

func pgnWizardGroups(config *wizardConfig) []*huh.Group {
	suggestions := make([]string, 0, len(pgn.PgnInfoLookup))
	for _, number := range sortedPGNNumbers() {
		suggestions = append(suggestions, strconv.FormatUint(uint64(number), 10))
	}
	return []*huh.Group{
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("PGN schema action").
				Options(
					huh.NewOption("Describe one PGN", "describe"),
					huh.NewOption("List every typed PGN variant", "list"),
				).
				Value(&config.pgnAction),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("PGN number").
				Description("Type to narrow known PGNs; Tab accepts the suggestion").
				Suggestions(suggestions).
				Value(&config.pgnNumber).
				Validate(validatePGN),
		).WithHideFunc(func() bool { return config.pgnAction == "list" }),
	}
}

func confirmationGroup(config *wizardConfig) *huh.Group {
	return huh.NewGroup(
		huh.NewNote().
			Title("Ready to run").
			DescriptionFunc(func() string {
				return renderCommandPreview(config.args())
			}, config).
			Next(true),
		huh.NewConfirm().
			Title("Run this command now?").
			Affirmative("Run").
			Negative("Cancel").
			Value(&config.run),
	)
}

func (config *wizardConfig) args() []string {
	args := []string{config.command}
	switch config.command {
	case "sniff", "record", "validate", "devices":
		args = append(args, config.sourceArgs()...)
	case "replay":
		args = append(args, config.file, "--timing="+strconv.FormatBool(config.timing))
	case "pgn":
		if config.pgnAction == "list" {
			return append(args, "list")
		}
		return append(args, config.pgnNumber)
	}

	switch config.command {
	case "sniff", "replay":
		args = append(args, "--output", config.output)
		if config.filter != "" {
			args = append(args, "--filter", config.filter)
		}
		if config.unknown {
			args = append(args, "--unknown")
		}
	case "record":
		args = append(args, "--out", config.outputPath, "--output-format", config.recordFormat)
	case "validate":
		if config.strict {
			args = append(args, "--strict")
		}
	case "devices":
		if config.source != "file" {
			args = append(args, "--wait", config.wait)
		}
		if config.source != "file" && config.source != "udp" {
			args = append(args, "--claim-timeout", config.claimTimeout)
		}
	}
	return args
}

func (config *wizardConfig) sourceArgs() []string {
	var args []string
	switch config.source {
	case "file":
		args = append(args, "--file", config.file)
		if config.timing {
			args = append(args, "--timing")
		}
	case "interface":
		args = append(args, "--interface", config.iface)
	case "usb":
		args = append(args, "--usb", config.usb)
	case "tcp":
		args = append(args, "--tcp", config.tcp, "--format", config.format)
	case "udp":
		args = append(args, "--udp", config.udp, "--format", config.format)
	}
	return args
}

func (app *cli) runForm(ctx context.Context, form *huh.Form, accessible bool) (bool, error) {
	err := form.
		WithInput(app.in).
		WithOutput(app.errOut).
		WithAccessible(accessible).
		RunWithContext(ctx)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, huh.ErrUserAborted) || ctx.Err() != nil {
		return false, nil
	}
	return false, fmt.Errorf("running interactive form: %w", err)
}

func existingPath(value string) error {
	if err := required("capture path")(value); err != nil {
		return err
	}
	info, err := os.Stat(value)
	if err != nil {
		return fmt.Errorf("cannot read %q: %w", value, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory", value)
	}
	return nil
}

func required(label string) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
		return nil
	}
}

func durationValidator(label string) func(string) error {
	return func(value string) error {
		duration, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("%s must be a Go duration: %w", label, err)
		}
		if duration <= 0 {
			return fmt.Errorf("%s must be greater than zero", label)
		}
		return nil
	}
}

func validatePGN(value string) error {
	if value == "list" {
		return nil
	}
	number, err := strconv.ParseUint(value, 0, 32)
	if err != nil {
		return fmt.Errorf("enter a decimal or hexadecimal PGN")
	}
	if len(pgn.PgnInfoLookup[uint32(number)]) == 0 {
		return fmt.Errorf("PGN %d is not in the typed metadata", number)
	}
	return nil
}

func renderCommandPreview(args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, "n2k")
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return "$ " + strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`;&|<>()*?[]{}!") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
