package architecture_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPureContractAndInsightDependencies(t *testing.T) {
	for _, root := range []string{"../contracts", "../insight"} {
		files, _ := filepath.Glob(filepath.Join(root, "*.go"))
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range parsed.Imports {
				path, _ := strconv.Unquote(spec.Path.Value)
				for _, forbidden := range []string{"net/http", "os", "io/fs", "database/", "internal/session", "internal/history", "internal/presentation", "internal/obscontrol", "internal/delivery", "internal/gsi", "internal/capture", "internal/analytics"} {
					if path == forbidden || strings.HasPrefix(path, forbidden) {
						t.Errorf("%s imports forbidden dependency %s", file, path)
					}
				}
			}
		}
	}
}
