package ingest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Source is a server-configured corpus root. Clients select sources by name;
// they cannot make the server read arbitrary paths.
type Source struct {
	Name string `json:"name"`
	Root string `json:"root"`
	// Include limits discovery to these slash-separated subdirectories or
	// files relative to Root. Empty means the whole root.
	Include []string `json:"include,omitempty"`
	// Exclude holds path.Match patterns tested against the base name and the
	// relative path of every file and directory.
	Exclude     []string `json:"exclude,omitempty"`
	Description string   `json:"description,omitempty"`
}

// MaxFileBytes bounds the size of a single ingested file.
const MaxFileBytes = 512 << 10

// skippedDirs are never descended into.
var skippedDirs = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true,
	"dist": true, "build": true, "vendor": true, ".idea": true, ".vscode": true, ".pytest_cache": true,
}

// File is a discovered candidate for ingestion.
type File struct {
	RelPath string `json:"path"`
	AbsPath string `json:"-"`
	Kind    Kind   `json:"kind"`
	Size    int64  `json:"size_bytes"`
}

// Skip records a file that was intentionally not ingested.
type Skip struct {
	RelPath string `json:"path"`
	Reason  string `json:"reason"`
}

// Validate checks a source definition.
func (s Source) Validate() error {
	if s.Name == "" || len(s.Name) > 64 || strings.ContainsAny(s.Name, "/\\\x00 ") {
		return fmt.Errorf("source name %q must be 1-64 bytes without slashes or spaces", s.Name)
	}
	info, err := os.Stat(s.Root)
	if err != nil {
		return fmt.Errorf("source %s: %w", s.Name, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %s: root %s is not a directory", s.Name, s.Root)
	}
	for _, inc := range s.Include {
		if inc == "" || path.IsAbs(inc) || strings.Contains(inc, "..") {
			return fmt.Errorf("source %s: include %q must be a relative path without '..'", s.Name, inc)
		}
	}
	for _, pattern := range s.Exclude {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("source %s: bad exclude pattern %q", s.Name, pattern)
		}
	}
	return nil
}

func (s Source) excluded(rel string) bool {
	base := path.Base(rel)
	for _, pattern := range s.Exclude {
		if ok, _ := path.Match(pattern, base); ok {
			return true
		}
		if ok, _ := path.Match(pattern, rel); ok {
			return true
		}
	}
	return false
}

// Discover lists supported files under the source, sorted by path. Symbolic
// links are not followed, so discovery cannot escape the configured root.
func Discover(src Source) ([]File, []Skip, error) {
	if err := src.Validate(); err != nil {
		return nil, nil, err
	}
	root, err := filepath.Abs(src.Root)
	if err != nil {
		return nil, nil, err
	}
	roots := []string{""}
	if len(src.Include) > 0 {
		roots = src.Include
	}
	var files []File
	var skips []Skip
	seen := map[string]bool{}
	for _, inc := range roots {
		start := filepath.Join(root, filepath.FromSlash(inc))
		err := filepath.WalkDir(start, func(abs string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && abs == start {
					return fmt.Errorf("source %s: include %q does not exist", src.Name, inc)
				}
				return err
			}
			relOS, err := filepath.Rel(root, abs)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(relOS)
			if d.IsDir() {
				if abs != start && (skippedDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") || src.excluded(rel)) {
					return filepath.SkipDir
				}
				return nil
			}
			if seen[rel] {
				return nil
			}
			seen[rel] = true
			if d.Type()&fs.ModeSymlink != 0 {
				skips = append(skips, Skip{rel, "symlink"})
				return nil
			}
			if !d.Type().IsRegular() || src.excluded(rel) || strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			kind, ok := KindForPath(rel)
			if !ok {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() > MaxFileBytes {
				skips = append(skips, Skip{rel, fmt.Sprintf("larger than %d bytes", MaxFileBytes)})
				return nil
			}
			files = append(files, File{RelPath: rel, AbsPath: abs, Kind: kind, Size: info.Size()})
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	return files, skips, nil
}

// CleanUploadPath validates a client-supplied logical path for an uploaded
// document. It is a name only; nothing is read from or written to it.
func CleanUploadPath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	clean := path.Clean("/" + p)[1:]
	if clean == "" || clean != p || strings.HasPrefix(p, "../") || len(p) > 512 {
		return "", fmt.Errorf("upload path %q must be a clean relative path", p)
	}
	return clean, nil
}
