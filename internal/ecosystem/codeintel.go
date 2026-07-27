package ecosystem

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Codemap with optional project root (-C)
// ---------------------------------------------------------------------------

// CodemapOpts binds codemap invocations to a project directory.
type CodemapOpts struct {
	// Path is passed as `codemap -C <path>` when non-empty so symbol-at /
	// impact resolve against the correct index instead of the caller's cwd.
	Path string
}

func codemapArgs(opts CodemapOpts, rest ...string) []string {
	var args []string
	if opts.Path != "" {
		args = append(args, "-C", opts.Path)
	}
	return append(args, rest...)
}

// CodemapSymbolAtPath is CodemapSymbolAt with an optional project root.
func CodemapSymbolAtPath(ctx context.Context, file string, line int, opts CodemapOpts) (SymbolAt, error) {
	args := codemapArgs(opts, "symbol-at", fmt.Sprintf("%s:%d", file, line), "--json")
	out, err := runJSON(ctx, "codemap", args...)
	if err != nil {
		return SymbolAt{}, err
	}
	return decodeJSON[SymbolAt](out, "codemap symbol-at")
}

// CodemapImpactAtPath is CodemapImpactAt with an optional project root.
func CodemapImpactAtPath(ctx context.Context, file string, line, depth int, opts CodemapOpts) (Impact, error) {
	rest := []string{"impact", "--at", fmt.Sprintf("%s:%d", file, line), "--json"}
	if depth > 0 {
		rest = append(rest, "--depth", strconv.Itoa(depth))
	}
	out, err := runJSON(ctx, "codemap", codemapArgs(opts, rest...)...)
	if err != nil {
		return Impact{}, err
	}
	return decodeJSON[Impact](out, "codemap impact")
}

// ---------------------------------------------------------------------------
// Vecgrep
// ---------------------------------------------------------------------------

// VecgrepAvailable reports whether the vecgrep binary is on PATH.
func VecgrepAvailable() bool {
	_, err := exec.LookPath("vecgrep")
	return err == nil
}

// VecgrepHit is one search/similar result. Field names match vecgrep -f json.
type VecgrepHit struct {
	ChunkID      int     `json:"chunk_id"`
	FilePath     string  `json:"file_path,omitempty"`
	RelativePath string  `json:"relative_path,omitempty"`
	Content      string  `json:"content,omitempty"`
	StartLine    int     `json:"start_line,omitempty"`
	EndLine      int     `json:"end_line,omitempty"`
	ChunkType    string  `json:"chunk_type,omitempty"`
	SymbolName   string  `json:"symbol_name,omitempty"`
	Language     string  `json:"language,omitempty"`
	Score        float64 `json:"score,omitempty"`
	Distance     float64 `json:"distance,omitempty"`
}

// VecgrepSearchOpts controls `vecgrep search`.
type VecgrepSearchOpts struct {
	// Dir is the project root. When set, the subprocess runs with that cwd
	// so vecgrep picks the right indexed project.
	Dir      string
	Limit    int
	Mode     string // hybrid|semantic|keyword; empty = vecgrep default
	MinScore float64
	Lang     string
	Symbol   string // scope via codemap blast radius when set
}

// VecgrepIndex describes whether a vecgrep project is indexed and fresh.
type VecgrepIndex struct {
	Indexed bool `json:"indexed"`
	Fresh   bool `json:"fresh"`
	Chunks  int  `json:"chunks"`
}

// VecgrepSearchEnvelope is vecgrep's durable machine contract. Warning holds
// successful stderr diagnostics such as hybrid-search fallback to keyword.
type VecgrepSearchEnvelope struct {
	SchemaVersion int          `json:"schema_version"`
	Index         VecgrepIndex `json:"index"`
	Hits          []VecgrepHit `json:"hits"`
	Warning       string       `json:"warning,omitempty"`
}

// VecgrepSearch shells out to `vecgrep search <query> -f json-envelope` and
// only returns hits from a present, fresh index.
func VecgrepSearch(ctx context.Context, query string, opts VecgrepSearchOpts) ([]VecgrepHit, error) {
	env, err := VecgrepSearchWithReadiness(ctx, query, opts)
	if err != nil {
		return nil, err
	}
	if !env.Index.Indexed {
		return nil, fmt.Errorf("vecgrep search: project is not indexed")
	}
	if !env.Index.Fresh {
		return nil, fmt.Errorf("vecgrep search: project index is stale")
	}
	return env.Hits, nil
}

