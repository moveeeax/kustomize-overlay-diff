package kustomize

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

// Tree is every kustomization under the audited root, plus any base reached
// from one of them, with the resolved resource sets cached per directory.
type Tree struct {
	Root string

	nodes  map[string]*Node
	noNode map[string]bool
	local  map[string]bool

	// accum holds the resources a kustomization sees when its own patches run:
	// everything its bases rendered, plus its own files. rendered holds the
	// same set after this kustomization's own name and namespace transforms.
	accum    map[string][]Resource
	rendered map[string][]Resource

	parseErrs      []ParseError
	cycleFindings  []Finding
	reportedCycles map[string]bool
}

func Load(root string) (*Tree, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}

	t := &Tree{
		Root:           abs,
		nodes:          map[string]*Node{},
		noNode:         map[string]bool{},
		local:          map[string]bool{},
		accum:          map[string][]Resource{},
		rendered:       map[string][]Resource{},
		reportedCycles: map[string]bool{},
	}

	var dirs []string
	walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != abs && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		for _, name := range kustomizationNames {
			if d.Name() == name {
				dirs = append(dirs, filepath.Dir(p))
				break
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		t.local[d] = true
		t.loadDir(d)
	}
	if len(t.nodes) == 0 {
		return nil, fmt.Errorf("no kustomization.yaml found under %s", root)
	}
	return t, nil
}

func (t *Tree) ParseErrors() []ParseError { return t.parseErrs }

func (t *Tree) loadDir(dir string) {
	if _, ok := t.nodes[dir]; ok {
		return
	}
	if t.noNode[dir] {
		return
	}
	file, ok := FindKustomization(dir)
	if !ok {
		t.noNode[dir] = true
		return
	}
	n, err := parseKustomization(file)
	if err != nil {
		t.parseErrs = append(t.parseErrs, ParseError{File: file, Err: err})
		t.noNode[dir] = true
		return
	}
	t.nodes[dir] = n

	for _, r := range n.FileRefs {
		rs, err := parseManifest(r.Path)
		if err != nil {
			t.parseErrs = append(t.parseErrs, ParseError{File: r.Path, Err: err})
			continue
		}
		n.FileResources = append(n.FileResources, rs...)
	}
	t.resolvePatchTargets(n)

	for _, b := range n.BaseRefs {
		t.loadDir(b.Path)
	}
}

// resolvePatchTargets fills in the target of every patch that did not declare
// one, by reading the patch document. A patch file listed but absent gets
// flagged instead of silently matching nothing.
func (t *Tree) resolvePatchTargets(n *Node) {
	var extra []Patch
	for i := range n.Patches {
		p := &n.Patches[i]
		if p.Path == "" {
			continue
		}
		path := p.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(n.Dir, path)
		}
		if _, err := os.Stat(path); err != nil {
			if p.Target.Empty() {
				p.FileMissing = true
			}
			continue
		}
		rs, err := parseManifest(path)
		if err != nil {
			t.parseErrs = append(t.parseErrs, ParseError{File: path, Err: err})
			continue
		}
		for _, r := range rs {
			if r.Delete {
				n.Deletes = append(n.Deletes, r.ID)
			}
		}
		if !p.Target.Empty() || len(rs) == 0 {
			continue
		}
		p.Target = Target{Kind: rs[0].ID.Kind, Name: rs[0].ID.Name, Namespace: rs[0].ID.Namespace}
		p.Delete = rs[0].Delete
		// A multi-document patch file patches several resources; each one is
		// its own target and its own possible dead patch.
		for _, r := range rs[1:] {
			extra = append(extra, Patch{
				Origin: p.Origin,
				Path:   p.Path,
				Line:   p.Line,
				Exact:  true,
				Target: Target{Kind: r.ID.Kind, Name: r.ID.Name, Namespace: r.ID.Namespace},
				Delete: r.Delete,
			})
		}
	}
	n.Patches = append(n.Patches, extra...)
}

// resolve returns the resource set a kustomization produces for its parents,
// caching both the pre-transform (accum) and post-transform (rendered) sets.
func (t *Tree) resolve(dir string, stack []string) []Resource {
	if r, ok := t.rendered[dir]; ok {
		return r
	}
	for i, s := range stack {
		if s == dir {
			cycle := make([]string, 0, len(stack)-i+1)
			cycle = append(cycle, stack[i:]...)
			cycle = append(cycle, dir)
			t.noteCycle(cycle)
			return nil
		}
	}
	n := t.nodes[dir]
	if n == nil {
		return nil
	}

	stack = append(stack, dir)
	var acc []Resource
	for _, b := range n.BaseRefs {
		acc = append(acc, t.resolve(b.Path, stack)...)
	}
	acc = append(acc, n.FileResources...)
	t.accum[dir] = acc

	out := make([]Resource, 0, len(acc))
	for _, r := range acc {
		r.ID.Name = n.NamePrefix + r.ID.Name + n.NameSuffix
		if n.Namespace != "" {
			r.ID.Namespace = n.Namespace
		}
		out = append(out, r)
	}
	t.rendered[dir] = out
	return out
}

func (t *Tree) noteCycle(cycle []string) {
	rels := make([]string, 0, len(cycle))
	for _, c := range cycle {
		rels = append(rels, t.rel(c))
	}
	key := strings.Join(rels, ">")
	if t.reportedCycles[key] {
		return
	}
	t.reportedCycles[key] = true

	head := cycle[0]
	file := t.rel(head)
	if n := t.nodes[head]; n != nil {
		file = t.rel(n.File)
	}
	t.cycleFindings = append(t.cycleFindings, Finding{
		Rule:     RuleCycle,
		Severity: SeverityError,
		Overlay:  t.rel(head),
		File:     file,
		Message:  "base chain forms a cycle: " + strings.Join(rels, " -> "),
	})
}

// chain lists the bases a kustomization inherits from, deepest first.
func (t *Tree) chain(dir string) []string {
	var out []string
	seen := map[string]bool{dir: true}
	var walk func(string, int)
	walk = func(d string, depth int) {
		if depth > 64 {
			return
		}
		n := t.nodes[d]
		if n == nil {
			return
		}
		for _, b := range n.BaseRefs {
			if seen[b.Path] {
				continue
			}
			seen[b.Path] = true
			walk(b.Path, depth+1)
			out = append(out, b.Path)
		}
	}
	walk(dir, 0)
	return out
}

func (t *Tree) rel(p string) string {
	r, err := filepath.Rel(t.Root, p)
	if err != nil || strings.HasPrefix(r, "..") {
		return p
	}
	return r
}
