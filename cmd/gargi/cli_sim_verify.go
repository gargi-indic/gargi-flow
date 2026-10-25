package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"

	"github.com/gargi-indic/gargi-flow/internal/simverify"
)

// simVerify implements the gargi sim verify CLI command.
func simVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi sim verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scenarios := fs.Int("scenarios", 10, "Number of scenarios to verify")
	asJSON := fs.Bool("json", false, "Output verification report as JSON")

	files, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if len(files) != 1 {
		printf(stderr, "%s\n", "usage: gargi sim verify <experiment.yaml> [--scenarios N] [--json]")
		return exitUsage
	}
	if *scenarios <= 0 {
		printf(stderr, "gargi: --scenarios must be greater than 0\n")
		return exitUsage
	}

	report, err := simverify.VerifyFile(context.Background(), files[0], simverify.VerifyOptions{
		Scenarios: *scenarios,
	})
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}

	if *asJSON {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
		if _, err := stdout.Write(buf.Bytes()); err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
	} else {
		pct := 0.0
		if report.Scenarios > 0 {
			pct = float64(report.Matched) / float64(report.Scenarios) * 100.0
		}
		printf(stdout, "Temporal Fidelity Check: %d scenarios\n", report.Scenarios)
		printf(stdout, "Matched: %d/%d (%.1f%%)\n", report.Matched, report.Scenarios, pct)
		if len(report.Mismatches) > 0 {
			printf(stdout, "Mismatches (%d):\n", len(report.Mismatches))
			for _, m := range report.Mismatches {
				printf(stdout, "  [Scenario %d, World %s] %s: %s\n", m.Scenario, m.World, m.Type, m.Details)
			}
		} else {
			printf(stdout, "All scenarios matched with 100%% fidelity.\n")
		}
	}

	if len(report.Mismatches) > 0 {
		return exitError
	}
	return exitOK
}
