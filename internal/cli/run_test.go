package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func tree(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

func TestCleanTreeExitsZero(t *testing.T) {
	code, out, errOut := run(t, "--dir", tree("clean"))
	if code != 0 {
		t.Fatalf("want exit 0, got %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "no findings") {
		t.Fatalf("clean tree should say so:\n%s", out)
	}
	if !strings.Contains(out, "namePrefix=prod-") {
		t.Fatalf("delta report should show the transforms:\n%s", out)
	}
}

func TestFindingsExitOne(t *testing.T) {
	code, out, _ := run(t, "--dir", tree("deadpatch"))
	if code != 1 {
		t.Fatalf("want exit 1 on a dead patch, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "dead-patch") {
		t.Fatalf("rule id should be in the table:\n%s", out)
	}
}

func TestIgnoreSuppressesTheRuleAndTheExitCode(t *testing.T) {
	code, out, _ := run(t, "--dir", tree("deadpatch"), "--ignore", "dead-patch")
	if code != 0 {
		t.Fatalf("want exit 0 once the only rule is ignored, got %d\n%s", code, out)
	}
	if strings.Contains(out, "dead-patch") {
		t.Fatalf("ignored rule should not be printed:\n%s", out)
	}
}

func TestFailOnWarningAndNever(t *testing.T) {
	if code, out, _ := run(t, "--dir", tree("shadowedimage"), "--fail-on", "warning"); code != 1 {
		t.Fatalf("want exit 1 at --fail-on warning, got %d\n%s", code, out)
	}
	if code, out, _ := run(t, "--dir", tree("deadpatch"), "--fail-on", "never"); code != 0 {
		t.Fatalf("--fail-on never must still exit 0, got %d\n%s", code, out)
	}
}

func TestMinSeverityError(t *testing.T) {
	code, out, _ := run(t, "--dir", tree("shadowedimage"), "--min-severity", "error")
	if code != 0 {
		t.Fatalf("want exit 0, got %d\n%s", code, out)
	}
	if strings.Contains(out, "shadowed-image") {
		t.Fatalf("warning should be filtered out:\n%s", out)
	}
}

func TestJSONOutputIsValidAndCarriesTheSummary(t *testing.T) {
	code, out, _ := run(t, "--dir", tree("dropped"), "--format", "json", "--fail-on", "warning")
	if code != 1 {
		t.Fatalf("want exit 1, got %d\n%s", code, out)
	}
	var rep struct {
		Root     string `json:"root"`
		Overlays []struct {
			Dir     string   `json:"dir"`
			Dropped []string `json:"resourcesDropped"`
		} `json:"overlays"`
		Findings []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
			Line     int    `json:"line"`
		} `json:"findings"`
		Summary struct {
			Warnings int `json:"warnings"`
			Findings int `json:"findings"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if rep.Summary.Findings != 1 || rep.Summary.Warnings != 1 {
		t.Fatalf("summary wrong: %+v", rep.Summary)
	}
	if rep.Findings[0].Rule != "dropped-resource" || rep.Findings[0].Line == 0 {
		t.Fatalf("finding lost its rule or position: %+v", rep.Findings[0])
	}
}

func TestMarkdownOutputHasATable(t *testing.T) {
	_, out, _ := run(t, "--dir", tree("deadpatch"), "--format", "markdown")
	if !strings.Contains(out, "| severity | rule | location | message |") {
		t.Fatalf("markdown findings table missing:\n%s", out)
	}
	if !strings.Contains(out, "### `overlays/dev`") {
		t.Fatalf("markdown should have a section per overlay:\n%s", out)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	cases := [][]string{
		{"--dir", tree("clean"), "--format", "yaml"},
		{"--dir", tree("clean"), "--min-severity", "info"},
		{"--dir", tree("clean"), "--fail-on", "sometimes"},
		{"--dir", tree("clean"), "--ignore", "no-such-rule"},
		{"--dir", filepath.Join(tree("clean"), "nope")},
		{"--dir", tree("clean"), "stray-arg"},
	}
	for _, args := range cases {
		if code, _, errOut := run(t, args...); code != 2 {
			t.Fatalf("args %v: want exit 2, got %d (stderr %q)", args, code, errOut)
		}
	}
}

func TestParseErrorExitsTwo(t *testing.T) {
	dir := t.TempDir()
	broken := "apiVersion: kustomize.config.k8s.io/v1beta1\nresources:\n  - [unclosed\n"
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := run(t, "--dir", dir)
	if code != 2 {
		t.Fatalf("want exit 2 on unparsable yaml, got %d", code)
	}
	if !strings.Contains(errOut, "parse error") && errOut == "" {
		t.Fatalf("the reason should reach stderr, got %q", errOut)
	}
}

func TestVersionFlag(t *testing.T) {
	code, out, _ := run(t, "--version")
	if code != 0 || !strings.Contains(out, Version) {
		t.Fatalf("version flag broken: %d %q", code, out)
	}
}