// VecgrepSearchWithReadiness preserves index state and successful warnings.
func VecgrepSearchWithReadiness(ctx context.Context, query string, opts VecgrepSearchOpts) (VecgrepSearchEnvelope, error) {
	if strings.TrimSpace(query) == "" {
		return VecgrepSearchEnvelope{}, fmt.Errorf("vecgrep search: empty query")
	}
	args := []string{"search", query, "-f", "json-envelope"}
	if opts.Limit > 0 {
		args = append(args, "-n", strconv.Itoa(opts.Limit))
	}
	if opts.Mode != "" {
		args = append(args, "-m", opts.Mode)
	}
	if opts.MinScore > 0 {
		args = append(args, "--min-score", strconv.FormatFloat(opts.MinScore, 'f', -1, 64))
	}
	if opts.Lang != "" {
		args = append(args, "-l", opts.Lang)
	}
	if opts.Symbol != "" {
		args = append(args, "--symbol", opts.Symbol)
	}
	out, warning, err := runJSONDirDetailed(ctx, opts.Dir, "vecgrep", args...)
	if err != nil {
		return VecgrepSearchEnvelope{}, err
	}
	if err := validateVecgrepEnvelopeShape(out); err != nil {
		return VecgrepSearchEnvelope{}, &Wrap{Cmd: "vecgrep search", Err: err, Output: string(out)}
	}
	env, err := decodeJSON[VecgrepSearchEnvelope](out, "vecgrep search")
	if err != nil {
		return VecgrepSearchEnvelope{}, err
	}
	if env.SchemaVersion != 1 {
		return VecgrepSearchEnvelope{}, fmt.Errorf("vecgrep search: unsupported envelope schema_version %d", env.SchemaVersion)
	}
	if env.Hits == nil {
		env.Hits = []VecgrepHit{}
	}
	env.Warning = warning
	return env, nil
}

func validateVecgrepEnvelopeShape(out []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		return err
	}
	for _, field := range []string{"schema_version", "index", "hits"} {
		if _, ok := top[field]; !ok {
			return fmt.Errorf("missing required field %q", field)
		}
	}
	var index map[string]json.RawMessage
	if err := json.Unmarshal(top["index"], &index); err != nil {
		return fmt.Errorf("index: %w", err)
	}
	for _, field := range []string{"indexed", "fresh", "chunks"} {
		if _, ok := index[field]; !ok {
			return fmt.Errorf("index missing required field %q", field)
		}
	}
	return nil
}

// VecgrepReadiness performs a bounded keyword query solely to read the
// json-envelope index state before trusting similar/search results.
func VecgrepReadiness(ctx context.Context, dir string) (VecgrepSearchEnvelope, error) {
	return VecgrepSearchWithReadiness(ctx, "monitor", VecgrepSearchOpts{Dir: dir, Limit: 1, Mode: "keyword"})
}

// VecgrepSimilarOpts controls `vecgrep similar`.
type VecgrepSimilarOpts struct {
	Dir   string
	Limit int
	Lang  string
}

// VecgrepSimilarAt runs `vecgrep similar <file>:<line> -f json`.
func VecgrepSimilarAt(ctx context.Context, file string, line int, opts VecgrepSimilarOpts) ([]VecgrepHit, error) {
	if file == "" || line <= 0 {
		return nil, fmt.Errorf("vecgrep similar: need file:line")
	}
	// Prefer a path relative to Dir when possible — vecgrep indexes relative paths.
	target := file
	if opts.Dir != "" {
		if rel, err := filepath.Rel(opts.Dir, file); err == nil && !strings.HasPrefix(rel, "..") {
			target = rel
		}
	}
	args := []string{"similar", fmt.Sprintf("%s:%d", target, line), "-f", "json"}
	if opts.Limit > 0 {
		args = append(args, "-n", strconv.Itoa(opts.Limit))
	}
	if opts.Lang != "" {
		args = append(args, "-l", opts.Lang)
	}
	out, err := runJSONDir(ctx, opts.Dir, "vecgrep", args...)
	if err != nil {
		return nil, err
	}
	return decodeVecgrepHits(out, "vecgrep similar")
}

// VecgrepSimilarText runs `vecgrep similar --text <text> -f json`.
func VecgrepSimilarText(ctx context.Context, text string, opts VecgrepSimilarOpts) ([]VecgrepHit, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("vecgrep similar: empty text")
	}
	args := []string{"similar", "--text", text, "-f", "json"}
	if opts.Limit > 0 {
		args = append(args, "-n", strconv.Itoa(opts.Limit))
	}
	if opts.Lang != "" {
		args = append(args, "-l", opts.Lang)
	}
	out, err := runJSONDir(ctx, opts.Dir, "vecgrep", args...)
	if err != nil {
		return nil, err
	}
	return decodeVecgrepHits(out, "vecgrep similar")
}

