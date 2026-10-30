package main

import (
	"bufio"
	"bytes"
	"errors"
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
		fields, err := splitFields(line)
		if err != nil {
			return cfg, fmt.Errorf("%d行目: %v", n, err)
		}
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

// 対話入力と設定ファイルで共通の分割。シェルと同じく ' や " で囲んだ範囲は空白を含めて1つの引数になる
func splitFields(line string) ([]string, error) {
	var fields []string
	var cur strings.Builder
	var quote byte
	var inField bool

	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, inField = c, true
		case c == ' ' || c == '\t':
			if inField {
				fields = append(fields, cur.String())
				cur.Reset()
				inField = false
			}
		default:
			cur.WriteByte(c)
			inField = true
		}
	}
	if quote != 0 {
		return nil, errors.New("クォートが閉じていません")
	}
	if inField {
		fields = append(fields, cur.String())
	}
	return fields, nil
}

func quoteField(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func saveConfig(path string, cfg config) error {
	var buf bytes.Buffer
	for _, e := range cfg.Sync {
		fmt.Fprintf(&buf, "sync %s %s\n", quoteField(e.Src), quoteField(e.Dst))
	}
	for _, e := range cfg.Replace {
		fmt.Fprintf(&buf, "replace %s %s\n", quoteField(e.Target), quoteField(e.Expr))
	}
	return atomicWrite(path, buf.Bytes(), 0644)
}
