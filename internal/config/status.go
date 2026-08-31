package config

import (
	"fmt"
	"io"
	"strings"
)

func WriteStatus(out io.Writer, report Report) {
	fmt.Fprintln(out, "CONFIG")
	// PendingParts is the one derivation of what a snapshot still owes. This
	// header used to name only changed files, so a checkout holding unpushed
	// commits read as clean here while the terminal called it unpushed.
	headline := "clean"
	if pending := report.Snapshot.PendingParts(); len(pending) > 0 {
		headline = strings.Join(pending, " · ")
	}
	fmt.Fprintf(out, "%s · %s · %s\n", report.Snapshot.Branch, report.Snapshot.Commit, headline)
	for _, resource := range report.Resources {
		fmt.Fprintf(out, "  %s %-20s %s\n", resource.Symbol(), resource.Name, resource.Summary)
	}
	snapshotSymbol := GlyphOK
	if report.Snapshot.Warnings() > 0 {
		snapshotSymbol = GlyphWarn
	}
	fmt.Fprintf(out, "  %s %-20s %s\n", snapshotSymbol, "Snapshot", report.Snapshot.Summary())
	failures, decisions, advisories := report.Counts()
	warnings := advisories + report.Snapshot.Warnings()
	fmt.Fprintf(out, "\n%s · %s · %s\n",
		FormatCount(failures, "failure", "failures"),
		FormatCount(decisions, "decision", "decisions"),
		FormatCount(warnings, "warning", "warnings"))
}
