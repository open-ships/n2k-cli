package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/open-ships/n2k"
	"github.com/open-ships/n2k/pgn"
)

type sourceFlagValues struct {
	iface  string
	usb    string
	file   string
	tcp    string
	udp    string
	format string
	timing bool
}

const maxExpandedCaptureBytes int64 = 16 << 30

func (values sourceFlagValues) option() (n2k.Option, error) {
	return sourceOption(values.iface, values.usb, values.file, values.tcp, values.udp, values.format, values.timing)
}

func (values *sourceFlagValues) prepareCapture(ctx context.Context) (func(), error) {
	if values.file == "" {
		return func() {}, nil
	}
	path, cleanup, err := prepareCapture(ctx, values.file)
	if err != nil {
		return func() {}, err
	}
	values.file = path
	return cleanup, nil
}

// prepareCapture transparently expands gzip captures to a private temporary
// file. n2k.File owns candump parsing, so keeping decompression at this
// boundary avoids duplicating the library's parser and preserves all file
// source behavior.
func prepareCapture(ctx context.Context, path string) (string, func(), error) {
	path = expandPath(path)
	file, err := os.Open(path) // #nosec G304 -- the CLI reads the operator-selected capture path.
	if err != nil {
		return "", func() {}, fmt.Errorf("opening capture %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	buffered := bufio.NewReader(file)
	header, peekErr := buffered.Peek(2)
	if peekErr != nil && !errors.Is(peekErr, io.EOF) {
		return "", func() {}, fmt.Errorf("inspecting capture %q: %w", path, peekErr)
	}
	if len(header) < 2 || header[0] != 0x1f || header[1] != 0x8b {
		return path, func() {}, validateCapture(ctx, path, path)
	}

	compressed, err := gzip.NewReader(buffered)
	if err != nil {
		return "", func() {}, fmt.Errorf("opening gzip capture %q: %w", path, err)
	}
	defer func() { _ = compressed.Close() }()

	expanded, err := os.CreateTemp("", "n2k-capture-*.log")
	if err != nil {
		return "", func() {}, fmt.Errorf("creating temporary capture: %w", err)
	}
	expandedPath := expanded.Name()
	removeExpanded := func() { _ = os.Remove(expandedPath) }
	limited := io.LimitReader(compressed, maxExpandedCaptureBytes+1)
	written, err := io.Copy(expanded, contextReader{ctx: ctx, reader: limited})
	if err != nil {
		_ = expanded.Close()
		removeExpanded()
		return "", func() {}, fmt.Errorf("decompressing capture %q: %w", path, err)
	}
	if written > maxExpandedCaptureBytes {
		_ = expanded.Close()
		removeExpanded()
		return "", func() {}, fmt.Errorf("decompressing capture %q: expanded data exceeds 16 GiB", path)
	}
	if err := expanded.Close(); err != nil {
		removeExpanded()
		return "", func() {}, fmt.Errorf("closing temporary capture: %w", err)
	}
	if err := compressed.Close(); err != nil {
		removeExpanded()
		return "", func() {}, fmt.Errorf("finishing gzip capture %q: %w", path, err)
	}
	if err := validateCapture(ctx, expandedPath, path); err != nil {
		removeExpanded()
		return "", func() {}, err
	}
	return expandedPath, removeExpanded, nil
}

func runSniff(ctx context.Context, out io.Writer, source n2k.Option, expression string, includeUnknown bool, outputFormat string) error {
	opts := []n2k.Option{source}
	if expression != "" {
		opts = append(opts, n2k.Filter(expression))
	}
	if includeUnknown {
		opts = append(opts, n2k.IncludeUnknown())
	}

	writer, err := newMessageWriter(out, outputFormat)
	if err != nil {
		return err
	}
	for message, receiveErr := range n2k.Receive(ctx, opts...) {
		if receiveErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return receiveErr
		}
		if err := writer.Write(message); err != nil {
			return err
		}
		countProgress(ctx)
	}
	return nil
}

func runRecord(ctx context.Context, stdout io.Writer, source n2k.Option, inputPath, outputPath, outputFormat string, overwrite bool) (resultErr error) {
	if outputFormat != "candump" && outputFormat != "jsonl" {
		return fmt.Errorf("unknown output format %q: use candump or jsonl", outputFormat)
	}

	writer, closeWriter, err := outputWriter(inputPath, outputPath, stdout, overwrite)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, closeWriter()) }()
	buffered := bufio.NewWriter(writer)
	defer func() { resultErr = errors.Join(resultErr, buffered.Flush()) }()
	encoder := json.NewEncoder(buffered)
	for observation, observeErr := range n2k.Observe(ctx, source) {
		if observeErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return observeErr
		}
		if outputFormat == "jsonl" {
			if err := encoder.Encode(observation); err != nil {
				return fmt.Errorf("encoding observation: %w", err)
			}
			countProgress(ctx)
			continue
		}
		if observation.Frame == nil {
			continue
		}
		if _, err := fmt.Fprintln(buffered, formatCandump(observation)); err != nil {
			return fmt.Errorf("writing capture: %w", err)
		}
		countProgress(ctx)
	}
	return nil // Deferred flush and close report errors, including after cancellation.
}