func decodeVecgrepHits(out []byte, cmd string) ([]VecgrepHit, error) {
	out = bytesTrimSpace(out)
	if len(out) == 0 {
		return nil, nil
	}
	// Prefer a bare array (default -f json). Also accept {results:[...]} envelopes.
	if out[0] == '[' {
		return decodeJSON[[]VecgrepHit](out, cmd)
	}
	var env struct {
		Results []VecgrepHit `json:"results"`
		Hits    []VecgrepHit `json:"hits"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, &Wrap{Cmd: cmd, Err: err, Output: string(out)}
	}
	if len(env.Results) > 0 {
		return env.Results, nil
	}
	return env.Hits, nil
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

// runJSONDir is runJSON with an optional working directory.
func runJSONDir(ctx context.Context, dir, bin string, args ...string) ([]byte, error) {
	out, _, err := runJSONDirDetailed(ctx, dir, bin, args...)
	return out, err
}

func runJSONDirDetailed(ctx context.Context, dir, bin string, args ...string) ([]byte, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound runaway embeddings/search the same way correlate bounds codemap.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if se := strings.TrimSpace(stderr.String()); se != "" {
			return []byte(stdout.String()), se, fmt.Errorf("%s: %w (stderr: %s)", bin, err, se)
		}
		return []byte(stdout.String()), "", fmt.Errorf("%s: %w", bin, err)
	}
	return []byte(stdout.String()), strings.TrimSpace(stderr.String()), nil
}

// ---------------------------------------------------------------------------
// fcheap artifact-ref
// ---------------------------------------------------------------------------

// ArtifactRefV1 mirrors file.cheap's portable reference envelope so Chalupa
// and other consumers can link incident evidence without copying bytes.
// See https://file.cheap/integrations/local-artifact-references
type ArtifactRefV1 struct {
	Schema     string            `json:"$schema,omitempty"`
	Version    int               `json:"version"`
	Provider   string            `json:"provider"`
	URI        string            `json:"uri"`
	ArtifactID string            `json:"artifact_id,omitempty"`
	Kind       string            `json:"kind"`
	Producer   *ArtifactProducer `json:"producer,omitempty"`
	WebURL     string            `json:"web_url,omitempty"`
}

// ArtifactProducer is the producer block inside ArtifactRefV1.
type ArtifactProducer struct {
	Tool         string `json:"tool"`
	Version      string `json:"version,omitempty"`
	NativeSchema string `json:"native_schema,omitempty"`
	NativeID     string `json:"native_id,omitempty"`
	Entrypoint   string `json:"entrypoint,omitempty"`
}

// ArtifactRefOpts configures `fcheap artifact-ref`.
type ArtifactRefOpts struct {
	Kind            string
	ProducerTool    string
	ProducerVersion string
	NativeSchema    string
	NativeID        string
	Entrypoint      string
}

const (
	artifactRefSchema   = "urn:filecheap.dev:artifact-ref:v1"
	artifactRefProvider = "fcheap-local"
)

var (
	localArtifactIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$`)
	artifactKindPattern     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	artifactToolPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	artifactVersionPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	artifactNativeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	artifactPathSegment     = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// ValidateLocalStashID checks the portable ID used by fcheap-local URIs.
func ValidateLocalStashID(id string) error {
	if !localArtifactIDPattern.MatchString(id) || id == "." || id == ".." {
		return fmt.Errorf("stash id %q is not a portable local artifact id", id)
	}
	return nil
}

// Validate enforces the immutable local ArtifactRefV1 handoff contract.
func (r ArtifactRefV1) Validate() error {
	if r.Schema != artifactRefSchema {
		return fmt.Errorf("artifact-ref .$schema must be %q", artifactRefSchema)
	}
	if r.Version != 1 {
		return fmt.Errorf("artifact-ref .version must be 1")
	}
	if r.Provider != artifactRefProvider {
		return fmt.Errorf("artifact-ref .provider must be %q", artifactRefProvider)
	}
	if err := ValidateLocalStashID(r.ArtifactID); err != nil {
		return fmt.Errorf("artifact-ref .artifact_id is not a portable local stash id")
	}
	if r.URI != "fcheap://stash/"+r.ArtifactID {
		return fmt.Errorf("artifact-ref .uri must exactly match fcheap://stash/<artifact_id>")
	}
	if len(r.Kind) == 0 || len(r.Kind) > 128 || !artifactKindPattern.MatchString(r.Kind) {
		return fmt.Errorf("artifact-ref .kind must be a bounded lowercase namespaced token")
	}
	if r.WebURL != "" {
		return fmt.Errorf("artifact-ref .web_url must be omitted for fcheap-local")
	}
	if r.Producer != nil {
		if err := r.Producer.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p ArtifactProducer) validate() error {
	if p.Tool == "" || len(p.Tool) > 64 || !artifactToolPattern.MatchString(p.Tool) {
		return fmt.Errorf("artifact-ref .producer.tool is invalid")
	}
	if p.Version != "" && (len(p.Version) > 64 || !artifactVersionPattern.MatchString(p.Version)) {
		return fmt.Errorf("artifact-ref .producer.version is invalid")
	}
	if p.NativeSchema != "" {
		parsed, err := url.Parse(p.NativeSchema)
		if len(p.NativeSchema) > 256 || !asciiVisible(p.NativeSchema) || err != nil || !parsed.IsAbs() || parsed.User != nil || parsed.RawQuery != "" ||
			(parsed.Scheme == "urn" && parsed.Opaque == "") || (parsed.Scheme == "https" && parsed.Host == "") || (parsed.Scheme != "urn" && parsed.Scheme != "https") {
			return fmt.Errorf("artifact-ref .producer.native_schema is invalid")
		}
	}
	if p.NativeID != "" && (len(p.NativeID) > 160 || !artifactNativeIDPattern.MatchString(p.NativeID)) {
		return fmt.Errorf("artifact-ref .producer.native_id is invalid")
	}
	if p.Entrypoint != "" {
		if len(p.Entrypoint) > 512 || strings.Contains(p.Entrypoint, `\`) || pathpkg.IsAbs(p.Entrypoint) || pathpkg.Clean(p.Entrypoint) != p.Entrypoint {
			return fmt.Errorf("artifact-ref .producer.entrypoint is invalid")
		}
		for _, segment := range strings.Split(p.Entrypoint, "/") {
			if segment == "" || segment == "." || segment == ".." || !artifactPathSegment.MatchString(segment) {
				return fmt.Errorf("artifact-ref .producer.entrypoint is invalid")
			}
		}
	}
	return nil
}

func asciiVisible(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func decodeArtifactRef(out []byte) (ArtifactRefV1, error) {
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.DisallowUnknownFields()
	var ref ArtifactRefV1
	if err := dec.Decode(&ref); err != nil {
		return ArtifactRefV1{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ArtifactRefV1{}, fmt.Errorf("trailing JSON value")
		}
		return ArtifactRefV1{}, err
	}
	return ref, nil
}

// ArtifactRef shells out to `fcheap artifact-ref <stash-id> --json`.
func ArtifactRef(ctx context.Context, stashID string, opts ArtifactRefOpts) (ArtifactRefV1, error) {
	if stashID == "" {
		return ArtifactRefV1{}, fmt.Errorf("artifact-ref: empty stash id")
	}
	args := []string{"artifact-ref", stashID, "--json"}
	if opts.Kind != "" {
		args = append(args, "--kind", opts.Kind)
	}
	if opts.ProducerTool != "" {
		args = append(args, "--producer-tool", opts.ProducerTool)
	}
	if opts.ProducerVersion != "" {
		args = append(args, "--producer-version", opts.ProducerVersion)
	}
	if opts.NativeSchema != "" {
		args = append(args, "--native-schema", opts.NativeSchema)
	}
	if opts.NativeID != "" {
		args = append(args, "--native-id", opts.NativeID)
	}
	if opts.Entrypoint != "" {
		args = append(args, "--entrypoint", opts.Entrypoint)
	}
	out, err := runJSON(ctx, "fcheap", args...)
	if err != nil {
		return ArtifactRefV1{}, &Wrap{Cmd: "fcheap artifact-ref", Err: err, Output: string(out)}
	}
	ref, err := decodeArtifactRef(out)
	if err != nil {
		return ArtifactRefV1{}, &Wrap{Cmd: "fcheap artifact-ref", Err: err, Output: string(out)}
	}
	if err := ref.Validate(); err != nil {
		return ArtifactRefV1{}, &Wrap{Cmd: "fcheap artifact-ref", Err: err, Output: string(out)}
	}
	return ref, nil
}

// EnsureDirExists is a tiny helper for callers validating codebase roots.
func EnsureDirExists(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}
