package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceInFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("hello foo bar foo"), 0644); err != nil {
		t.Fatal(err)
	}

	// パターン未登録なら無変更
	if err := replaceInFile(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "hello foo bar foo" {
		t.Fatalf("置換なしのはずが %q", b)
	}

	if err := addPattern("fo+", "BAZ"); err != nil {
		t.Fatal(err)
	}
	if err := replaceInFile(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "hello BAZ bar BAZ" {
		t.Fatalf("置換結果が %q", b)
	}

	// 2回目は差分なし = 書き戻さない（イベントループ防止）
	before, _ := os.Stat(path)
	if err := replaceInFile(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("差分がないのに書き戻している")
	}

	if addPattern("[", "x") == nil {
		t.Fatal("不正な正規表現がエラーにならない")
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
	cfg.Replace = append(cfg.Replace, replaceEntry{Pattern: "fo+", With: "BAZ"})
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
	if len(got.Replace) != 1 || got.Replace[0] != (replaceEntry{Pattern: "fo+", With: "BAZ"}) {
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
