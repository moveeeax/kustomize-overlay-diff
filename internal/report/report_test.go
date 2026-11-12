package report

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moveeeax/kustomize-overlay-diff/internal/kustomize"
)

func TestGitHubMapsSeverityAndPosition(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo", "k8s")
	rep := &kustomize.Report{Root: root, Findings: []kustomize.Finding{
		{Rule: "dead-patch", Severity: kustomize.SeverityError, File: filepath.Join("overlays", "prod", "kustomization.yaml"), Line: 15, Message: "patch matches nothing"},
		{Rule: "shadowed-image", Severity: kustomize.SeverityWarning, File: "base/kustomization.yaml", Message: "image overridden twice"},
	}}
	var out bytes.Buffer
	if err := GitHub(&out, rep, filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	want := "::error file=k8s/overlays/prod/kustomization.yaml,line=15,title=dead-patch::patch matches nothing\n" +
		"::warning file=k8s/base/kustomization.yaml,title=shadowed-image::image overridden twice\n"
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestGitHubEscapesDataAndProperties(t *testing.T) {
	rep := &kustomize.Report{Root: "/repo", Findings: []kustomize.Finding{
		{Rule: "cycle", Severity: kustomize.SeverityError, File: "a,b:c/kustomization.yaml", Line: 1, Message: "100% looped\nback"},
	}}
	var out bytes.Buffer
	if err := GitHub(&out, rep, "/repo"); err != nil {
		t.Fatal(err)
	}
	want := "::error file=a%2Cb%3Ac/kustomization.yaml,line=1,title=cycle::100%25 looped%0Aback\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestGitHubKeepsAbsolutePathOutsideBase(t *testing.T) {
	rep := &kustomize.Report{Root: "/elsewhere/k8s", Findings: []kustomize.Finding{
		{Rule: "missing-base", Severity: kustomize.SeverityError, File: "kustomization.yaml", Line: 3, Message: "x"},
	}}
	var out bytes.Buffer
	if err := GitHub(&out, rep, "/repo"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "::error file=/elsewhere/k8s/kustomization.yaml,line=3,") {
		t.Fatalf("a file outside the workspace should keep its absolute path: %q", out.String())
	}
}

func TestGitHubPrintsNothingWithoutFindings(t *testing.T) {
	var out bytes.Buffer
	if err := GitHub(&out, &kustomize.Report{Root: "/repo"}, "/repo"); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("no findings should print no commands, got %q", out.String())
	}
}
