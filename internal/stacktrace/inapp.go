package stacktrace

import (
	"path"
	"regexp"
	"strings"
)

// dependencySegments are path components that mark vendored or installed
// third-party code even inside the git root. They are matched against the
// path RELATIVE to the root (never against the root's own ancestors, so a
// checkout under ~/vendor/app is still in-app).
var dependencySegments = map[string]bool{
	"node_modules":     true,
	"bower_components": true,
	"jspm_packages":    true,
	"site-packages":    true,
	"dist-packages":    true,
	"__pypackages__":   true,
	"vendor":           true, // Go modules vendor/, Ruby vendor/bundle
	".bundle":          true,
	"gems":             true,
}

// reSourceExt is a real file extension ("", ".<anonymous>" and the like
// are not).
var reSourceExt = regexp.MustCompile(`^\.[A-Za-z0-9]+$`)

// reWebpackPath matches a webpack dev-server module path as Next.js 14
// prints it: "webpack-internal:///(rsc)/./app/page.tsx".
var reWebpackPath = regexp.MustCompile(`^webpack(?:-internal)?://(?:[^/]*)/(?:\([^)/]*\)/)?(?:\./)?`)

// webpackPath reduces a webpack module path to the project-relative path
// it names, so dev-server frames of app code can be in-app.
func webpackPath(p string) string {
	if loc := reWebpackPath.FindStringIndex(p); loc != nil {
		return p[loc[1]:]
	}
	return p
}

// reURLScheme matches a pseudo-path scheme such as "node:", "bun:", "ext:",
// "deno:", "https:" or "webpack:" (two or more letters, so a Windows drive
// letter "C:" is not a scheme).
var reURLScheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]+:`)

// isPseudoPath reports runtime-internal locations that are never files in
// the repository: node:fs, bun:main, ext:core/..., deno:..., https://...
// (remote Deno modules), <anonymous>, <frozen runpy>, <internal:kernel>,
// <string>, [eval], [eval]-wrapper, native, "index 0" (Promise.all).
func isPseudoPath(p string) bool {
	switch {
	case p == "", p == "native":
		return true
	case strings.HasPrefix(p, "["), strings.ContainsAny(p, "<>"):
		// <anonymous>, <frozen runpy>, evalmachine.<anonymous>, [eval].
		return true
	case !isWindowsAbs(p) && reURLScheme.MatchString(p):
		return true
	case !isAbsPath(p) && strings.Contains(p, " "):
		// "index 0" and similar labels. Absolute paths may contain
		// spaces ("/home/me/My Projects/app"); relative source paths
		// in stack traces practically never do.
		return true
	}
	return false
}

// normPath turns p into a cleaned, slash-separated path for prefix tests;
// Windows paths are lower-cased (their filesystems are case-insensitive).
func normPath(p string) string {
	win := isWindowsAbs(p)
	p = strings.ReplaceAll(p, `\`, "/")
	p = path.Clean(p)
	if win {
		p = strings.ToLower(p)
	}
	return p
}

// relToRoot returns abs relative to root (slash-separated) when abs is
// inside root.
func relToRoot(abs, root string) (string, bool) {
	if root == "" || !isAbsPath(abs) || isWindowsAbs(abs) != isWindowsAbs(root) {
		return "", false
	}
	a, r := normPath(abs), normPath(root)
	if a == r {
		return "", false
	}
	prefix := r
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if !strings.HasPrefix(a, prefix) {
		return "", false
	}
	// Keep the original spelling of the relative part when case folding
	// did not change byte lengths (always, for ASCII paths).
	orig := path.Clean(strings.ReplaceAll(abs, `\`, "/"))
	if len(orig) == len(a) {
		return orig[len(prefix):], true
	}
	return a[len(prefix):], true
}

func hasDependencySegment(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if dependencySegments[seg] || strings.Contains(seg, "@v") {
			return true
		}
	}
	return false
}

// relativeInApp decides a relative path (Ruby's "workload.rb", a Python
// "src/app.py", Go -trimpath output): it is taken as relative to the root,
// except for Node's legacy "internal/*.js" core modules and Go's trimmed
// standard library ("runtime/proc.go": a first element without a dot, which
// Go reserves for the standard library).
func relativeInApp(rel string) bool {
	rel = strings.TrimPrefix(strings.ReplaceAll(rel, `\`, "/"), "./")
	if strings.HasPrefix(rel, "../") || !reSourceExt.MatchString(path.Ext(rel)) {
		return false
	}
	if strings.HasPrefix(rel, "internal/") && strings.HasSuffix(rel, ".js") {
		return false
	}
	if strings.HasSuffix(rel, ".go") {
		first, _, _ := strings.Cut(rel, "/")
		if strings.Contains(rel, "/") && !strings.Contains(first, ".") {
			return false
		}
	}
	return !hasDependencySegment(rel)
}

// InApp reports whether frame f is application code: a real file under
// gitRoot that is not vendored or installed third-party code, and not a
// runtime pseudo-path. Frames outside the root (GOROOT, $GOPATH/pkg/mod, the
// Python/Ruby/Node installation, global gems) are never in-app, and neither
// is anything when gitRoot is unknown ("").
//
// It never touches the filesystem; gitRoot is compared as a path string.
func InApp(f Frame, gitRoot string) bool {
	if gitRoot == "" {
		return false
	}
	p := f.AbsPath
	if p == "" {
		p = f.Filename
	}
	p = strings.TrimPrefix(p, "file://")
	p = webpackPath(p)
	if isPseudoPath(p) {
		return false
	}
	if !isAbsPath(p) {
		return relativeInApp(p)
	}
	rel, ok := relToRoot(p, gitRoot)
	if !ok {
		return false
	}
	return !hasDependencySegment(rel)
}

// ApplyGitRoot finishes an Exception once the caller knows the git root:
// every frame (outer and chained) gets InApp, and a frame whose absolute
// path lies under gitRoot gets Filename rewritten to the root-relative,
// slash-separated form with the absolute path kept in AbsPath. With an empty
// gitRoot it only resets InApp to false.
func ApplyGitRoot(ex *Exception, gitRoot string) {
	if ex == nil {
		return
	}
	for i := range ex.Frames {
		f := &ex.Frames[i]
		f.InApp = InApp(*f, gitRoot)
		abs := f.AbsPath
		if abs == "" && isAbsPath(f.Filename) {
			abs = f.Filename
		}
		if rel, ok := relToRoot(abs, gitRoot); ok {
			f.AbsPath = abs
			f.Filename = rel
		}
	}
	for i := range ex.Chained {
		ApplyGitRoot(&ex.Chained[i], gitRoot)
	}
}
