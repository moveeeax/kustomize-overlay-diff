package kustomize

import (
	"fmt"
	"sort"
	"strings"
)

// Severity orders findings. There are only two: a warning is worth reading at
// review time, an error means the kustomization does not do what it says.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Rank lets callers compare severities without a switch at every call site.
func (s Severity) Rank() int {
	switch s {
	case SeverityError:
		return 2
	case SeverityWarning:
		return 1
	}
	return 0
}

func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(s) {
	case "warning":
		return SeverityWarning, nil
	case "error":
		return SeverityError, nil
	}
	return "", fmt.Errorf("unknown severity %q (want warning or error)", s)
}

// Rule ids. They are part of the CLI contract: --ignore takes these strings.
const (
	RuleDeadPatch       = "dead-patch"
	RuleShadowedImage   = "shadowed-image"
	RuleMissingBase     = "missing-base"
	RuleDroppedResource = "dropped-resource"
	RuleCycle           = "cycle"
)

var ruleSeverity = map[string]Severity{
	RuleDeadPatch:       SeverityError,
	RuleShadowedImage:   SeverityWarning,
	RuleMissingBase:     SeverityError,
	RuleDroppedResource: SeverityWarning,
	RuleCycle:           SeverityError,
}

// Rules returns every known rule id, sorted, for help text and validation.
func Rules() []string {
	out := make([]string, 0, len(ruleSeverity))
	for r := range ruleSeverity {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func KnownRule(r string) bool {
	_, ok := ruleSeverity[r]
	return ok
}

// ResourceID is how kustomize addresses a resource in a patch target: kind and
// name, with namespace only when the manifest sets one explicitly.
type ResourceID struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

func (r ResourceID) String() string {
	if r.Namespace != "" {
		return fmt.Sprintf("%s/%s.%s", r.Kind, r.Name, r.Namespace)
	}
	return r.Kind + "/" + r.Name
}

func (r ResourceID) key() string {
	return strings.ToLower(r.Kind + "/" + r.Name)
}

// Resource is one document found in a manifest file referenced by a
// kustomization, carrying the file and line it came from.
type Resource struct {
	ID   ResourceID `json:"id"`
	File string     `json:"file"`
	Line int        `json:"line"`
	// Delete is true for a strategic-merge document whose top level carries
	// "$patch: delete", which removes a base resource instead of merging.
	Delete bool `json:"-"`
}

type Finding struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Overlay  string   `json:"overlay"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Message  string   `json:"message"`
}

func (f Finding) Location() string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

// ParseError means a file could not be read as YAML at all. The CLI turns any
// of these into exit code 2, because an audit of unparsable input is a guess.
type ParseError struct {
	File string
	Line int
	Err  error
}

func (e ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %v", e.File, e.Line, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.File, e.Err)
}

func (e ParseError) Unwrap() error { return e.Err }
