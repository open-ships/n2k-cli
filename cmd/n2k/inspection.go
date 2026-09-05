package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/open-ships/n2k/pgn"
)

func summaryOutputFlag() flagSpec {
	return flagSpec{name: "output", valueName: "format", defaultVal: messageOutputJSON, description: "result format", choices: []completionItem{
		{value: "json", description: "JSON for scripts"}, {value: "text", description: "readable summary and tables"},
	}}
}

func findPGNs(query string) ([]*pgn.PgnInfo, error) {
	query = strings.TrimSpace(query)
	if number, err := strconv.ParseUint(query, 0, 32); err == nil {
		if infos := pgn.PgnInfoLookup[uint32(number)]; len(infos) > 0 {
			return infos, nil
		}
		return nil, fmt.Errorf("PGN %d is not in the typed metadata; use n2k pgn list --output text to browse", number)
	}
	var matches []*pgn.PgnInfo
	for _, number := range sortedPGNNumbers() {
		for _, info := range pgn.PgnInfoLookup[number] {
			if query == "list" || (query != "" && strings.Contains(strings.ToLower(info.Description+" "+info.Id), strings.ToLower(query))) {
				matches = append(matches, info)
			}
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no known PGNs match %q; try a number or name such as heading", query)
	}
	return matches, nil
}

func writePGNTable(out io.Writer, infos []*pgn.PgnInfo, listOnly bool) error {
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if listOnly {
		_, _ = fmt.Fprintln(writer, "PGN\tNAME\tSCHEMA")
		for _, info := range infos {
			_, _ = fmt.Fprintf(writer, "%d\t%s\t%s\n", info.PGN, singleLine(info.Description), schemaConfidence(info))
		}
		_, _ = fmt.Fprintln(writer, "\nInspect fields with: n2k pgn <number> --output text")
		return writer.Flush()
	}
	for _, info := range infos {
		_, _ = fmt.Fprintf(writer, "PGN %d · %s\nSchema: %s · Transport: %s\n\n", info.PGN, singleLine(info.Description), schemaConfidence(info), info.Type)
		_, _ = fmt.Fprintln(writer, "FIELD\tUNITS\tRANGE / VALUES")
		orders := make([]int, 0, len(info.Fields))
		for order := range info.Fields {
			orders = append(orders, order)
		}
		sort.Ints(orders)
		for _, order := range orders {
			field := info.Fields[order]
			values := lookupName(field)
			if field.RangeMin != nil && field.RangeMax != nil {
				values = fmt.Sprintf("%g to %g", *field.RangeMin, *field.RangeMax)
			}
			if field.Description != "" {
				values += " " + singleLine(field.Description)
			}
			_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\n", singleLine(field.Name), field.Unit, strings.TrimSpace(values))
		}
	}
	return writer.Flush()
}

func schemaConfidence(info *pgn.PgnInfo) string {
	if info.Fallback {
		return "fallback"
	}
	if info.Complete {
		return "complete"
	}
	return "partial"
}

func writeDevices(out io.Writer, devices []deviceRecord, format string) error {
	if format != messageOutputText {
		return encodeDevices(out, devices)
	}
	if len(devices) == 0 {
		_, err := fmt.Fprintln(out, "No devices observed. Try a longer discovery window or a different source.")
		return err
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "ADDRESS\tMANUFACTURER\tMODEL\tLAST SEEN")
	for _, device := range devices {
		manufacturer, model := "Unknown", "Not observed"
		if device.Name != nil {
			manufacturer = pgn.ManufacturerCodeConst(device.Name.ManufacturerCode).String()
		}
		if device.ProductInfo != nil && device.ProductInfo.ModelId != "" {
			model = device.ProductInfo.ModelId
		}
		_, _ = fmt.Fprintf(writer, "%d\t%s\t%s\t%s\n", device.Address, singleLine(manufacturer), singleLine(model), device.LastSeen.Format("2006-01-02 15:04:05 MST"))
	}
	return writer.Flush()
}

func writeValidation(out io.Writer, summary validationSummary, format string) error {
	if format != messageOutputText {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(summary)
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(writer, "%d messages · %d decoded · %d undecodable\n", summary.Messages, summary.Typed, summary.Undecodable)
	if summary.Undecodable > 0 {
		_, _ = fmt.Fprintln(writer, "\nPGN\tUNDECODABLE\tINSPECT")
		numbers := make([]uint32, 0, len(summary.UndecodableByPGN))
		for number := range summary.UndecodableByPGN {
			numbers = append(numbers, number)
		}
		sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
		for _, number := range numbers {
			_, _ = fmt.Fprintf(writer, "%d\t%d\tn2k sniff --file <capture> --unknown --filter 'pgn == %d'\n", number, summary.UndecodableByPGN[number], number)
		}
	}
	return writer.Flush()
}

func singleLine(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, value)
}