func outputWriter(inputPath, path string, stdout io.Writer, overwrite bool) (io.Writer, func() error, error) {
	if path == "" || path == "-" {
		return stdout, func() error { return nil }, nil
	}
	path = expandPath(path)
	flags := os.O_CREATE | os.O_WRONLY | os.O_EXCL
	if overwrite {
		flags = os.O_CREATE | os.O_WRONLY
	}
	file, err := os.OpenFile(path, flags, 0o600) // #nosec G304 -- the CLI writes the operator-selected capture path.
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, nil, fmt.Errorf("output %q already exists; choose a new path or use --overwrite to replace it", path)
		}
		return nil, nil, fmt.Errorf("opening output: %w", err)
	}
	if err := checkOutputFile(inputPath, file); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if overwrite {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return nil, nil, fmt.Errorf("replacing output: %w", err)
		}
	}
	return file, file.Close, nil
}

func formatCandump(observation n2k.Observation) string {
	timestamp := observation.Timestamp
	if timestamp.IsZero() {
		timestamp = observation.ReceivedAt
	}
	network := observation.NetworkID
	if network == "" || strings.ContainsAny(network, " \t") {
		network = "n2k"
	}
	frame := observation.Frame
	data := strings.ToUpper(hex.EncodeToString(frame.Data[:frame.Length]))
	return fmt.Sprintf("(%d.%06d) %s %08X#%s", timestamp.Unix(), timestamp.Nanosecond()/1000, network, frame.ID, data)
}

func runReplay(ctx context.Context, out io.Writer, file string, timing bool, expression string, includeUnknown bool, outputFormat string) error {
	var fileOpts []n2k.FileOption
	if timing {
		fileOpts = append(fileOpts, n2k.OriginalTiming())
	}
	opts := []n2k.Option{n2k.File(file, fileOpts...)}
	if expression != "" {
		opts = append(opts, n2k.Filter(expression))
	}
	if includeUnknown {
		opts = append(opts, n2k.IncludeUnknown())
	}
	writer, err := newMessageWriter(out, outputFormat)
	if err != nil {
		return err
	}
	for message, receiveErr := range n2k.Receive(ctx, opts...) {
		if receiveErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return receiveErr
		}
		if err := writer.Write(message); err != nil {
			return err
		}
		countProgress(ctx)
	}
	return nil
}

type validationSummary struct {
	Messages         int            `json:"messages"`
	Typed            int            `json:"typed"`
	Undecodable      int            `json:"undecodable"`
	ByPGN            map[uint32]int `json:"byPgn"`
	UndecodableByPGN map[uint32]int `json:"undecodableByPgn"`
}

func runValidate(ctx context.Context, out io.Writer, source n2k.Option, strict bool, outputFormat string) error {
	summary := validationSummary{ByPGN: make(map[uint32]int), UndecodableByPGN: make(map[uint32]int)}
	for message, receiveErr := range n2k.Receive(ctx, source, n2k.IncludeUnknown()) {
		if receiveErr != nil {
			if ctx.Err() != nil {
				break
			}
			return receiveErr
		}
		summary.Messages++
		countProgress(ctx)
		if unknown, ok := message.(*pgn.UnknownPGN); ok {
			summary.Undecodable++
			summary.UndecodableByPGN[unknown.Info.PGN]++
			summary.ByPGN[unknown.Info.PGN]++
			continue
		}
		summary.Typed++
		if carrier, ok := message.(interface{ MessageInfo() pgn.MessageInfo }); ok {
			summary.ByPGN[carrier.MessageInfo().PGN]++
		}
	}
	if err := writeValidation(out, summary, outputFormat); err != nil {
		return err
	}
	if strict && summary.Undecodable > 0 {
		return fmt.Errorf("%d undecodable messages; see undecodableByPgn for the failing PGNs", summary.Undecodable)
	}
	return nil
}

