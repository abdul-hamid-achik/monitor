package explain

import "encoding/json"

// maxTestFiles bounds ImpactInfo.TestFiles (standard/full only) -- a highly
// tested culprit could otherwise list dozens of files in what is meant to
// stay a short, human-scannable hint.
const maxTestFiles = 10

// testFilesFrom best-effort extracts human-readable file paths from
// codemap's raw `impact --at` "tests" entries. codemap's own per-entry
// shape isn't a pinned monitor contract (ecosystem.Impact keeps it as
// json.RawMessage precisely so a codemap-side field rename never breaks
// decoding here) -- this tries the two shapes codemap is known to use (an
// object with a "file" field, or a bare string) and silently drops any
// entry it doesn't recognize rather than failing the whole impact section
// over one unexpected shape. The COUNT (ImpactInfo.Tests, len(raw)) never
// depends on this succeeding; TestFiles is purely an additive, best-effort
// convenience on top of it.
func testFilesFrom(raw []json.RawMessage) []string {
	seen := make(map[string]struct{}, len(raw))
	files := make([]string, 0, len(raw))
	add := func(f string) {
		if f == "" {
			return
		}
		if _, ok := seen[f]; ok {
			return
		}
		seen[f] = struct{}{}
		files = append(files, f)
	}
	for _, entry := range raw {
		if len(files) >= maxTestFiles {
			break
		}
		var asString string
		if err := json.Unmarshal(entry, &asString); err == nil {
			add(asString)
			continue
		}
		var asObject struct {
			File string `json:"file"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(entry, &asObject); err == nil {
			add(firstNonEmptyString(asObject.File, asObject.Path))
		}
	}
	return files
}
