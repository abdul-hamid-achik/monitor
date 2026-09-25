package explain

import (
	"strings"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
)

// LatestFilter narrows ResolveLatest's search for the "latest" issue.
type LatestFilter struct {
	Project string
	Service string
	// Kind defaults to "exception" when empty -- deliberately NOT "any" --
	// so a bare `monitor issue latest` / monitor_issue {id:"latest"}
	// answers "why did my code crash" and never surfaces a watch alert
	// (Issue.Kind "monitor.alert.<rule>") or an investigate run (kind
	// "investigation"), either of which can easily be more recently active
	// than the exception the caller actually wants. Pass Kind: "any"
	// explicitly to widen it.
	Kind string
}

// ResolveLatest returns the most recently active issue matching filter (the
// same newest-LastSeen-first order `monitor issues` and monitor_issues
// use), and the ResolvedFrom this search actually ran with. ok is false --
// never an error on its own -- when nothing matched: an empty result under
// a filter is an ordinary outcome ("no exception open right now"), not a
// failure, and the caller (the CLI's `monitor issue latest`, MCP's
// monitor_issue) should turn that into a recovery hint, not an error
// envelope.
func ResolveLatest(store *issues.Store, filter LatestFilter) (issue issues.Issue, resolved ResolvedFrom, ok bool, err error) {
	kind := strings.TrimSpace(filter.Kind)
	if kind == "" {
		kind = issues.KindException
	}
	resolved = ResolvedFrom{ID: "latest", Project: strings.TrimSpace(filter.Project), Service: strings.TrimSpace(filter.Service), Kind: kind}
	items, err := store.List(issues.ListOptions{
		// Statuses excludes "ignored": an issue the user explicitly told
		// monitor to stop surfacing must never win "latest" over an older
		// but still-open (or resolved-and-possibly-recurring) one just
		// because it happened to be touched more recently (a stray
		// occurrence can still update LastSeen on an ignored issue).
		Statuses: []issues.Status{issues.StatusOpen, issues.StatusResolved},
		Project:  resolved.Project, Service: resolved.Service, Kind: kind, Limit: 1,
	})
	if err != nil {
		return issues.Issue{}, resolved, false, err
	}
	if len(items) == 0 {
		return issues.Issue{}, resolved, false, nil
	}
	return items[0], resolved, true, nil
}
