package telegram

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// userTextFiles hold every text the bot shows (docs/texts.md).
var userTextFiles = []string{"texts.go", filepath.Join("converter", "texts.go")}

// bannedInTexts are phrases that docs/texts.md rules out; compared in lower case.
var bannedInTexts = []struct{ phrase, why string }{
	{"most docs", "the Drive folder name comes from the config, via %s"},
	{"в google диске", "the preposition is «на Google Диске»"},
	{"выгруз", "the action is «загрузить на Google Диск»"},
	{"jpeg", "the format is written JPG"},
	{"drive", "the product is «Google Диск»"},
}

// constStrings returns the value of every string constant of a Go file, with
// concatenations folded, keyed by the constant's name.
func constStrings(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	require.NoError(t, err)
	out := map[string]string{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if v, ok := foldString(vs.Values[i]); ok {
					out[name.Name] = v
				}
			}
		}
	}
	return out
}

// foldString evaluates a string literal or a concatenation of them.
func foldString(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.ParenExpr:
		return foldString(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, lok := foldString(e.X)
		r, rok := foldString(e.Y)
		return l + r, lok && rok
	}
	return "", false
}

func TestTexts_followTheGlossary(t *testing.T) {
	for _, file := range userTextFiles {
		consts := constStrings(t, file)
		require.Greater(t, len(consts), 40, "%s: the constants are read, concatenations included", file)
		for name, text := range consts {
			lower := strings.ToLower(text)
			for _, b := range bannedInTexts {
				require.NotContains(t, lower, b.phrase, "%s in %s: %s", name, file, b.why)
			}
		}
	}
}

func TestTexts_noDoubleSpacesOrStrayFormatVerbs(t *testing.T) {
	for _, file := range userTextFiles {
		for name, text := range constStrings(t, file) {
			require.NotContains(t, text, "  ", "%s in %s: double space", name, file)
			require.NotContains(t, text, "%!", "%s in %s", name, file)
			require.NotContains(t, text, "%v", "%s in %s: use a typed verb", name, file)
			require.False(t, numberThenPlainSpace.MatchString(text), "%s in %s: a non-breaking space goes between a number and its unit", name, file)
		}
	}
}

// numberThenPlainSpace finds a number (or its %d) split from the unit by a plain
// space, which lets the unit wrap to the next line alone.
var numberThenPlainSpace = regexp.MustCompile(`(\d|%d) (МБ|КБ|стр\.)`)

// A user-facing string spelled out in code instead of texts.go skips the
// glossary check above, so the phrases are looked for in every literal of the
// adapter and the converter, not only in the text files.
func TestTexts_noBannedPhraseInAnyLiteral(t *testing.T) {
	for _, dir := range []string{".", "converter"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			require.NoError(t, err)
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				// Not "jpeg" or "drive": MIME types and URLs are not user texts.
				for _, b := range bannedInTexts[:3] {
					require.NotContains(t, strings.ToLower(v), b.phrase, "%s: %s", path, b.why)
				}
				return true
			})
		}
	}
}

// The Drive folder name is configuration. Outside the default's one declaration
// in platform/gdrive no code string may spell it out, or a renamed folder would
// still be shown under the old name.
func TestTexts_folderNameIsNotHardcoded(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := filepath.Join(root, "internal", "platform", "gdrive")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name != "." && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Dir(path) == allowed {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution) // comments are not parsed into the tree
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				require.NotContains(t, lit.Value, "Most Docs", path)
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
}
