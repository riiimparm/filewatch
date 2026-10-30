package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
)

const defaultConfigPath = ".filewatch"

type config struct {
	Sync    []syncEntry
	Replace []replaceEntry
}

type syncEntry struct {
	Src string
	Dst string
}

type replaceEntry struct {
	Target string
	Expr   string
}

// 設定ファイルは対話入力と同じ形式を1行ずつ並べたもの:
//
//	sync    {監視元} {反映先}
//	replace {監視対象} {sed置換}
//
// ponytail: 対話入力と同じく空白区切り。空白を含むパスやパターンは扱えない
func loadConfig(path string) (config, error) {
	var cfg config
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return cfg, fmt.Errorf("%d行目: 「{sync|replace} {第1引数} {第2引数}」の形式で書いてください", n)
		}
		switch fields[0] {
		case "sync":
			cfg.Sync = append(cfg.Sync, syncEntry{Src: fields[1], Dst: fields[2]})
		case "replace":
			cfg.Replace = append(cfg.Replace, replaceEntry{Target: fields[1], Expr: fields[2]})
		default:
			return cfg, fmt.Errorf("%d行目: 不明な種別 %q", n, fields[0])
		}
	}
	return cfg, scanner.Err()
}

func saveConfig(path string, cfg config) error {
	var buf bytes.Buffer
	for _, e := range cfg.Sync {
		fmt.Fprintf(&buf, "sync %s %s\n", e.Src, e.Dst)
	}
	for _, e := range cfg.Replace {
		fmt.Fprintf(&buf, "replace %s %s\n", e.Target, e.Expr)
	}
	return atomicWrite(path, buf.Bytes(), 0644)
}
