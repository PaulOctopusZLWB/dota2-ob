package architecture_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePrefix = "github.com/PaulOctopusZLWB/dota2-ob/"

// These are the frozen M0 ownership roots. Every root must exist and contain
// examined production Go source; recursive subpackages inherit the same rule.
func TestTrackImportGraphUsesExplicitDirections(t *testing.T) {
	rules := map[string][]string{
		"contracts":      {},
		"liveprojection": {"internal/contracts", "internal/session"},
		"history":        {"internal/contracts"},
		"insight":        {"internal/contracts"},
		"policy":         {"internal/contracts"},
		"delivery":       {"internal/contracts"},
		"presentation":   {"internal/contracts"},
		"obscontrol":     {"internal/contracts"},
	}
	for root, allowed := range rules {
		rootPath := filepath.Join("..", root)
		info, err := os.Stat(rootPath)
		if err != nil || !info.IsDir() {
			t.Errorf("required ownership root internal/%s is absent", root)
			continue
		}
		examined := 0
		err = filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			examined++
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				imp, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if !strings.HasPrefix(imp, modulePrefix) {
					if strings.Contains(strings.Split(imp, "/")[0], ".") {
						t.Errorf("internal/%s imports unapproved third-party package %s", root, imp)
					}
					continue
				}
				rel := strings.TrimPrefix(imp, modulePrefix)
				if strings.HasPrefix(rel, "internal/"+root+"/") || rel == "internal/"+root {
					continue
				}
				ok := false
				for _, prefix := range allowed {
					if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
						ok = true
						break
					}
				}
				if !ok {
					t.Errorf("internal/%s imports disallowed internal package %s", root, rel)
				}
			}
			return nil
		})
		if err != nil {
			t.Errorf("walk internal/%s: %v", root, err)
		}
		if examined == 0 {
			t.Errorf("required ownership root internal/%s has no examined production Go files", root)
		}
	}
}
