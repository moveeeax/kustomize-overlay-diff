package kustomize

import (
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) *Report {
	t.Helper()
	tree, err := Load(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if errs := tree.ParseErrors(); len(errs) > 0 {
		t.Fatalf("unexpected parse errors in %s: %v", name, errs)
	}
	return tree.Analyze()
}

func rules(r *Report) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.Rule)
	}
	return out
}

func only(t *testing.T, r *Report, rule string) Finding {
	t.Helper()
	var hits []Finding
	for _, f := range r.Findings {
		if f.Rule == rule {
			hits = append(hits, f)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one %s finding, got %d (%v)", rule, len(hits), rules(r))
	}
	return hits[0]
}

func TestCleanTreeHasNoFindings(t *testing.T) {
	rep := fixture(t, "clean")
	if len(rep.Findings) != 0 {
		t.Fatalf("clean tree should be clean, got %v", rep.Findings)
	}
	if ExitCode(rep.Findings, "warning") != 0 {
		t.Fatal("clean tree must exit 0 even at --fail-on warning")
	}

	var prod *Overlay
	for i := range rep.Overlays {
		if rep.Overlays[i].Dir == filepath.Join("overlays", "prod") {
			prod = &rep.Overlays[i]
		}
	}
	if prod == nil {
		t.Fatalf("no delta reported for overlays/prod, got %d overlays", len(rep.Overlays))
	}
	if len(prod.Chain) != 1 || prod.Chain[0] != "base" {
		t.Fatalf("chain should resolve to base, got %v", prod.Chain)
	}
	if prod.NamePrefix != "prod-" || prod.Namespace != "acme-prod" {
		t.Fatalf("transforms not picked up: %+v", prod)
	}
	if len(prod.Added) != 1 || !strings.Contains(prod.Added[0], "PodDisruptionBudget") {
		t.Fatalf("overlay should add the PDB, got %v", prod.Added)
	}
	// Patch targets are matched against the base render, so the Deployment is
	// found under its pre-prefix name.
	if len(prod.PatchTargets) != 1 || len(prod.PatchTargets[0].Matches) != 1 {
		t.Fatalf("patch should match exactly one resource, got %+v", prod.PatchTargets)
	}
	if prod.Resources != 3 {
		t.Fatalf("prod should render 3 resources, got %d", prod.Resources)
	}
	if len(prod.ReplicaOverrides) != 1 || prod.ReplicaOverrides[0] != "api=4" {
		t.Fatalf("replica override lost: %v", prod.ReplicaOverrides)
	}
}

func TestDeadPatchIsReportedWithPosition(t *testing.T) {
	rep := fixture(t, "deadpatch")
	f := only(t, rep, RuleDeadPatch)
	if f.Severity != SeverityError {
		t.Fatalf("dead-patch must be an error, got %s", f.Severity)
	}
	if f.Line == 0 || !strings.HasSuffix(f.File, "kustomization.yaml") {
		t.Fatalf("finding must point at the patch reference, got %s", f.Location())
	}
	if !strings.Contains(f.Message, "worker") {
		t.Fatalf("message should name the unmatched target, got %q", f.Message)
	}
	if ExitCode(rep.Findings, "error") != 1 {
		t.Fatal("a dead patch must fail --fail-on error")
	}
}

func TestShadowedImageReportsBothValues(t *testing.T) {
	rep := fixture(t, "shadowedimage")
	f := only(t, rep, RuleShadowedImage)
	if f.Severity != SeverityWarning {
		t.Fatalf("shadowed-image must be a warning, got %s", f.Severity)
	}
	if !strings.Contains(f.Message, "1.0.0") || !strings.Contains(f.Message, "2.0.0") {
		t.Fatalf("both image values must appear, got %q", f.Message)
	}
	// Warning only: the default --fail-on error leaves the exit code at 0.
	if ExitCode(rep.Findings, "error") != 0 {
		t.Fatal("a warning must not fail --fail-on error")
	}
	if ExitCode(rep.Findings, "warning") != 1 {
		t.Fatal("a warning must fail --fail-on warning")
	}
}

func TestMissingBaseIsAnError(t *testing.T) {
	rep := fixture(t, "missingbase")
	f := only(t, rep, RuleMissingBase)
	if f.Severity != SeverityError {
		t.Fatalf("missing-base must be an error, got %s", f.Severity)
	}
	if !strings.Contains(f.Message, "base-that-moved") {
		t.Fatalf("message should quote the broken reference, got %q", f.Message)
	}
}

func TestDroppedResourceIsReported(t *testing.T) {
	rep := fixture(t, "dropped")
	f := only(t, rep, RuleDroppedResource)
	if f.Severity != SeverityWarning {
		t.Fatalf("dropped-resource must be a warning, got %s", f.Severity)
	}
	if !strings.Contains(f.Message, "Service/api") {
		t.Fatalf("message should name the dropped resource, got %q", f.Message)
	}
	for _, ov := range rep.Overlays {
		if ov.Dir == filepath.Join("overlays", "edge") && len(ov.Dropped) != 1 {
			t.Fatalf("delta should list the dropped resource, got %v", ov.Dropped)
		}
	}
}

func TestCycleIsReportedOnce(t *testing.T) {
	rep := fixture(t, "cycle")
	f := only(t, rep, RuleCycle)
	if f.Severity != SeverityError {
		t.Fatalf("cycle must be an error, got %s", f.Severity)
	}
	if !strings.Contains(f.Message, "->") {
		t.Fatalf("message should show the cycle path, got %q", f.Message)
	}
}

func TestFilterIgnoresExactlyOneRule(t *testing.T) {
	rep := fixture(t, "deadpatch")
	kept := rep.Filter(SeverityWarning, []string{RuleDeadPatch})
	if len(kept.Findings) != 0 {
		t.Fatalf("--ignore dead-patch should empty this report, got %v", rules(kept))
	}
	if kept.Stats.Errors != 0 || kept.Stats.Findings != 0 {
		t.Fatalf("summary should follow the filter, got %+v", kept.Stats)
	}
	if ExitCode(kept.Findings, "error") != 0 {
		t.Fatal("exit code must reflect the remaining findings")
	}
	if len(kept.Overlays) != len(rep.Overlays) {
		t.Fatal("filtering findings must not drop the delta report")
	}
}

func TestMinSeverityDropsWarnings(t *testing.T) {
	rep := fixture(t, "shadowedimage")
	kept := rep.Filter(SeverityError, nil)
	if len(kept.Findings) != 0 {
		t.Fatalf("--min-severity error should hide the warning, got %v", rules(kept))
	}
}

func TestNameMatchesTreatsPatchesTargetAsRegex(t *testing.T) {
	if !nameMatches("api-.*", "api-web", false) {
		t.Fatal("patches: target name should match as a regex")
	}
	if nameMatches("api", "api-web", false) {
		t.Fatal("regex is anchored, so api must not match api-web")
	}
	if nameMatches("api-.*", "api-web", true) {
		t.Fatal("a target read from a patch document is literal, not a regex")
	}
	if !nameMatches("api", "api", true) {
		t.Fatal("literal match broken")
	}
}

func TestLoadRejectsATreeWithoutKustomizations(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("an empty directory is not a kustomize tree")
	}
}
