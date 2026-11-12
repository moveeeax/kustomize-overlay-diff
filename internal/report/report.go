// Package report renders an analysed kustomize tree for a terminal, a pull
// request comment or another program.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/moveeeax/kustomize-overlay-diff/internal/kustomize"
)

// Formats lists the accepted --format values.
func Formats() []string { return []string{"table", "json", "markdown", "github"} }

func Write(w io.Writer, r *kustomize.Report, format string) error {
	switch format {
	case "table":
		return Table(w, r)
	case "json":
		return JSON(w, r)
	case "markdown":
		return Markdown(w, r)
	case "github":
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return GitHub(w, r, cwd)
	}
	return fmt.Errorf("unknown format %q (want %s)", format, strings.Join(Formats(), ", "))
}

func JSON(w io.Writer, r *kustomize.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func Table(w io.Writer, r *kustomize.Report) error {
	fmt.Fprintf(w, "tree %s\n", r.Root)
	for _, ov := range r.Overlays {
		fmt.Fprintf(w, "\n%s  (%s)\n", ov.Dir, plural(ov.Resources, "resource", "resources"))
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		if len(ov.Chain) > 0 {
			fmt.Fprintf(tw, "  chain\t%s\n", strings.Join(append(ov.Chain, ov.Dir), " -> "))
		} else {
			fmt.Fprintf(tw, "  chain\t(root base)\n")
		}
		if tr := transforms(ov); tr != "" {
			fmt.Fprintf(tw, "  transforms\t%s\n", tr)
		}
		for _, a := range ov.Added {
			fmt.Fprintf(tw, "  adds\t%s\n", a)
		}
		for _, d := range ov.Dropped {
			fmt.Fprintf(tw, "  drops\t%s\n", d)
		}
		for _, p := range ov.PatchTargets {
			fmt.Fprintf(tw, "  patch\t%s -> %s %s\n", patchName(p), p.Target, matchText(p.Matches))
		}
		for _, img := range ov.ImageOverrides {
			if img.From != "" {
				fmt.Fprintf(tw, "  image\t%s %s -> %s\n", img.Name, img.From, img.To)
			} else {
				fmt.Fprintf(tw, "  image\t%s -> %s\n", img.Name, img.To)
			}
		}
		if len(ov.ReplicaOverrides) > 0 {
			fmt.Fprintf(tw, "  replicas\t%s\n", strings.Join(ov.ReplicaOverrides, " "))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	if len(r.Findings) == 0 {
		fmt.Fprintf(w, "\nno findings\n")
		return nil
	}
	fmt.Fprintf(w, "\nfindings\n")
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "SEVERITY\tRULE\tLOCATION\tMESSAGE\n")
	for _, f := range r.Findings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Severity, f.Rule, f.Location(), f.Message)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n%s\n", countLine(r))
	return nil
}

func Markdown(w io.Writer, r *kustomize.Report) error {
	fmt.Fprintf(w, "## kustomize overlay diff\n\n")
	fmt.Fprintf(w, "`%s` — %s\n", r.Root, countLine(r))

	for _, ov := range r.Overlays {
		fmt.Fprintf(w, "\n### `%s`\n\n", ov.Dir)
		if len(ov.Chain) > 0 {
			fmt.Fprintf(w, "- chain: `%s`\n", strings.Join(append(ov.Chain, ov.Dir), "` -> `"))
		} else {
			fmt.Fprintf(w, "- chain: root base\n")
		}
		if tr := transforms(ov); tr != "" {
			fmt.Fprintf(w, "- transforms: `%s`\n", tr)
		}
		for _, a := range ov.Added {
			fmt.Fprintf(w, "- adds `%s`\n", a)
		}
		for _, d := range ov.Dropped {
			fmt.Fprintf(w, "- drops `%s`\n", d)
		}
		for _, p := range ov.PatchTargets {
			fmt.Fprintf(w, "- patch `%s` -> `%s` %s\n", patchName(p), p.Target, matchText(p.Matches))
		}
		for _, img := range ov.ImageOverrides {
			if img.From != "" {
				fmt.Fprintf(w, "- image `%s` `%s` -> `%s`\n", img.Name, img.From, img.To)
			} else {
				fmt.Fprintf(w, "- image `%s` -> `%s`\n", img.Name, img.To)
			}
		}
		if len(ov.ReplicaOverrides) > 0 {
			fmt.Fprintf(w, "- replicas `%s`\n", strings.Join(ov.ReplicaOverrides, "` `"))
		}
	}

	if len(r.Findings) == 0 {
		fmt.Fprintf(w, "\nNo findings.\n")
		return nil
	}
	fmt.Fprintf(w, "\n### Findings\n\n")
	fmt.Fprintf(w, "| severity | rule | location | message |\n|---|---|---|---|\n")
	for _, f := range r.Findings {
		fmt.Fprintf(w, "| %s | `%s` | `%s` | %s |\n", f.Severity, f.Rule, f.Location(), mdEscape(f.Message))
	}
	return nil
}

// GitHub prints one workflow command per finding, which a GitHub Actions
// runner turns into an annotation on that file and line. GitHub wants paths
// relative to the checkout, so each file is made relative to base, normally
// the working directory the Action runs in (the workspace).
func GitHub(w io.Writer, r *kustomize.Report, base string) error {
	for _, f := range r.Findings {
		level := "warning"
		if f.Severity == kustomize.SeverityError {
			level = "error"
		}
		props := "file=" + ghProperty(annotationPath(r.Root, f.File, base))
		if f.Line > 0 {
			props += fmt.Sprintf(",line=%d", f.Line)
		}
		props += ",title=" + ghProperty(f.Rule)
		if _, err := fmt.Fprintf(w, "::%s %s::%s\n", level, props, ghData(f.Message)); err != nil {
			return err
		}
	}
	return nil
}

// annotationPath resolves a finding's file against the tree root and returns
// it relative to base. A file outside base keeps its absolute path, since a
// "../" path would not name anything in the checkout either.
func annotationPath(root, file, base string) string {
	abs := file
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, file)
	}
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// ghData and ghProperty escape the way @actions/core does, so a message or
// path holding a newline, comma or colon cannot end the command early.
func ghData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func ghProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

func transforms(ov kustomize.Overlay) string {
	var parts []string
	if ov.NamePrefix != "" {
		parts = append(parts, "namePrefix="+ov.NamePrefix)
	}
	if ov.NameSuffix != "" {
		parts = append(parts, "nameSuffix="+ov.NameSuffix)
	}
	if ov.Namespace != "" {
		parts = append(parts, "namespace="+ov.Namespace)
	}
	return strings.Join(parts, " ")
}

func patchName(p kustomize.PatchTarget) string {
	if p.Path != "" {
		return p.Path
	}
	return p.Origin
}

func matchText(matches []string) string {
	if len(matches) == 0 {
		return "(matches nothing)"
	}
	return "matches " + strings.Join(matches, ", ")
}

func countLine(r *kustomize.Report) string {
	overlays := plural(r.Stats.Overlays, "overlay", "overlays")
	if r.Stats.Findings == 0 {
		return overlays + ", no findings"
	}
	return fmt.Sprintf("%s, %s (%s, %s)", overlays,
		plural(r.Stats.Findings, "finding", "findings"),
		plural(r.Stats.Errors, "error", "errors"),
		plural(r.Stats.Warnings, "warning", "warnings"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func mdEscape(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
