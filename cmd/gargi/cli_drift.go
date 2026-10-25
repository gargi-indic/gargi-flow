package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"time"

	"github.com/gargi-indic/gargi-flow/internal/simbackend"
)

func modelDrift(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gargi model drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "URL of gargi server (e.g. http://localhost:8080)")
	policyVersion := fs.String("policy-version", "1", "Policy version to compare against")
	asJSON := fs.Bool("json", false, "Print the report as JSON")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return parseExit(err)
	}
	if len(pos) != 1 || *server == "" {
		printf(stderr, "%s\n", "usage: gargi model drift <model-id> --server <url> [--policy-version <version>] [--json]")
		return exitUsage
	}

	modelID := pos[0]

	remote, err := simbackend.NewRemote(*server, nil)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitUsage
	}

	reportJSON, err := remote.GetDriftReport(context.Background(), modelID, *policyVersion)
	if err != nil {
		printf(stderr, "gargi: %v\n", err)
		return exitError
	}

	if *asJSON {
		var buf bytes.Buffer
		if err := json.Indent(&buf, reportJSON, "", "  "); err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
		buf.WriteByte('\n')
		if _, err := stdout.Write(buf.Bytes()); err != nil {
			printf(stderr, "gargi: %v\n", err)
			return exitError
		}
		return exitOK
	}

	// Print summary
	var report struct {
		PolicyVersion string `json:"policyVersion"`
		DriftDetected bool   `json:"driftDetected"`
		Nodes         map[string]struct {
			ObservedSamples  int  `json:"observedSamples"`
			DriftDetected    bool `json:"driftDetected"`
			FailureRateDrift *struct {
				Predicted float64 `json:"predicted"`
				Observed  float64 `json:"observed"`
				IsDrift   bool    `json:"isDrift"`
			} `json:"failureRateDrift"`
			DurationDrift *struct {
				Predicted float64 `json:"predicted"`
				Observed  float64 `json:"observed"`
				IsDrift   bool    `json:"isDrift"`
			} `json:"durationDrift"`
			OutputDrift map[string]*struct {
				Predicted float64 `json:"predicted"`
				Observed  float64 `json:"observed"`
				IsDrift   bool    `json:"isDrift"`
			} `json:"outputDrift"`
		} `json:"nodes"`
		Latents map[string]*struct {
			Predicted float64 `json:"predicted"`
			Observed  float64 `json:"observed"`
			Delta     float64 `json:"delta"`
			IsDrift   bool    `json:"isDrift"`
		} `json:"latents"`
	}

	if err := json.Unmarshal(reportJSON, &report); err != nil {
		printf(stderr, "gargi: decoding drift report details: %v\n", err)
		return exitError
	}

	statusSummary := "OK"
	if report.DriftDetected {
		statusSummary = "DRIFT DETECTED"
	}

	printf(stdout, "Drift Report for Model: %s (Policy Version: %s) [%s]\n", modelID, report.PolicyVersion, statusSummary)
	if len(report.Nodes) == 0 && len(report.Latents) == 0 {
		printf(stdout, "  No sufficient data for drift detection.\n")
		return exitOK
	}

	for nodeID, details := range report.Nodes {
		driftStr := "OK"
		if details.DriftDetected {
			driftStr = "DRIFT"
		}
		printf(stdout, "\nNode: %s [%s] (%d samples)\n", nodeID, driftStr, details.ObservedSamples)

		if f := details.FailureRateDrift; f != nil {
			fDriftStr := ""
			if f.IsDrift {
				fDriftStr = " (DRIFT)"
			}
			printf(stdout, "  Failure Rate: predicted %.1f%% vs observed %.1f%%%s\n", f.Predicted*100, f.Observed*100, fDriftStr)
		}

		if d := details.DurationDrift; d != nil {
			dDriftStr := ""
			if d.IsDrift {
				dDriftStr = " (DRIFT)"
			}
			predDur := time.Duration(d.Predicted * float64(time.Second))
			obsDur := time.Duration(d.Observed * float64(time.Second))
			printf(stdout, "  Duration: predicted %s vs observed %s%s\n", predDur, obsDur, dDriftStr)
		}

		for outKey, od := range details.OutputDrift {
			odDriftStr := ""
			if od.IsDrift {
				odDriftStr = " (DRIFT)"
			}
			printf(stdout, "  Output %s: predicted %.1f%% vs observed %.1f%%%s\n", outKey, od.Predicted*100, od.Observed*100, odDriftStr)
		}
	}

	if len(report.Latents) > 0 {
		printf(stdout, "\nLatents:\n")
		for latentID, lm := range report.Latents {
			driftStr := "OK"
			if lm.IsDrift {
				driftStr = "DRIFT"
			}
			printf(stdout, "  %s [%s]: predicted %.2f vs observed %.2f (delta %.2f)\n", latentID, driftStr, lm.Predicted, lm.Observed, lm.Delta)
		}
	}

	return exitOK
}
