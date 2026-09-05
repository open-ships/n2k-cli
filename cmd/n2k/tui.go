package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
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

func (item paletteItem) Title() string {
	for index, choice := range commandPaletteItems() {
		if choice.command == item.command {
			return fmt.Sprintf("%d  %s", index+1, item.title)
		}
	}
	return item.title
}
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
	isDark   bool
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
	return paletteModel{list: model, width: 84, height: 24, isDark: true}
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
			description: "Save a replayable capture or detailed JSON export",
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
			description: "Search PGNs by name or number; inspect fields, ranges, units, and confidence",
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
		model.width = max(1, message.Width)
		model.height = max(1, message.Height)
		model.resizeList()
	case tea.BackgroundColorMsg:
		model.isDark = message.IsDark()
		model.list.Styles = list.DefaultStyles(model.isDark)
		model.resizeList()
	case tea.KeyPressMsg:
		key := message.String()
		if model.list.FilterState() != list.Filtering {
			switch key {
			case "esc":
				if model.list.FilterState() != list.Unfiltered {
					break
				}
				model.canceled = true
				return model, tea.Quit
			case "q", "ctrl+c":
				model.canceled = true
				return model, tea.Quit
			case "enter":
				if item, ok := model.list.SelectedItem().(paletteItem); ok {
					model.selected = item.command
					return model, tea.Quit
				}
			case "1", "2", "3", "4", "5", "6", "7":
				if model.list.FilterState() != list.Unfiltered {
					break
				}
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
	hintText := "1–7 configure  •  / filter  •  enter open  •  ? help  •  q quit"
	if model.list.FilterState() == list.FilterApplied {
		hintText = "esc clear filter  •  enter open  •  q quit"
	} else if model.list.FilterState() == list.Filtering {
		hintText = "enter apply filter  •  esc cancel filter"
	}
	hint := lipgloss.NewStyle().
		Foreground(muted).
		Render(hintText)
	body := lipgloss.JoinVertical(
		lipgloss.Left,
		title+"  "+subtitle,
		"",
		model.list.View(),
		"",
		hint,
	)
	content := lipgloss.NewStyle().Padding(1, 3).Render(body)
	if model.compact() {
		hint := "↑/↓ choose · enter open\n/ search · esc back · q quit"
		if model.list.FilterState() == list.Filtering {
			hint = "enter applies filter\nesc cancels filter"
		}
		content = lipgloss.NewStyle().Padding(0, 1).Render(title + "\n" + model.list.View() + "\n" + hint)
	}
	if model.width < 30 || model.height < 10 {
		content = lipgloss.NewStyle().MaxWidth(model.width).MaxHeight(model.height).Render("Resize terminal to 30×10.\nq to quit")
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "n2k command center"
	return view
}

func (model paletteModel) compact() bool { return model.width < 70 || model.height < 24 }

func (model *paletteModel) resizeList() {
	delegate := list.NewDefaultDelegate()
	delegate.Styles = list.NewDefaultItemStyles(model.isDark)
	delegate.ShowDescription = !model.compact()
	delegate.SetSpacing(0)
	model.list.SetDelegate(delegate)
	model.list.SetShowStatusBar(!model.compact())
	model.list.SetShowHelp(!model.compact())
	if model.compact() {
		model.list.SetSize(max(1, model.width-2), max(1, model.height-5))
	} else {
		model.list.SetSize(max(1, model.width-6), max(1, model.height-8))
	}
}

func (app *cli) runInteractive(ctx context.Context, accessible bool) error {
	accessible = accessible || envTruthy("N2K_ACCESSIBLE")
	updated, err := app.maybeOfferUpdate(ctx, accessible)
	if err != nil || updated {
		return err
	}
	var config *wizardConfig
	action := "another"
	for ctx.Err() == nil {
		if action == "another" {
			command, selected, err := app.chooseCommand(ctx, accessible)
			if err != nil || !selected {
				return err
			}
			config = defaultWizardConfig(command)
			action = "edit"
		}
		if action == "edit" {
			_, confirmed, err := app.configureWizard(ctx, config, accessible)
			if err != nil {
				return err
			}
			if !confirmed {
				action = "another"
				continue
			}
		}
		confirmed, runErr := app.confirmOverwrite(ctx, config, accessible)
		if runErr == nil && !confirmed {
			action = "edit"
			continue
		}
		if runErr == nil {
			runErr = app.executeInteractive(ctx, config)
		}
		if ctx.Err() != nil {
			return nil
		}
		if runErr != nil {
			_, _ = fmt.Fprintf(app.errOut, "\nn2k: %v\nYour settings are preserved.\n", runErr)
		}
		action, err = app.nextWorkflowAction(ctx, runErr != nil, accessible)
		if err != nil || action == "quit" {
			return err
		}
	}
	return nil
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
	overwrite     bool
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
		output:        "text",
		outputPath:    freshCapturePath(time.Now()),
		timing:        command == "replay",
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

func (app *cli) configureWizard(ctx context.Context, config *wizardConfig, accessible bool) ([]string, bool, error) {
	command := config.command
	config.run = true
	config.overwrite = false
	var groups []*huh.Group
	switch command {
	case "sniff", "record", "validate", "devices":
		groups = append(groups, sourceWizardGroups(config)...)
		groups = append(groups, commandOptionGroups(config)...)
	case "replay":
		groups = append(groups, replayWizardGroups(config)...)
	case "pgn":
		groups = append(groups, pgnWizardGroups(config, accessible)...)
	case "update":
	default:
		return nil, false, fmt.Errorf("no interactive workflow for %q", command)
	}

	if command == "devices" || command == "validate" || command == "pgn" {
		groups = append(groups, resultOutputGroup(config))
	}
	if !accessible {
		groups = append(groups, confirmationGroup(config))
	}
	form := huh.NewForm(groups...).WithShowHelp(true).WithShowErrors(true)
	ok, err := app.runForm(ctx, form, accessible)
	if err != nil || !ok || !config.run {
		return nil, false, err
	}
	if accessible {
		// Huh's accessible renderer does not evaluate DescriptionFunc. Build
		// the preview after the answers have been collected so it is reviewable.
		confirmation := huh.NewForm(huh.NewGroup(
			huh.NewNote().Title("Ready to run").Description(renderCommandPreview(config.args())),
			huh.NewConfirm().Title("Run this command now?").Affirmative("Run").Negative("Cancel").Value(&config.run),
		))
		ok, err = app.runForm(ctx, confirmation, true)
		if err != nil || !ok || !config.run {
			return nil, false, err
		}
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
				SuggestionsFunc(func() []string { return pathSuggestions(config.file) }, &config.file).
				Value(&config.file).
				Validate(existingPath),
		).WithHideFunc(func() bool { return config.source != "file" }),
		huh.NewGroup(
			huh.NewInput().
				Title("SocketCAN interface").
				Placeholder("can0").
				SuggestionsFunc(func() []string { return completionValues(socketCANInterfaces()) }, &config.iface).
				Value(&config.iface).
				Validate(required("interface")),
		).WithHideFunc(func() bool { return config.source != "interface" }),
		huh.NewGroup(
			huh.NewInput().
				Title("USB-CAN serial port").
				Placeholder("/dev/ttyUSB0").
				SuggestionsFunc(func() []string { return completionValues(completeUSBDevices(config.usb)) }, &config.usb).
				Value(&config.usb).
				Validate(required("serial port")),
		).WithHideFunc(func() bool { return config.source != "usb" }),
		huh.NewGroup(
			huh.NewInput().
				Title("TCP gateway address").
				Placeholder("192.168.4.1:1457").
				Suggestions([]string{"192.168.4.1:1457", "localhost:1457"}).
				Value(&config.tcp).
				Validate(addressValidator("TCP")),
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
					Validate(addressValidator("UDP")),
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
					Description("A new file is suggested. Existing files require a separate replacement choice; - streams to stdout.").
					Value(&config.outputPath).
					SuggestionsFunc(func() []string { return pathSuggestions(config.outputPath) }, &config.outputPath).
					Validate(required("output path")),
				huh.NewSelect[string]().
					Title("Capture format").
					Options(
						huh.NewOption("Replayable candump text", "candump"),
						huh.NewOption("Detailed JSON export (cannot replay)", "jsonl"),
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
					huh.NewOption("Readable physical values and units", "text"),
					huh.NewOption("JSON lines with exact wire values", "json"),
				).
				Value(&config.output),
			huh.NewInput().
				Title("CEL filter").
				Description("Optional: pgn == 127250 keeps heading messages; Tab completes").
				Placeholder("pgn == 127250").
				Suggestions([]string{"pgn == 127250", "source == 0", "priority <= 3"}).
				Value(&config.filter).
				Validate(validateFilter),
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
					SuggestionsFunc(func() []string { return pathSuggestions(config.file) }, &config.file).
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

func pgnWizardGroups(config *wizardConfig, accessible bool) []*huh.Group {
	options := make([]huh.Option[string], 0, len(pgn.PgnInfoLookup))
	for _, number := range sortedPGNNumbers() {
		infos := pgn.PgnInfoLookup[number]
		label := fmt.Sprintf("%d · %s", number, infos[0].Description)
		if len(infos) > 1 {
			label += fmt.Sprintf(" (+%d variants)", len(infos)-1)
		}
		options = append(options, huh.NewOption(label, strconv.FormatUint(uint64(number), 10)))
	}
	var field huh.Field = huh.NewSelect[string]().Title("Find a PGN").Description("Press / to search by name or number, for example heading or 127250").Options(options...).Height(8).Value(&config.pgnNumber)
	if accessible {
		field = huh.NewInput().Title("PGN number or name").Description("A number shows fields; a name such as heading searches known PGNs.").Value(&config.pgnNumber).Validate(func(value string) error {
			_, err := findPGNs(value)
			return err
		})
	}
	return []*huh.Group{
		huh.NewGroup(huh.NewSelect[string]().Title("PGN schema action").Options(
			huh.NewOption("Describe one PGN", "describe"), huh.NewOption("List known PGNs", "list"),
		).Value(&config.pgnAction)),
		huh.NewGroup(field).WithHideFunc(func() bool { return config.pgnAction == "list" }),
	}
}

func pathSuggestions(prefix string) []string { return completionValues(completeFiles(prefix)) }
func completionValues(items []completionItem) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, item.value)
	}
	return values
}

func resultOutputGroup(config *wizardConfig) *huh.Group {
	return huh.NewGroup(huh.NewSelect[string]().Title("Result output").Options(
		huh.NewOption("Readable summary and tables", "text"), huh.NewOption("JSON for scripts", "json"),
	).Value(&config.output))
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
			return append(args, "list", "--output", config.output)
		}
		return append(args, config.pgnNumber, "--output", config.output)
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
		if config.overwrite {
			args = append(args, "--overwrite")
		}
	case "validate":
		args = append(args, "--output", config.output)
		if config.strict {
			args = append(args, "--strict")
		}
	case "devices":
		args = append(args, "--output", config.output)
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
	if accessible {
		_, _ = fmt.Fprintln(app.errOut, "Follow the prompts. Ctrl+C exits.")
	} else {
		_, _ = fmt.Fprintln(app.errOut, "Esc / Ctrl+C cancels · Shift+Tab goes back")
	}
	err := form.WithKeyMap(wizardKeyMap()).WithProgramOptions(tea.WithFilter(formKeyFilter(form))).
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
	info, err := os.Stat(expandPath(value))
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

func wizardKeyMap() *huh.KeyMap {
	keymap := huh.NewDefaultKeyMap()
	keymap.Input.AcceptSuggestion.SetKeys("tab", "ctrl+e")
	keymap.Input.AcceptSuggestion.SetHelp("tab", "complete")
	keymap.Input.Next.SetKeys("enter")
	return keymap
}

// Let a select field finish its search with Escape as its own help advertises.
// Outside search, Escape cancels the form through Huh's standard quit key.
func formKeyFilter(form *huh.Form) func(tea.Model, tea.Msg) tea.Msg {
	return func(_ tea.Model, message tea.Msg) tea.Msg {
		if pressed, ok := message.(tea.KeyPressMsg); ok && pressed.Code == tea.KeyEscape {
			for _, binding := range form.GetFocusedField().KeyBinds() {
				if key.Matches(pressed, binding) {
					return message
				}
			}
			return tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})
		}
		return message
	}
}
