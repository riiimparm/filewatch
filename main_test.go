package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSed(t *testing.T) {
	cases := []struct{ expr, in, want string }{
		{`s/foo/bar/g`, "foo foo", "bar bar"},
		{`s/foo/bar/`, "foo foo", "bar foo"},           // g 無しは行ごとに1件
		{`s/foo/bar/`, "foo foo\nfoo", "bar foo\nbar"}, // 行単位で適用する
		{`s/FOO/bar/gi`, "foo Foo", "bar bar"},
		{`s|a/b|x|g`, "a/b", "x"},       // 区切り文字は任意
		{`s/a\/b/x/g`, "a/b", "x"},      // エスケープした区切り文字
		{`s/(a)(b)/\2\1/g`, "ab", "ba"}, // 後方参照
		{`s/ab/[&]/g`, "ab", "[ab]"},    // & はマッチ全体
		{`s/a/$1/g`, "a", "$1"},         // $ はリテラル
	}
	for _, c := range cases {
		r, err := parseSed(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := string(r.apply([]byte(c.in))); got != c.want {
			t.Errorf("%s: %q -> %q (want %q)", c.expr, c.in, got, c.want)
		}
	}

	for _, expr := range []string{"foo", "s/foo", "s/foo/bar/x", "s/[/x/"} {
		if _, err := parseSed(expr); err == nil {
			t.Errorf("%q がエラーにならない", expr)
		}
	}
}

func TestReplaceInFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("hello foo bar foo"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := parseSed(`s/fo+/BAZ/g`)
	if err != nil {
		t.Fatal(err)
	}

	if err := replaceInFile(path, r); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "hello BAZ bar BAZ" {
		t.Fatalf("置換結果が %q", b)
	}

	// 2回目は差分なし = 書き戻さない（イベントループ防止）
	before, _ := os.Stat(path)
	if err := replaceInFile(path, r); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("差分がないのに書き戻している")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".filewatch")

	// 存在しない場合は空設定
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sync) != 0 || len(cfg.Replace) != 0 {
		t.Fatalf("空でない: %+v", cfg)
	}

	cfg.Sync = append(cfg.Sync, syncEntry{Src: "a", Dst: "b"})
	cfg.Replace = append(cfg.Replace, replaceEntry{Target: "./docs", Expr: "s/fo+/BAZ/g"})
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	got, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sync) != 1 || got.Sync[0] != (syncEntry{Src: "a", Dst: "b"}) {
		t.Fatalf("sync が復元できない: %+v", got.Sync)
	}
	if len(got.Replace) != 1 || got.Replace[0] != (replaceEntry{Target: "./docs", Expr: "s/fo+/BAZ/g"}) {
		t.Fatalf("replace が復元できない: %+v", got.Replace)
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".filewatch")
	if err := os.WriteFile(path, []byte("# コメント\n\nsync a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("引数不足がエラーにならない")
	}
}

func TestSplitFields(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{`a b`, []string{"a", "b"}},
		{`replace ./docs 's|a/b|x|g'`, []string{"replace", "./docs", "s|a/b|x|g"}},
		{`"a b" 's/x y/z/'`, []string{"a b", "s/x y/z/"}},
		{`a'b'c`, []string{"abc"}},
		{``, nil},
	}
	for _, c := range cases {
		got, err := splitFields(c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%q -> %q (want %q)", c.line, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q -> %q (want %q)", c.line, got, c.want)
			}
		}
	}
	if _, err := splitFields(`a 'b`); err == nil {
		t.Error("閉じていないクォートがエラーにならない")
	}
}

func TestConfigQuoting(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".filewatch")
	cfg := config{Replace: []replaceEntry{{Target: "my docs", Expr: "s/a b/c/g"}}}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Replace) != 1 || got.Replace[0] != cfg.Replace[0] {
		b, _ := os.ReadFile(path)
		t.Fatalf("空白を含む値が復元できない: %+v (file: %s)", got.Replace, b)
	}
}
