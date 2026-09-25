package explain

import "encoding/json"

// briefFrameLimit/briefCauseLimit are brief budget's fixed section caps
// (docs/contracts/issue-context-v1.md: "frames collapsed to in-app frames
// only ... causes capped to what fits").
const (
	briefFrameLimit = 5
	briefCauseLimit = 2
)

// Fixed free-text caps applied by capFreeText, in runes, before the
// shrink-until-it-fits backstop below ever runs. Without these, a single
// pathologically long field -- most commonly Issue.Title (mirrors a raw
// exception Value a monitored process printed, which monitor never bounds
// at ingest) -- blew brief/standard past their target sizes on its own,
// since the backstop only ever trimmed snippet/frames/causes, never title
// or free text (see budget_test.go's oversized-message case and build_test.
// go's Build-level size tests). full stays unbounded (capFreeText is a
// no-op there), matching every other budget-controlled field in this file.
const (
	briefTitleMaxRunes    = 200
	standardTitleMaxRunes = 2000
	freeTextFieldMaxRunes = 400
	snippetLineMaxRunes   = 300
)

// truncateRunes shortens s to at most max runes, marking the cut with a
// trailing ellipsis when it actually had to cut anything (byte length is
// the wrong unit here: Title/Detail/snippet lines are free text a monitored
// process printed, not guaranteed ASCII).
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// capFreeText applies capFreeText's fixed per-field caps (see the constants
// above) to every free-text field applyBudget's own size backstop does NOT
// already bound a different way (snippet/frames/causes are trimmed whole-
// element by the backstop below; this instead shortens the TEXT within a
// field that survives). A no-op at BudgetFull, matching every other cap in
// this file.
func capFreeText(c *Context, budget Budget) {
	if budget == BudgetFull {
		return
	}
	titleMax := standardTitleMaxRunes
	if budget == BudgetBrief {
		titleMax = briefTitleMaxRunes
	}
	c.Issue.Title = truncateRunes(c.Issue.Title, titleMax)
	c.LastTouched.Subject = truncateRunes(c.LastTouched.Subject, freeTextFieldMaxRunes)
	for i := range c.Degraded {
		c.Degraded[i].Detail = truncateRunes(c.Degraded[i].Detail, freeTextFieldMaxRunes)
		c.Degraded[i].Recovery = truncateRunes(c.Degraded[i].Recovery, freeTextFieldMaxRunes)
	}
	for i := range c.Next {
		c.Next[i].Why = truncateRunes(c.Next[i].Why, freeTextFieldMaxRunes)
	}
	if c.Impact.Detail != "" {
		c.Impact.Detail = truncateRunes(c.Impact.Detail, freeTextFieldMaxRunes)
	}
	if c.Culprit != nil && c.Culprit.Snippet != nil {
		for i, line := range c.Culprit.Snippet.Lines {
			c.Culprit.Snippet.Lines[i] = truncateRunes(line, snippetLineMaxRunes)
		}
	}
}

// applyBudget trims c's section detail to match budget. It first applies
// capFreeText's fixed per-field caps to every free-text field (title,
// degraded detail/recovery, snippet lines, ...) -- these bound a single
// pathologically long VALUE, which the element-level trimming below cannot
// (a 14,000-character exception message is one title, not many frames to
// drop). brief then keeps only a few frames/causes (the rest folded into
// Truncated) and strips last_touched.author_email; standard/full keep
// everything Build already assembled. It then runs the promised size
// backstop (briefMaxBytes/standardMaxBytes), progressively shrinking the
// snippet, then frames, then causes -- the same shrink-until-it-fits shape
// internal/issues' truncateExceptionInfo uses -- in case the fixed caps
// above weren't enough on their own.
func applyBudget(c *Context, budget Budget) {
	capFreeText(c, budget)

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
