package popnow

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This repository is public and read by developers everywhere, so every tracked file is English,
// and it names only PopNow. These two scans keep it that way mechanically, because a reviewer's
// eye is the least reliable guard there is.

// cjkPattern matches Han, Hiragana, Katakana and Hangul characters.
var cjkPattern = regexp.MustCompile(`[\x{4e00}-\x{9fff}\x{3400}-\x{4dbf}\x{3040}-\x{30ff}\x{ac00}-\x{d7af}]`)

// foreignNamePattern matches names that do not belong in this repository: other API brands,
// generation backends and internal documentation paths. The words are split so that this file
// does not match itself.
var foreignNamePattern = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`sp` + `icy`,
	`\bk` + `ie\b`,
	`k` + `ie\.ai`,
	`wave` + `speed`,
	`atlas` + `cloud`,
	`\bfal\.` + `ai\b`,
	`repli` + `cate\.com`,
	`foun` + `dation/`,
}, "|"))

// minimumScannedFiles is the counter-check: a scan that silently reads nothing passes forever.
const minimumScannedFiles = 15

func TestRepositoryIsEnglishAndNamesOnlyPopNow(t *testing.T) {
	files := repositoryFiles(t)
	if len(files) < minimumScannedFiles {
		t.Fatalf("scanned only %d files (want at least %d); the guard is not reading the repository", len(files), minimumScannedFiles)
	}

	sawSelf := false
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if name == "guard_test.go" {
			sawSelf = true
		}
		if bytes.IndexByte(data, 0) >= 0 {
			// Binary files (the logo images) carry no prose.
			continue
		}
		for number, line := range strings.Split(string(data), "\n") {
			if cjkPattern.MatchString(line) {
				t.Errorf("%s:%d contains CJK text; this repository is English only", name, number+1)
			}
			if match := foreignNamePattern.FindString(line); match != "" {
				t.Errorf("%s:%d mentions %q; this repository names only PopNow", name, number+1, match)
			}
		}
	}
	if !sawSelf {
		t.Error("the scan did not include this file, so its file list is wrong")
	}
}

// TestGuardPatternsActuallyMatch is the counter-check for the patterns themselves.
func TestGuardPatternsActuallyMatch(t *testing.T) {
	samples := map[string]*regexp.Regexp{
		"\u4e2d\u6587":              cjkPattern,
		"\u30ab\u30bf":              cjkPattern,
		"\ud55c\uae00":              cjkPattern,
		"Sp" + "icyAPI":             foreignNamePattern,
		"docs/foun" + "dation/x.md": foreignNamePattern,
	}
	for sample, pattern := range samples {
		if !pattern.MatchString(sample) {
			t.Errorf("pattern %s does not match %q", pattern, sample)
		}
	}
	// And the patterns must not flag ordinary text, or every test would go red at once.
	for _, clean := range []string{"PopNow Go SDK", "kinetic", "Cyrillic \u0416 Greek \u03a9 emoji \U0001F680"} {
		if cjkPattern.MatchString(clean) || foreignNamePattern.MatchString(clean) {
			t.Errorf("a guard pattern flags clean text %q", clean)
		}
	}
}

// repositoryFiles lists the tracked files, or every file under the working tree when git is not
// available (a source archive) or nothing is tracked yet (before the first commit).
func repositoryFiles(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("git"); err == nil {
		out, err := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard").Output()
		if err == nil {
			var files []string
			for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if name == "" {
					continue
				}
				if _, statErr := os.Stat(name); statErr == nil {
					files = append(files, name)
				}
			}
			if len(files) > 0 {
				return files
			}
		}
	}
	var files []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	return files
}
