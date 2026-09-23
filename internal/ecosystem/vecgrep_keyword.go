package ecosystem

import "context"

// KeywordSearch runs `vecgrep search <query> --mode keyword` (BM25 only) in
// dir and returns its hits. It is the one entry point internal/explain's
// message-search culprit fallback (E2.8) uses: keyword mode never sends the
// query text to an embedding provider (see docs/contracts/
// local-sentry-naming.md §7 -- error text must never reach a remote
// provider), unlike vecgrep's default hybrid mode or an explicit semantic
// one. Callers should have already confirmed readiness with ProbeVecgrep
// (HealthOK) before calling this; KeywordSearch itself does not probe.
//
// limit <= 0 uses vecgrep's own default (10).
func KeywordSearch(ctx context.Context, dir, query string, limit int) ([]VecgrepHit, error) {
	env, err := VecgrepSearchWithReadiness(ctx, query, VecgrepSearchOpts{
		Dir: dir, Mode: "keyword", Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	return env.Hits, nil
}
