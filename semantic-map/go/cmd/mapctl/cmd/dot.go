package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/DiyazY/di-agent/cmd/mapctl/client"
)

// newDotCmd returns `mapctl dot`, which emits the current graph as Graphviz
// DOT. Pipe to `dot -Tpdf` to produce a figure suitable for a paper or demo.
//
// Edge styling:
//   - color: green for "+" (positive), red for "-" (negative)
//   - style: dashed when the corresponding proposition is deprecated
//   - label: "{PropositionID}\nw={EMAWeight:.2f}"
func newDotCmd(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "dot",
		Short: "Emit the graph as Graphviz DOT (suitable for `dot -Tpdf`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			snap, err := deps.Client().Graph(deps.Ctx)
			if err != nil {
				return err
			}
			writeDot(cmd.OutOrStdout(), snap)
			return nil
		},
	}
}

// writeDot is the formatter. ~30 lines on purpose — keeping it inline avoids
// over-engineering a graphviz package for a single subcommand.
func writeDot(w io.Writer, snap *client.GraphSnapshot) {
	deprecated := make(map[string]bool, len(snap.Propositions))
	for _, p := range snap.Propositions {
		deprecated[p.PropositionID] = p.Deprecated
	}

	fmt.Fprintln(w, "digraph semanticmap {")
	fmt.Fprintln(w, "  rankdir=LR;")
	fmt.Fprintln(w, "  node [shape=box, style=rounded];")
	for _, c := range snap.Constructs {
		fmt.Fprintf(w, "  %q [label=%q];\n", c.ConstructID, c.ConstructID+"\\n"+c.Name)
	}
	for _, e := range snap.Edges {
		color := "darkgreen"
		if e.Direction == "-" {
			color = "firebrick"
		}
		style := "solid"
		if deprecated[e.PropositionID] {
			style = "dashed"
		}
		label := fmt.Sprintf("%s\\nw=%.2f", e.PropositionID, e.EMAWeight)
		if e.Association {
			fmt.Fprintf(w, "  %q -> %q [label=%q, color=%s, style=dotted, dir=none];\n",
				e.FromID, e.ToID, e.PropositionID+"\\nassociation only", color)
			continue
		}
		fmt.Fprintf(w, "  %q -> %q [label=%q, color=%s, style=%s];\n",
			e.FromID, e.ToID, label, color, style)
	}
	fmt.Fprintln(w, "}")
}