type deviceRecord struct {
	Address     uint8                         `json:"address"`
	LastSeen    time.Time                     `json:"lastSeen"`
	RawName     *uint64                       `json:"rawName,omitempty"`
	Name        *n2k.DeviceName               `json:"name,omitempty"`
	ProductInfo *pgn.ProductInformation       `json:"productInfo,omitempty"`
	ConfigInfo  *pgn.ConfigurationInformation `json:"configInfo,omitempty"`
}

func runActiveDevices(ctx context.Context, out io.Writer, source n2k.Option, reconnect bool, wait, claimTimeout time.Duration, outputFormat string) error {
	opts := []n2k.Option{source, n2k.WithClaimTimeout(claimTimeout)}
	if reconnect {
		opts = append(opts, n2k.WithReconnect(n2k.ReconnectPolicy{}))
	}
	client, err := n2k.NewClient(ctx, opts...)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
	if err := client.Err(); err != nil {
		return err
	}
	devices := client.Devices()
	records := make([]deviceRecord, 0, len(devices))
	for _, device := range devices {
		rawName := device.RawName
		name := device.Name
		records = append(records, deviceRecord{
			Address:     device.Address,
			LastSeen:    device.LastSeen,
			RawName:     &rawName,
			Name:        &name,
			ProductInfo: device.ProductInfo,
			ConfigInfo:  device.ConfigInfo,
		})
	}
	setProgress(ctx, len(records))
	return writeDevices(out, records, outputFormat)
}

func runPassiveDevices(ctx context.Context, out io.Writer, source n2k.Option, wait time.Duration, bounded bool, outputFormat string) error {
	scanCtx := ctx
	cancel := func() {}
	if bounded {
		scanCtx, cancel = context.WithTimeout(ctx, wait)
	}
	defer cancel()

	inventory := newDeviceInventory()
	for message, receiveErr := range n2k.Receive(scanCtx, source, n2k.IncludeUnknown()) {
		if receiveErr != nil {
			if scanCtx.Err() != nil {
				break
			}
			return receiveErr
		}
		inventory.observe(message)
	}
	records := inventory.snapshot()
	setProgress(ctx, len(records))
	return writeDevices(out, records, outputFormat)
}

type deviceInventory struct {
	byAddress map[uint8]*deviceRecord
	byName    map[uint64]*deviceRecord
}

func newDeviceInventory() *deviceInventory {
	return &deviceInventory{
		byAddress: make(map[uint8]*deviceRecord),
		byName:    make(map[uint64]*deviceRecord),
	}
}

func (inventory *deviceInventory) observe(message pgn.Message) {
	carrier, ok := message.(interface{ MessageInfo() pgn.MessageInfo })
	if !ok {
		return
	}
	info := carrier.MessageInfo()
	if info.SourceId >= 254 {
		return
	}
	record := inventory.byAddress[info.SourceId]
	if record == nil {
		record = &deviceRecord{Address: info.SourceId}
		inventory.byAddress[info.SourceId] = record
	}

	if claim, ok := message.(*pgn.IsoAddressClaim); ok {
		if rawName, name, valid := claimedDeviceName(claim); valid {
			if prior := inventory.byName[rawName]; prior != nil && prior != record {
				delete(inventory.byAddress, prior.Address)
				mergeDeviceRecords(record, prior)
			}
			if displaced := record.RawName; displaced != nil && *displaced != rawName {
				delete(inventory.byName, *displaced)
			}
			record.RawName = uint64Pointer(rawName)
			record.Name = &name
			inventory.byName[rawName] = record
		}
	}
	switch typed := message.(type) {
	case *pgn.ProductInformation:
		record.ProductInfo = typed
	case *pgn.ConfigurationInformation:
		record.ConfigInfo = typed
	}

	seen := info.Timestamp
	if seen.IsZero() {
		seen = info.ReceivedAt
	}
	if seen.After(record.LastSeen) {
		record.LastSeen = seen
	}
}

