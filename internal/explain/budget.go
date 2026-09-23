package explain

import "encoding/json"

// briefFrameLimit/briefCauseLimit are brief budget's fixed section caps
// (docs/contracts/issue-context-v1.md: "frames collapsed to in-app frames
// only ... causes capped to what fits").
const (
	briefFrameLimit = 5
	briefCauseLimit = 2
)

// applyBudget trims c's section detail to match budget. brief keeps only a
// few frames/causes (the rest folded into Truncated) and strips
// last_touched.author_email; standard/full keep everything Build already
// assembled. It then runs the promised size backstop
// (briefMaxBytes/standardMaxBytes), progressively shrinking the snippet,
// then frames, then causes -- the same shrink-until-it-fits shape
// internal/issues' truncateExceptionInfo uses -- in case the fixed caps
// above weren't enough on their own (a pathologically long culprit
// function/file name, or one very wide snippet line).
func applyBudget(c *Context, budget Budget) {
	if budget == BudgetBrief {
		c.LastTouched = stripAuthorEmail(c.LastTouched)
		if len(c.Frames) > briefFrameLimit {
			c.Truncated.Frames += len(c.Frames) - briefFrameLimit
			c.Frames = c.Frames[:briefFrameLimit]
		}
		if len(c.Causes) > briefCauseLimit {
			// Keep the LAST briefCauseLimit entries: Causes is already
			// ordered outer-to-innermost (issues.ExceptionInfo.Causes'
			// convention), and the innermost entry is the one Issue.Culprit
			// and the fingerprint's own innermost.Type both anchor on --
			// see internal/issues/exception.go's selectCauses/dropOneCause,
			// which bias the same way.
			c.Truncated.Causes += len(c.Causes) - briefCauseLimit
			c.Causes = c.Causes[len(c.Causes)-briefCauseLimit:]
		}
	}

	var target int
	switch budget {
	case BudgetBrief:
		target = briefMaxBytes
	case BudgetStandard:
		target = standardMaxBytes
	default:
		return // full is unbounded
	}

	for {
		data, err := json.Marshal(c)
		if err != nil || len(data) <= target {
			return
		}
		switch {
		case c.Culprit != nil && c.Culprit.Snippet != nil && len(c.Culprit.Snippet.Lines) > 1:
			shrinkSnippet(c.Culprit.Snippet)
		case len(c.Frames) > 0:
			c.Truncated.Frames++
			c.Frames = c.Frames[:len(c.Frames)-1]
		case len(c.Causes) > 0:
			c.Truncated.Causes++
			c.Causes = c.Causes[:len(c.Causes)-1]
		case c.Culprit != nil && c.Culprit.Snippet != nil:
			c.Culprit.Snippet = nil
		default:
			return // nothing left to cut; report whatever size resulted
		}
	}
}

// shrinkSnippet drops one line from whichever end of the snippet is
// currently farther from Highlight, so the highlighted line stays as close
// to centered as the remaining budget allows.
func shrinkSnippet(s *Snippet) {
	if len(s.Lines) <= 1 {
		return
	}
	first, last := s.Start, s.Start+len(s.Lines)-1
	if s.Highlight-first >= last-s.Highlight {
		s.Lines = s.Lines[1:]
		s.Start++
	} else {
		s.Lines = s.Lines[:len(s.Lines)-1]
	}
}
