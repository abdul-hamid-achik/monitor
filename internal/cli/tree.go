package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// treeNode is a process plus its children, for the process hierarchy.
type treeNode struct {
	collector.ProcessInfo
	Children []*treeNode `json:"children"`
}

func (n *treeNode) MarshalJSON() ([]byte, error) {
	process, err := json.Marshal(n.ProcessInfo)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(process, &fields); err != nil {
		return nil, err
	}
	children, err := json.Marshal(n.Children)
	if err != nil {
		return nil, err
	}
	fields["children"] = children
	return json.Marshal(fields)
}

// buildForest builds the process forest from procs. If root > 0 it returns the
// subtree rooted at that PID; otherwise it returns every top-level process
// (one whose parent isn't in the set, e.g. reparented to init). Children are
// sorted by PID for determinism.
func buildForest(procs []collector.ProcessInfo, root int32) []*treeNode {
	byPID := make(map[int32]*treeNode, len(procs))
	for _, p := range procs {
		byPID[p.PID] = &treeNode{ProcessInfo: p, Children: []*treeNode{}}
	}
	var roots []*treeNode
	for _, p := range procs {
		node := byPID[p.PID]
		if parent, ok := byPID[p.Parent]; ok && p.Parent != p.PID {
			parent.Children = append(parent.Children, node)
		} else {
			roots = append(roots, node)
		}
	}
	var sortKids func(n *treeNode)
	sortKids = func(n *treeNode) {
		sort.Slice(n.Children, func(i, j int) bool { return n.Children[i].PID < n.Children[j].PID })
		for _, c := range n.Children {
			sortKids(c)
		}
	}
	for _, r := range roots {
		sortKids(r)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].PID < roots[j].PID })

	if root > 0 {
		if n, ok := byPID[root]; ok {
			return []*treeNode{n}
		}
		return nil
	}
	return roots
}

// renderTree renders a forest as an indented text tree (depth-capped to guard
// against PID-reuse cycles).
func renderTree(roots []*treeNode) string {
	var b strings.Builder
	var walk func(n *treeNode, depth int)
	walk = func(n *treeNode, depth int) {
		if depth > 64 {
			return
		}
		fmt.Fprintf(&b, "%s%s (pid %d)  cpu %.1f%%  mem %s\n",
			strings.Repeat("  ", depth), n.Name, n.PID, n.CPUPercent, collector.FormatBytes(n.Memory))
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return b.String()
}

func newTreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tree [pid]",
		Short: "Show the process hierarchy (parent/child tree)",
		Long: `tree prints the process forest by parent/child relationship. With a
PID it prints just that process's subtree.

  monitor tree           # the whole forest
  monitor tree 1234       # the subtree rooted at pid 1234
  monitor tree --json     # nested JSON`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var root int32
			if len(args) == 1 {
				p, err := parsePID(args[0])
				if err != nil {
					return err
				}
				root = p
			}
			ctx, cancel := Context()
			defer cancel()
			c := NewCollector(0)
			info, err := collectFullSnapshot(ctx, c)
			if err != nil {
				return err
			}
			if root > 0 {
				found := false
				for _, process := range info.Processes {
					if process.PID == root {
						found = true
						break
					}
				}
				if !found {
					if direct, directErr := c.Process(ctx, root); directErr == nil {
						info.Processes = append(info.Processes, direct)
					}
				}
			}
			forest := buildForest(info.Processes, root)
			if root > 0 && len(forest) == 0 {
				return fmt.Errorf("pid %d not found", root)
			}
			if JSONOutput(cmd) {
				return WriteJSON(forest)
			}
			fmt.Print(renderTree(forest))
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}