func claimedDeviceName(claim *pgn.IsoAddressClaim) (uint64, n2k.DeviceName, bool) {
	fields := []*uint64{
		claim.UniqueNumber,
		claim.ManufacturerCode,
		claim.DeviceInstanceLower,
		claim.DeviceInstanceUpper,
		claim.DeviceFunction,
		claim.DeviceClass,
		claim.SystemInstance,
		claim.IndustryGroup,
		claim.ArbitraryAddressCapable,
	}
	for _, field := range fields {
		if field == nil {
			return 0, n2k.DeviceName{}, false
		}
	}
	name := n2k.DeviceName{
		IdentityNumber:   uint32(*claim.UniqueNumber),
		ManufacturerCode: uint16(*claim.ManufacturerCode),
		DeviceInstance:   uint8(*claim.DeviceInstanceLower) | uint8(*claim.DeviceInstanceUpper)<<3,
		DeviceFunction:   uint8(*claim.DeviceFunction),
		DeviceClass:      uint8(*claim.DeviceClass),
		SystemInstance:   uint8(*claim.SystemInstance),
		IndustryGroup:    uint8(*claim.IndustryGroup),
	}
	rawName := name.Pack(*claim.ArbitraryAddressCapable == 1)
	return rawName, name, true
}

func mergeDeviceRecords(destination, source *deviceRecord) {
	if destination.ProductInfo == nil {
		destination.ProductInfo = source.ProductInfo
	}
	if destination.ConfigInfo == nil {
		destination.ConfigInfo = source.ConfigInfo
	}
	if source.LastSeen.After(destination.LastSeen) {
		destination.LastSeen = source.LastSeen
	}
}

func uint64Pointer(value uint64) *uint64 {
	return &value
}

func (inventory *deviceInventory) snapshot() []deviceRecord {
	records := make([]deviceRecord, 0, len(inventory.byAddress))
	for _, record := range inventory.byAddress {
		records = append(records, *record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Address < records[j].Address })
	return records
}

func encodeDevices(out io.Writer, devices []deviceRecord) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(devices); err != nil {
		return fmt.Errorf("encoding devices: %w", err)
	}
	return nil
}

func sortedPGNNumbers() []uint32 {
	numbers := make([]uint32, 0, len(pgn.PgnInfoLookup))
	for number := range pgn.PgnInfoLookup {
		numbers = append(numbers, number)
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
	return numbers
}

func runPGN(out io.Writer, value, outputFormat string) error {
	infos, err := findPGNs(value)
	if err != nil {
		return err
	}
	if outputFormat == messageOutputText {
		_, numericErr := strconv.ParseUint(value, 0, 32)
		return writePGNTable(out, infos, value == "list" || (numericErr != nil && len(infos) > 1))
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if value == "list" {
		for _, info := range infos {
			if err := encoder.Encode(info); err != nil {
				return err
			}
		}
		return nil
	}
	return encoder.Encode(infos)
}

// sourceOption converts mutually exclusive source flags into the one n2k
// source Option they select.
func sourceOption(iface, usb, file, tcp, udp, format string, timing bool) (n2k.Option, error) {
	stream, err := streamFormat(format)
	if err != nil {
		return nil, err
	}
	var sources []n2k.Option
	if iface != "" {
		sources = append(sources, n2k.CAN(iface))
	}
	if usb != "" {
		sources = append(sources, n2k.USB(usb))
	}
	if file != "" {
		var fileOpts []n2k.FileOption
		if timing {
			fileOpts = append(fileOpts, n2k.OriginalTiming())
		}
		sources = append(sources, n2k.File(file, fileOpts...))
	}
	if tcp != "" {
		if err := addressValidator("TCP")(tcp); err != nil {
			return nil, err
		}
		sources = append(sources, n2k.TCP(tcp, stream))
	}
	if udp != "" {
		if err := addressValidator("UDP")(udp); err != nil {
			return nil, err
		}
		sources = append(sources, n2k.UDP(udp, stream))
	}
	if len(sources) != 1 {
		return nil, errors.New("exactly one source is required: --interface, --usb, --file, --tcp, or --udp")
	}
	if timing && file == "" {
		return nil, errors.New("--timing only applies to --file sources")
	}
	return sources[0], nil
}

func streamFormat(name string) (n2k.StreamFormat, error) {
	switch name {
	case "raw":
		return n2k.FormatYDRaw, nil
	case "actisense":
		return n2k.FormatActisense, nil
	default:
		return 0, fmt.Errorf("unknown format %q: use raw or actisense", name)
	}
}
