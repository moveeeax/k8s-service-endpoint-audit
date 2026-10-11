package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// WriteJSON prints the report as stable, indented JSON.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteTable prints a human-readable table plus a one-line summary.
func (r Report) WriteTable(w io.Writer) error {
	if len(r.Findings) == 0 {
		_, err := fmt.Fprintf(w, "no findings: all %d service(s) resolve to something (%s mode)\n",
			r.Scanned.Services, r.Mode)
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SEVERITY\tNAMESPACE\tSERVICE\tRULE\tDETAIL")
	for _, f := range r.Findings {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			strings.ToUpper(f.Severity), f.Namespace, f.Service, f.Rule, f.Message)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, f := range r.Findings {
		if f.Hint == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "\n%s/%s (%s)\n  %s\n  hint: %s\n",
			f.Namespace, f.Service, f.Rule, f.Message, f.Hint); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\n%d finding(s) across %d service(s) (%s mode)\n",
		len(r.Findings), r.Scanned.Services, r.Mode)
	return err
}

// WriteMarkdown prints a table suitable for a pull-request comment.
func (r Report) WriteMarkdown(w io.Writer) error {
	if len(r.Findings) == 0 {
		_, err := fmt.Fprintf(w, "**k8s-service-endpoint-audit**: no findings across %d service(s).\n", r.Scanned.Services)
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**k8s-service-endpoint-audit**: %d finding(s) across %d service(s).\n\n",
		len(r.Findings), r.Scanned.Services)
	b.WriteString("| Severity | Service | Rule | Detail |\n|---|---|---|---|\n")
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "| %s | `%s/%s` | `%s` | %s |\n",
			f.Severity, f.Namespace, f.Service, f.Rule, f.Message)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
