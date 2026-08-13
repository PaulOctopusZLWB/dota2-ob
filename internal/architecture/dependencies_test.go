package architecture_test

import (
	"fmt"
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

type importRule struct {
	standard map[string]bool
	internal []string
}

// These are the frozen M0 ownership roots. Standard-library and internal
// imports both fail closed: adding a dependency requires an explicit rule
// change that is visible in review. Recursive subpackages inherit the rule.
var ownershipRules = map[string]importRule{
	"contracts": {
		standard: stringSet("bytes", "crypto/sha256", "encoding/hex", "encoding/json", "errors", "fmt", "io", "reflect", "regexp", "sort", "strconv", "strings", "time", "unicode", "unicode/utf16", "unicode/utf8"),
	},
	"liveprojection": {standard: stringSet(), internal: []string{"internal/contracts", "internal/session"}},
	"history":        {standard: stringSet(), internal: []string{"internal/contracts"}},
	"insight":        {standard: stringSet(), internal: []string{"internal/contracts"}},
	"policy":         {standard: stringSet(), internal: []string{"internal/contracts"}},
	"delivery": {
		standard: stringSet("context", "crypto/subtle", "encoding/json", "errors", "io", "mime", "net", "net/http", "net/url", "path", "strconv", "strings", "sync", "time"),
		internal: []string{"internal/contracts"},
	},
	"presentation": {standard: stringSet(), internal: []string{"internal/contracts"}},
	"obscontrol":   {standard: stringSet("context", "errors", "net", "net/url", "strconv", "strings"), internal: []string{"internal/contracts"}},
}

var policyCommitLogRule = importRule{
	standard: stringSet("bytes", "crypto/sha256", "encoding/binary", "encoding/hex", "errors", "fmt", "io", "os", "path/filepath", "sort", "strings", "sync"),
	internal: []string{"internal/contracts"},
}

func TestTrackImportGraphUsesExplicitDirections(t *testing.T) {
	for root, rule := range ownershipRules {
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
			fileRule := rule
			if root == "policy" && strings.HasPrefix(filepath.ToSlash(path), "../policy/commitlog/") {
				fileRule = policyCommitLogRule
			}
			for _, spec := range file.Imports {
				imp, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if err := checkImport(root, imp, fileRule); err != nil {
					t.Errorf("%s: %v", path, err)
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

func TestTrackImportGraphRejectsForbiddenDependencyFixtures(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"http", "net/http"},
		{"filesystem", "io/fs"},
		{"operating_system", "os"},
		{"database", "database/sql"},
		{"replay", modulePrefix + "internal/replay"},
		{"presentation", modulePrefix + "internal/presentation"},
		{"adapter", modulePrefix + "internal/capture"},
	}
	for root, rule := range ownershipRules {
		root, rule := root, rule
		for _, tc := range cases {
			if rule.standard[tc.path] {
				continue
			}
			if tc.path == modulePrefix+"internal/"+root {
				continue
			}
			t.Run(root+"/"+tc.name, func(t *testing.T) {
				if err := checkImport(root, tc.path, rule); err == nil {
					t.Fatalf("forbidden import %q accepted for internal/%s", tc.path, root)
				}
			})
		}
	}
}

func TestPolicyCommitLogAdapterHasNarrowFilesystemRule(t *testing.T) {
	for _, allowed := range []string{"crypto/sha256", "encoding/binary", "os", "path/filepath", modulePrefix + "internal/contracts"} {
		if err := checkImport("policy", allowed, policyCommitLogRule); err != nil {
			t.Fatalf("policy commit-log import %q rejected: %v", allowed, err)
		}
	}
	for _, forbidden := range []string{"net/http", "database/sql", modulePrefix + "internal/capture", modulePrefix + "internal/presentation"} {
		if err := checkImport("policy", forbidden, policyCommitLogRule); err == nil {
			t.Fatalf("policy commit-log forbidden import %q accepted", forbidden)
		}
	}
	if err := checkImport("policy", "os", ownershipRules["policy"]); err == nil {
		t.Fatal("pure policy core accepted filesystem import")
	}
}

func checkImport(root, imp string, rule importRule) error {
	if !strings.HasPrefix(imp, modulePrefix) {
		if !rule.standard[imp] {
			return fmt.Errorf("internal/%s imports unapproved external or standard package %s", root, imp)
		}
		return nil
	}
	rel := strings.TrimPrefix(imp, modulePrefix)
	if rel == "internal/"+root || strings.HasPrefix(rel, "internal/"+root+"/") {
		return nil
	}
	for _, prefix := range rule.internal {
		if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
			return nil
		}
	}
	return fmt.Errorf("internal/%s imports disallowed internal package %s", root, rel)
}

func stringSet(values ...string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}
