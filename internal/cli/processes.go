package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
)

// ProcessList is the stable envelope emitted by `monitor processes --json`.
// Counts make truncation and system-process filtering explicit for agents.
type ProcessList struct {
	Total          int                     `json:"total"`
	Matched        int                     `json:"matched"`
	Returned       int                     `json:"returned"`
	Limit          int                     `json:"limit"`
	Sort           string                  `json:"sort"`
	Filter         string                  `json:"filter,omitempty"`
	IncludesSystem bool                    `json:"includes_system"`
	Truncated      bool                    `json:"truncated"`
	State          collector.MetricStatus  `json:"state"`
	Processes      []collector.ProcessInfo `json:"processes"`
}

func newProcessesCmd() *cobra.Command {
	var (
		limit         int
		sortBy        string
		filter        string
		includeSystem bool
	)
	cmd := &cobra.Command{
		Use:     "processes",
		Aliases: []string{"ps"},
		Short:   "List and filter running processes",
		Long: `List a bounded process inventory without fetching the full snapshot.

Examples:
  monitor processes --sort memory --limit 10
  monitor ps --filter go --json
  monitor processes --system --sort pid`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			settings, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if limit == 0 {
				limit = settings.MaxProcesses
			}
			if limit < 1 || limit > 1000 {
				return fmt.Errorf("limit must be between 1 and 1000")
			}
			if !cmd.Flags().Changed("system") && !cmd.Flags().Changed("all") {
				includeSystem = settings.ShowSystemProcesses
			}

			c := NewCollector(0)
			ctx, cancel := Context()
			defer cancel()
			info := c.Collect(ctx)
			list, err := buildProcessList(info.Processes, info.ProcessesState, processListOptions{
				Limit: limit, Sort: sortBy, Filter: filter, IncludeSystem: includeSystem,
			})
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				return WriteJSON(list)
			}
			return printProcessList(cmd, list)
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 0, "maximum rows (default: config max_processes, max 1000)")
	cmd.Flags().StringVar(&sortBy, "sort", "cpu", "sort by cpu, memory, pid, or name")
	cmd.Flags().StringVarP(&filter, "filter", "f", "", "case-insensitive name, PID, user, or status substring")
	cmd.Flags().BoolVar(&includeSystem, "system", false, "include system-owned processes")
	cmd.Flags().BoolVar(&includeSystem, "all", false, "include system-owned processes (alias for --system)")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

type processListOptions struct {
	Limit         int
	Sort          string
	Filter        string
	IncludeSystem bool
}

func buildProcessList(processes []collector.ProcessInfo, state collector.MetricStatus, opts processListOptions) (ProcessList, error) {
	sortBy := strings.ToLower(strings.TrimSpace(opts.Sort))
	switch sortBy {
	case "cpu", "memory", "pid", "name":
	default:
		return ProcessList{}, fmt.Errorf("invalid sort %q: expected cpu, memory, pid, or name", opts.Sort)
	}
	if opts.Limit < 1 || opts.Limit > 1000 {
		return ProcessList{}, fmt.Errorf("limit must be between 1 and 1000")
	}

	query := strings.ToLower(strings.TrimSpace(opts.Filter))
	matched := make([]collector.ProcessInfo, 0, len(processes))
	for _, process := range processes {
		if !opts.IncludeSystem && process.IsSystem {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(strings.Join([]string{
				process.Name,
				strconv.FormatInt(int64(process.PID), 10),
				process.User,
				process.Status,
			}, " "))
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		matched = append(matched, process)
	}

	sort.SliceStable(matched, func(i, j int) bool {
		a, b := matched[i], matched[j]
		switch sortBy {
		case "memory":
			if a.Memory != b.Memory {
				return a.Memory > b.Memory
			}
		case "pid":
			return a.PID < b.PID
		case "name":
			an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
			if an != bn {
				return an < bn
			}
		default: // cpu
			if a.CPUPercent != b.CPUPercent {
				return a.CPUPercent > b.CPUPercent
			}
		}
		return a.PID < b.PID
	})

	matchedCount := len(matched)
	truncated := matchedCount > opts.Limit
	if truncated {
		matched = matched[:opts.Limit]
	}
	if matched == nil {
		matched = []collector.ProcessInfo{}
	}
	return ProcessList{
		Total: len(processes), Matched: matchedCount, Returned: len(matched),
		Limit: opts.Limit, Sort: sortBy, Filter: strings.TrimSpace(opts.Filter),
		IncludesSystem: opts.IncludeSystem, Truncated: truncated, State: state,
		Processes: matched,
	}, nil
}

func printProcessList(cmd *cobra.Command, list ProcessList) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "PID\tSTATUS\tCPU\tMEMORY\tTHREADS\tUSER\tNAME"); err != nil {
		return err
	}
	for _, process := range list.Processes {
		status := process.Status
		if status == "" {
			status = "-"
		}
		if _, err := fmt.Fprintf(w, "%d\t%s\t%.1f%%\t%s\t%d\t%s\t%s\n",
			process.PID,
			status,
			process.CPUPercent,
			collector.FormatBytes(process.Memory),
			process.Threads,
			process.User,
			process.Name,
		); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if list.Truncated {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Showing %d of %d matched processes (use --limit to expand)\n", list.Returned, list.Matched)
		return err
	}
	return nil
}
