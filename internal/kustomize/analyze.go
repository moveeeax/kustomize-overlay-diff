package kustomize

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PatchTarget is one patch of an overlay and the resources it actually hits.
type PatchTarget struct {
	Origin  string   `json:"origin"`
	Path    string   `json:"path,omitempty"`
	Target  string   `json:"target"`
	Matches []string `json:"matches,omitempty"`
}

type ImageDelta struct {
	Name string `json:"name"`
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

// Overlay is the readable answer to "what does this kustomization do to its
// base", which is the whole point of the tool.
type Overlay struct {
	Dir              string        `json:"dir"`
	Chain            []string      `json:"chain,omitempty"`
	NamePrefix       string        `json:"namePrefix,omitempty"`
	NameSuffix       string        `json:"nameSuffix,omitempty"`
	Namespace        string        `json:"namespace,omitempty"`
	Added            []string      `json:"resourcesAdded,omitempty"`
	Dropped          []string      `json:"resourcesDropped,omitempty"`
	PatchTargets     []PatchTarget `json:"patchTargets,omitempty"`
	ImageOverrides   []ImageDelta  `json:"imageOverrides,omitempty"`
	ReplicaOverrides []string      `json:"replicaOverrides,omitempty"`
	Resources        int           `json:"resourceCount"`
}

type Summary struct {
	Overlays int `json:"overlays"`
	Findings int `json:"findings"`
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

type Report struct {
	Root     string    `json:"root"`
	Overlays []Overlay `json:"overlays"`
	Findings []Finding `json:"findings"`
	Stats    Summary   `json:"summary"`
}

// Analyze resolves every kustomization under the root and runs the rules.
func (t *Tree) Analyze() *Report {
	dirs := make([]string, 0, len(t.nodes))
	for d := range t.nodes {
		if t.local[d] {
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)

	for _, d := range dirs {
		t.resolve(d, nil)
	}

	rep := &Report{Root: t.Root}
	findings := append([]Finding{}, t.cycleFindings...)
	for _, d := range dirs {
		ov, fs := t.analyzeNode(t.nodes[d])
		rep.Overlays = append(rep.Overlays, ov)
		findings = append(findings, fs...)
	}
	sortFindings(findings)
	rep.Findings = findings
	rep.Stats = stats(rep)
	return rep
}

func (t *Tree) analyzeNode(n *Node) (Overlay, []Finding) {
	ov := Overlay{
		Dir:        t.rel(n.Dir),
		NamePrefix: n.NamePrefix,
		NameSuffix: n.NameSuffix,
		Namespace:  n.Namespace,
		Resources:  len(t.rendered[n.Dir]),
	}
	for _, c := range t.chain(n.Dir) {
		ov.Chain = append(ov.Chain, t.rel(c))
	}
	for _, r := range n.FileResources {
		ov.Added = append(ov.Added, r.ID.String())
	}

	var out []Finding
	file := t.rel(n.File)

	for _, r := range n.MissingRefs {
		out = append(out, Finding{
			Rule:     RuleMissingBase,
			Severity: SeverityError,
			Overlay:  ov.Dir,
			File:     file,
			Line:     r.Line,
			Message:  fmt.Sprintf("resources entry %q cannot be resolved: %s", r.Raw, r.Reason),
		})
	}

	acc := t.accum[n.Dir]
	for _, p := range n.Patches {
		pt := PatchTarget{Origin: p.Origin, Path: p.Path, Target: p.Target.String()}
		if p.FileMissing {
			ov.PatchTargets = append(ov.PatchTargets, pt)
			out = append(out, Finding{
				Rule:     RuleDeadPatch,
				Severity: SeverityError,
				Overlay:  ov.Dir,
				File:     file,
				Line:     p.Line,
				Message:  fmt.Sprintf("patch %q is listed but the file is not on disk, so it patches nothing", p.Path),
			})
			continue
		}
		matched := matchResources(acc, p.Target, p.Exact)
		for _, m := range matched {
			pt.Matches = append(pt.Matches, m.ID.String())
		}
		ov.PatchTargets = append(ov.PatchTargets, pt)

		// A label selector cannot be evaluated without rendering, and an
		// empty target means kustomize derives it elsewhere. Neither is a
		// claim this tool can make, so neither is reported.
		if p.Target.Selector != "" || p.Target.Empty() {
			continue
		}
		if len(matched) == 0 {
			what := p.Path
			if what == "" {
				what = "(inline)"
			}
			out = append(out, Finding{
				Rule:     RuleDeadPatch,
				Severity: SeverityError,
				Overlay:  ov.Dir,
				File:     file,
				Line:     p.Line,
				Message: fmt.Sprintf("patch %s targets %s, which matches no resource in the chain", what,
					p.Target.String()),
			})
		}
	}

	for _, img := range n.Images {
		d := ImageDelta{Name: img.Name, To: img.Value()}
		if anc, prev, ok := t.ancestorImage(n.Dir, img.Name); ok {
			d.From = prev.Value()
			out = append(out, Finding{
				Rule:     RuleShadowedImage,
				Severity: SeverityWarning,
				Overlay:  ov.Dir,
				File:     file,
				Line:     img.Line,
				Message: fmt.Sprintf("image %s is overridden to %s here and to %s in %s; the overlay wins and the base override is dead",
					img.Name, img.Value(), prev.Value(), t.rel(anc.File)),
			})
		}
		ov.ImageOverrides = append(ov.ImageOverrides, d)
	}

	for _, r := range n.Replicas {
		ov.ReplicaOverrides = append(ov.ReplicaOverrides, fmt.Sprintf("%s=%d", r.Name, r.Count))
	}

	dropped, df := t.droppedResources(n, ov.Dir, file)
	ov.Dropped = dropped
	out = append(out, df...)

	return ov, out
}

// ancestorImage finds the nearest base that already overrides the same image.
func (t *Tree) ancestorImage(dir, name string) (*Node, Image, bool) {
	chain := t.chain(dir)
	for i := len(chain) - 1; i >= 0; i-- {
		n := t.nodes[chain[i]]
		if n == nil {
			continue
		}
		for _, img := range n.Images {
			if img.Name == name {
				return n, img, true
			}
		}
	}
	return nil, Image{}, false
}

// droppedResources catches the overlay that reaches into a kustomized base and
// picks files out of it one by one instead of inheriting the directory. Every
// resource the base declares and the overlay skipped is gone from the render,
// with nothing in the diff to say so.
func (t *Tree) droppedResources(n *Node, ovDir, file string) ([]string, []Finding) {
	refByDir := map[string]map[string]bool{}
	lineByDir := map[string]int{}
	for _, r := range n.FileRefs {
		d := filepath.Dir(r.Path)
		if d == n.Dir {
			continue
		}
		if refByDir[d] == nil {
			refByDir[d] = map[string]bool{}
			lineByDir[d] = r.Line
		}
		refByDir[d][r.Path] = true
	}
	if len(refByDir) == 0 {
		return nil, nil
	}

	present := map[string]bool{}
	for _, r := range t.accum[n.Dir] {
		present[r.ID.key()] = true
	}
	deleted := map[string]bool{}
	for _, d := range n.Deletes {
		deleted[d.key()] = true
	}

	dirs := make([]string, 0, len(refByDir))
	for d := range refByDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var names []string
	var out []Finding
	for _, d := range dirs {
		base := t.nodes[d]
		if base == nil || n.HasBase(d) {
			continue
		}
		for _, decl := range base.FileRefs {
			if refByDir[d][decl.Path] {
				continue
			}
			for _, r := range base.FileResources {
				if r.File != decl.Path {
					continue
				}
				if present[r.ID.key()] || deleted[r.ID.key()] {
					continue
				}
				names = append(names, r.ID.String())
				out = append(out, Finding{
					Rule:     RuleDroppedResource,
					Severity: SeverityWarning,
					Overlay:  ovDir,
					File:     file,
					Line:     lineByDir[d],
					Message: fmt.Sprintf("%s declares %s (%s) but this kustomization lists files from %s one by one and leaves it out, with no $patch: delete",
						t.rel(base.File), filepath.Base(decl.Path), r.ID.String(), t.rel(d)),
				})
			}
		}
	}
	return names, out
}

func matchResources(rs []Resource, tg Target, exact bool) []Resource {
	var out []Resource
	for _, r := range rs {
		if tg.Kind != "" && !strings.EqualFold(tg.Kind, r.ID.Kind) {
			continue
		}
		if tg.Namespace != "" && !strings.EqualFold(tg.Namespace, r.ID.Namespace) {
			continue
		}
		if tg.Name != "" && !nameMatches(tg.Name, r.ID.Name, exact) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// nameMatches mirrors kustomize: a patches: target name is a regular
// expression, a name read out of a patch document is literal.
func nameMatches(pattern, name string, exact bool) bool {
	if pattern == name {
		return true
	}
	if exact {
		return false
	}
	re, err := regexp.Compile("^(?:" + pattern + ")$")
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].File != f[j].File {
			return f[i].File < f[j].File
		}
		if f[i].Line != f[j].Line {
			return f[i].Line < f[j].Line
		}
		return f[i].Rule < f[j].Rule
	})
}

func stats(r *Report) Summary {
	s := Summary{Overlays: len(r.Overlays), Findings: len(r.Findings)}
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityError:
			s.Errors++
		case SeverityWarning:
			s.Warnings++
		}
	}
	return s
}

// Filter drops findings below min severity or in the ignore list.
func (r *Report) Filter(min Severity, ignore []string) *Report {
	skip := map[string]bool{}
	for _, rule := range ignore {
		skip[rule] = true
	}
	out := &Report{Root: r.Root, Overlays: r.Overlays}
	for _, f := range r.Findings {
		if skip[f.Rule] || f.Severity.Rank() < min.Rank() {
			continue
		}
		out.Findings = append(out.Findings, f)
	}
	out.Stats = stats(out)
	return out
}

// ExitCode is 1 once a finding reaches the --fail-on threshold, else 0.
func ExitCode(findings []Finding, failOn string) int {
	if failOn == "never" {
		return 0
	}
	threshold, err := ParseSeverity(failOn)
	if err != nil {
		threshold = SeverityError
	}
	for _, f := range findings {
		if f.Severity.Rank() >= threshold.Rank() {
			return 1
		}
	}
	return 0
}
