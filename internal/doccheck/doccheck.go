// Package doccheck keeps the repository's own documentation honest about its
// own shape. A doc that names a directory the tree does not have sends a
// contributor to a path that does not exist, and a reviewer cannot see it: the
// claim reads exactly like a true one.
package doccheck

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PathRef is one directory reference a document makes, with the file it came
// from. Line is where the reference starts, so a failure names a place to look.
type PathRef struct {
	File string
	Line int
	Path string
}

// ref matches a backticked single-segment directory: `docs/`. A deeper path is
// left alone, because a claim about a subtree is not what this check resolves.
//
// There is no allow-list: a name a document spells this way is a claim that the
// repository has that directory, so the only place the name may also live is
// the tree itself.
var ref = regexp.MustCompile("`([A-Za-z0-9_.-]+)/`")

// Check walks the Markdown at the repository root and under docs/, and returns
// every reference to a top-level directory the tree does not contain. The
// result is sorted so a failure is stable between runs.
func Check(root string) ([]PathRef, error) {
	files, err := markdown(root)
	if err != nil {
		return nil, err
	}

	var missing []PathRef
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			rel = file
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, match := range ref.FindAllStringSubmatch(line, -1) {
				name := match[1]
				if isDir(filepath.Join(root, name)) {
					continue
				}
				missing = append(missing, PathRef{File: filepath.ToSlash(rel), Line: i + 1, Path: name + "/"})
			}
		}
	}

	sort.Slice(missing, func(i, j int) bool {
		if missing[i].File != missing[j].File {
			return missing[i].File < missing[j].File
		}
		return missing[i].Line < missing[j].Line
	})
	return missing, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// markdown is every Markdown file at the repository root and under docs/. A
// package's own README is excluded: it describes code the package's tests
// already pin.
func markdown(root string) ([]string, error) {
	var files []string

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read the repository root: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			files = append(files, filepath.Join(root, entry.Name()))
		}
	}

	entries, err = os.ReadDir(filepath.Join(root, "docs"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read docs: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			files = append(files, filepath.Join(root, "docs", entry.Name()))
		}
	}

	sort.Strings(files)
	return files, nil
}
