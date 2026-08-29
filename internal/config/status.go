package config

import (
	"fmt"
	"io"
)

func WriteStatus(out io.Writer, report Report) {
	fmt.Fprintln(out, "CONFIG")
	fmt.Fprintf(out, "%s · %s", report.Snapshot.Branch, report.Snapshot.Commit)
	if report.Snapshot.Dirty == 0 {
		fmt.Fprintln(out, " · clean")
	} else {
		fmt.Fprintf(out, " · %s\n", FormatCount(report.Snapshot.Dirty, "changed file", "changed files"))
	}
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
