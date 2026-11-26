package deps

import (
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"database/sql"
	"github.com/pelletier/go-toml/v2"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	_ "modernc.org/sqlite"
	"path/filepath"
)

type smokeModel struct{}

func (smokeModel) Init() tea.Cmd                         { return nil }
func (m smokeModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (smokeModel) View() tea.View                        { return tea.NewView("ok") }

func TestDependencies(t *testing.T) {
	md := goldmark.New(goldmark.WithExtensions(extension.Table))
	doc := md.Parser().Parse(text.NewReader([]byte("| a | b |\n| - | - |\n")))
	found := false
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if n.Kind() == extast.KindTable {
			found = true
		}
		return ast.WalkContinue, nil
	})
	if !found {
		t.Fatal("goldmark table extension not active")
	}

	var cfg struct {
		Translation struct {
			Model string `toml:"model"`
		} `toml:"translation"`
	}
	if err := toml.Unmarshal([]byte("[translation]\nmodel = \"m\"\n"), &cfg); err != nil || cfg.Translation.Model != "m" {
		t.Fatalf("toml: %v %+v", err, cfg)
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE translations (cache_key TEXT PRIMARY KEY, translated TEXT NOT NULL, created_at TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}

	var m tea.Model = smokeModel{}
	if m.View().Content != "ok" {
		t.Fatal("bubbletea view")
	}
	vp := viewport.New(viewport.WithWidth(10), viewport.WithHeight(2))
	vp.SetContent("a\nb")
	if vp.TotalLineCount() != 2 {
		t.Fatalf("viewport lines = %d", vp.TotalLineCount())
	}
}
