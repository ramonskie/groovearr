package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Sentinel errors returned by ResolveWithinRoot. Their messages deliberately
// omit the offending path so callers can surface them to clients without
// leaking server-side paths (AGENTS §10).
var (
	// ErrPathOutsideRoot means the target does not live under root, either
	// lexically (a `..` escape) or after symlink resolution.
	ErrPathOutsideRoot = errors.New("path outside library root")
	// ErrNotRegularFile means the target resolved to something other than a
	// regular file (directory, device, socket, FIFO, ...).
	ErrNotRegularFile = errors.New("not a regular file")
	// ErrPathUnresolvable means the target (or root) could not be cleaned or
	// symlink-resolved, e.g. it does not exist.
	ErrPathUnresolvable = errors.New("path cannot be resolved")
)

// ResolveWithinRoot returns an absolute, symlink-resolved path guaranteed to
// live under root, or an error. Used before serving any library file over HTTP.
//
// Containment is checked twice:
//
//  1. lexically on the cleaned absolute paths (rejects `..` escapes), and
//  2. again after filepath.EvalSymlinks resolves both target and root (rejects
//     a symlink whose target points outside root).
//
// The resolved target must be a regular file; directories, devices, sockets
// and FIFOs are refused. Symlinks are followed, not rejected: a symlink whose
// target is a regular file inside root is allowed, and only a symlink whose
// resolved target falls outside root is refused by the containment check.
//
// Returned errors never include the input paths, so callers may surface them
// directly in an HTTP error body.
func ResolveWithinRoot(root, target string) (string, error) {
	// filepath.Abs cleans the path and anchors relative inputs to the working
	// directory, so the lexical check below always compares absolute paths.
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", ErrPathUnresolvable
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", ErrPathUnresolvable
	}
	if !withinRoot(rootAbs, targetAbs) {
		return "", ErrPathOutsideRoot
	}

	// Resolve symlinks in every path component before trusting containment; a
	// symlink inside root may still point outside it.
	resolvedRoot, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", ErrPathUnresolvable
	}
	resolvedTarget, err := filepath.EvalSymlinks(targetAbs)
	if err != nil {
		return "", ErrPathUnresolvable
	}
	if !withinRoot(resolvedRoot, resolvedTarget) {
		return "", ErrPathOutsideRoot
	}

	info, err := os.Lstat(resolvedTarget)
	if err != nil {
		return "", ErrPathUnresolvable
	}
	if !info.Mode().IsRegular() {
		return "", ErrNotRegularFile
	}
	return resolvedTarget, nil
}

// withinRoot reports whether target is root itself or lives under it. The
// comparison is lexical only; callers re-apply it after resolving symlinks.
func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
