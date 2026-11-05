package kustomize

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

var kustomizationNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// FindKustomization reports the kustomization file in dir, if dir has one.
func FindKustomization(dir string) (string, bool) {
	for _, n := range kustomizationNames {
		p := filepath.Join(dir, n)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// Ref is one entry of resources:/bases:, kept with its line so findings can
// point at the exact reference instead of the file as a whole.
type Ref struct {
	Raw    string
	Path   string
	Line   int
	Reason string
}

// Target is a kustomize patch target. Name is a regular expression for
// patches:, an exact string when it was read from the patch document itself.
type Target struct {
	Group     string `json:"group,omitempty"`
	Version   string `json:"version,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Selector  string `json:"labelSelector,omitempty"`
}

func (t Target) String() string {
	var parts []string
	if t.Kind != "" {
		parts = append(parts, t.Kind)
	}
	if t.Name != "" {
		parts = append(parts, t.Name)
	}
	if t.Namespace != "" {
		parts = append(parts, "ns="+t.Namespace)
	}
	if t.Selector != "" {
		parts = append(parts, "labels="+t.Selector)
	}
	if len(parts) == 0 {
		return "(no target)"
	}
	return strings.Join(parts, "/")
}

func (t Target) Empty() bool {
	return t.Kind == "" && t.Name == "" && t.Namespace == "" && t.Selector == ""
}

func (t Target) id() ResourceID {
	return ResourceID{Kind: t.Kind, Name: t.Name, Namespace: t.Namespace}
}

type Patch struct {
	Origin      string
	Path        string
	Line        int
	Target      Target
	Exact       bool
	FileMissing bool
	Delete      bool
}

type Image struct {
	Name    string
	NewName string
	NewTag  string
	Digest  string
	Line    int
}

// Value renders the override the way a reader thinks about it: what the image
// becomes, not which three fields were set.
func (i Image) Value() string {
	switch {
	case i.Digest != "" && i.NewName != "":
		return i.NewName + "@" + i.Digest
	case i.Digest != "":
		return "@" + i.Digest
	case i.NewName != "" && i.NewTag != "":
		return i.NewName + ":" + i.NewTag
	case i.NewName != "":
		return i.NewName
	case i.NewTag != "":
		return ":" + i.NewTag
	}
	return "(no change)"
}

type Replica struct {
	Name  string
	Count int
	Line  int
}

// Node is one parsed kustomization.yaml.
type Node struct {
	Dir  string
	File string

	BaseRefs    []Ref
	FileRefs    []Ref
	MissingRefs []Ref
	RemoteRefs  []Ref

	NamePrefix string
	NameSuffix string
	Namespace  string

	Patches  []Patch
	Images   []Image
	Replicas []Replica

	FileResources []Resource
	Deletes       []ResourceID
}

func (n *Node) HasBase(dir string) bool {
	for _, b := range n.BaseRefs {
		if b.Path == dir {
			return true
		}
	}
	return false
}

func parseKustomization(file string) (*Node, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	n := &Node{Dir: filepath.Dir(file), File: file}
	if len(doc.Content) == 0 {
		return n, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected a mapping at the top level of the kustomization")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		val := root.Content[i+1]
		switch key {
		case "resources", "bases":
			for _, e := range seq(val) {
				n.addRef(e.Value, e.Line)
			}
		case "namePrefix":
			n.NamePrefix = val.Value
		case "nameSuffix":
			n.NameSuffix = val.Value
		case "namespace":
			n.Namespace = val.Value
		case "patchesStrategicMerge":
			for _, e := range seq(val) {
				n.Patches = append(n.Patches, Patch{Origin: key, Path: e.Value, Line: e.Line, Exact: true})
			}
		case "patches":
			for _, e := range seq(val) {
				p := Patch{Origin: key, Line: e.Line, Path: scalar(mapGet(e, "path"))}
				if tn := mapGet(e, "target"); tn != nil {
					p.Target = parseTarget(tn)
				} else {
					// No explicit target: kustomize reads kind/name out of the
					// patch document, and that match is exact, not a regex.
					p.Exact = true
				}
				n.Patches = append(n.Patches, p)
			}
		case "patchesJson6902":
			for _, e := range seq(val) {
				p := Patch{Origin: key, Line: e.Line, Exact: true, Path: scalar(mapGet(e, "path"))}
				if tn := mapGet(e, "target"); tn != nil {
					p.Target = parseTarget(tn)
				}
				n.Patches = append(n.Patches, p)
			}
		case "images":
			for _, e := range seq(val) {
				n.Images = append(n.Images, Image{
					Name:    scalar(mapGet(e, "name")),
					NewName: scalar(mapGet(e, "newName")),
					NewTag:  scalar(mapGet(e, "newTag")),
					Digest:  scalar(mapGet(e, "digest")),
					Line:    e.Line,
				})
			}
		case "replicas":
			for _, e := range seq(val) {
				count, _ := strconv.Atoi(scalar(mapGet(e, "count")))
				n.Replicas = append(n.Replicas, Replica{
					Name:  scalar(mapGet(e, "name")),
					Count: count,
					Line:  e.Line,
				})
			}
		}
	}
	return n, nil
}

// addRef classifies a resources: entry by what it actually is on disk: a
// directory with a kustomization is a base, a file is a resource, anything
// else is a reference to nothing.
func (n *Node) addRef(raw string, line int) {
	if raw == "" {
		return
	}
	r := Ref{Raw: raw, Line: line}
	if isRemote(raw) {
		n.RemoteRefs = append(n.RemoteRefs, r)
		return
	}
	p := raw
	if !filepath.IsAbs(p) {
		p = filepath.Join(n.Dir, p)
	}
	r.Path = filepath.Clean(p)

	st, err := os.Stat(r.Path)
	switch {
	case err != nil:
		r.Reason = "no such file or directory"
		n.MissingRefs = append(n.MissingRefs, r)
	case st.IsDir():
		if _, ok := FindKustomization(r.Path); ok {
			n.BaseRefs = append(n.BaseRefs, r)
		} else {
			r.Reason = "directory holds no kustomization.yaml"
			n.MissingRefs = append(n.MissingRefs, r)
		}
	default:
		n.FileRefs = append(n.FileRefs, r)
	}
}

// isRemote spots the git-url forms of a base, which this tool cannot and does
// not try to resolve. They are reported as remote, never as missing.
func isRemote(raw string) bool {
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "git@") {
		return true
	}
	for _, host := range []string{"github.com/", "gitlab.com/", "bitbucket.org/"} {
		if strings.HasPrefix(raw, host) {
			return true
		}
	}
	return strings.Contains(raw, ".git//") || strings.Contains(raw, "?ref=")
}

// parseManifest reads every document of a YAML file and keeps the ones that
// look like Kubernetes objects.
func parseManifest(file string) ([]Resource, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var out []Resource
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if len(doc.Content) == 0 {
			continue
		}
		m := doc.Content[0]
		if m.Kind != yaml.MappingNode {
			continue
		}
		res := Resource{File: file, Line: m.Line}
		res.ID.Kind = scalar(mapGet(m, "kind"))
		if md := mapGet(m, "metadata"); md != nil {
			res.ID.Name = scalar(mapGet(md, "name"))
			res.ID.Namespace = scalar(mapGet(md, "namespace"))
		}
		if strings.EqualFold(scalar(mapGet(m, "$patch")), "delete") {
			res.Delete = true
		}
		if res.ID.Kind == "" && res.ID.Name == "" {
			continue
		}
		out = append(out, res)
	}
	return out, nil
}

func parseTarget(n *yaml.Node) Target {
	return Target{
		Group:     scalar(mapGet(n, "group")),
		Version:   scalar(mapGet(n, "version")),
		Kind:      scalar(mapGet(n, "kind")),
		Name:      scalar(mapGet(n, "name")),
		Namespace: scalar(mapGet(n, "namespace")),
		Selector:  scalar(mapGet(n, "labelSelector")),
	}
}

func seq(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}
