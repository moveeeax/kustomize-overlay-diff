// Package cli is the command line surface, kept out of main so the exit codes
// are testable.
package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/moveeeax/kustomize-overlay-diff/internal/kustomize"
	"github.com/moveeeax/kustomize-overlay-diff/internal/report"
)

// Version is stamped at release time.
var Version = "0.2.0"

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

// Run returns the process exit code: 0 clean, 1 findings at or above
// --fail-on, 2 a usage or parse error.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("kustomize-overlay-diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "kustomize-overlay-diff audits a kustomize tree: what each overlay\n")
		fmt.Fprintf(stderr, "changes versus its base, and which of it is dead.\n\n")
		fmt.Fprintf(stderr, "usage: kustomize-overlay-diff [flags]\n\n")
		fs.PrintDefaults()
		fmt.Fprintf(stderr, "\nrules: %s\n", strings.Join(kustomize.Rules(), ", "))
	}

	dir := fs.String("dir", ".", "root of the kustomize tree to audit")
	format := fs.String("format", "table", "output format: "+strings.Join(report.Formats(), ", "))
	minSeverity := fs.String("min-severity", "warning", "lowest severity to report: warning or error")
	failOn := fs.String("fail-on", "error", "exit 1 when a finding reaches this severity: warning, error or never")
	var ignore stringList
	fs.Var(&ignore, "ignore", "rule id to suppress (repeatable, comma-separated accepted)")
	version := fs.Bool("version", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *version {
		fmt.Fprintf(stdout, "kustomize-overlay-diff %s\n", Version)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q; the tree is selected with --dir\n", fs.Arg(0))
		return 2
	}

	min, err := kustomize.ParseSeverity(*minSeverity)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	if *failOn != "never" {
		if _, err := kustomize.ParseSeverity(*failOn); err != nil {
			fmt.Fprintf(stderr, "bad --fail-on: %v (or never)\n", err)
			return 2
		}
	}
	if !contains(report.Formats(), *format) {
		fmt.Fprintf(stderr, "unknown --format %q (want %s)\n", *format, strings.Join(report.Formats(), ", "))
		return 2
	}
	for _, rule := range ignore {
		if !kustomize.KnownRule(rule) {
			fmt.Fprintf(stderr, "unknown rule %q passed to --ignore; known rules: %s\n",
				rule, strings.Join(kustomize.Rules(), ", "))
			return 2
		}
	}

	tree, err := kustomize.Load(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	if errs := tree.ParseErrors(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(stderr, "parse error: %v\n", e)
		}
		return 2
	}

	rep := tree.Analyze().Filter(min, ignore)
	if err := report.Write(stdout, rep, *format); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	return kustomize.ExitCode(rep.Findings, *failOn)
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
