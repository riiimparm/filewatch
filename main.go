package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type mapping struct {
	src, dst string
	isDir    bool
}

type rule struct {
	re     *regexp.Regexp
	repl   string
	global bool
}

// sed の s コマンド形式 s/{パターン}/{置換}/{フラグ} を解釈する。区切り文字は任意、フラグは g と i
func parseSed(expr string) (rule, error) {
	badForm := errors.New("置換は s/{パターン}/{置換}/{フラグ} の形式で指定してください")
	if len(expr) < 2 || expr[0] != 's' {
		return rule{}, badForm
	}
	parts := splitUnescaped(expr[2:], expr[1])
	if len(parts) != 3 {
		return rule{}, badForm
	}
	pattern, repl, flags := parts[0], parts[1], parts[2]

	var r rule
	for _, f := range flags {
		switch f {
		case 'g':
			r.global = true
		case 'i':
			pattern = "(?i)" + pattern
		default:
			return rule{}, fmt.Errorf("未対応のフラグ %q (g と i のみ)", f)
		}
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return rule{}, err
	}
	r.re, r.repl = re, sedReplacement(repl)
	return r, nil
}

// 区切り文字で分割する。\ でエスケープされた区切り文字は区切りとして扱わず、そのまま残す
func splitUnescaped(s string, delim byte) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == delim:
			cur.WriteByte(delim)
			i++
		case s[i] == '\\' && i+1 < len(s):
			cur.WriteByte(s[i])
			cur.WriteByte(s[i+1])
			i++
		case s[i] == delim:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(parts, cur.String())
}

// sed の後方参照(\1, &)を Go の regexp.Expand 形式へ変換する
func sedReplacement(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '$':
			b.WriteString("$$")
		case c == '&':
			b.WriteString("${0}")
		case c == '\\' && i+1 < len(s):
			i++
			switch n := s[i]; {
			case n >= '0' && n <= '9':
				b.WriteString("${" + string(n) + "}")
			case n == 'n':
				b.WriteByte('\n')
			case n == 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(n)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// sed と同じく行単位で適用する。g が無ければ各行の最初の1件だけ置換する
func (r rule) apply(b []byte) []byte {
	lines := bytes.Split(b, []byte("\n"))
	for i, line := range lines {
		if r.global {
			lines[i] = r.re.ReplaceAll(line, []byte(r.repl))
			continue
		}
		loc := r.re.FindSubmatchIndex(line)
		if loc == nil {
			continue
		}
		out := append([]byte{}, line[:loc[0]]...)
		out = r.re.Expand(out, []byte(r.repl), line, loc)
		lines[i] = append(out, line[loc[1]:]...)
	}
	return bytes.Join(lines, []byte("\n"))
}

func main() {
	cfgPath := flag.String("c", defaultConfigPath, "設定ファイル")
	flag.Parse()
	args := flag.Args()

	var mode string
	switch {
	case len(args) == 0:
	case len(args) == 1 && (args[0] == "sync" || args[0] == "replace"):
		mode = args[0]
	default:
		fmt.Fprintf(os.Stderr, "usage:\n  %s [-c 設定ファイル] [sync|replace]\n", os.Args[0])
		os.Exit(2)
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", *cfgPath, err)
		os.Exit(1)
	}

	fmt.Println("Go local file watcher")
	switch mode {
	case "sync":
		fmt.Println("入力形式: {監視元} {反映先}")
	case "replace":
		fmt.Println("入力形式: {監視対象} {sed置換}")
	default:
		fmt.Println("入力形式: sync {監視元} {反映先}")
		fmt.Println("          replace {監視対象} {sed置換}")
	}
	fmt.Println("終了: Ctrl+C")

	// サブコマンド指定が無ければ sync と replace を同時に動かす
	if mode == "" || mode == "sync" {
		for _, e := range cfg.Sync {
			if err := startEntry("sync", e.Src, e.Dst); err != nil {
				fmt.Printf("error: %s: %v\n", *cfgPath, err)
			}
		}
	}
	if mode == "" || mode == "replace" {
		for _, e := range cfg.Replace {
			if err := startEntry("replace", e.Target, e.Expr); err != nil {
				fmt.Printf("error: %s: %v\n", *cfgPath, err)
			}
		}
	}

	interactive(*cfgPath, cfg, mode)
}

// mode が空なら入力の先頭で種別を指定する。設定ファイルの各行と同じ形式
func interactive(cfgPath string, cfg config, mode string) {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print(">> ")
		if !scanner.Scan() {
			return
		}
		fields, err := splitFields(scanner.Text())
		if err != nil {
			fmt.Printf("error: %v\n", err)
			continue
		}
		kind := mode
		if kind == "" {
			if len(fields) == 0 {
				continue
			}
			kind, fields = fields[0], fields[1:]
		}
		if len(fields) != 2 {
			fmt.Println("error: 引数を2つ指定してください")
			continue
		}
		if err := startEntry(kind, fields[0], fields[1]); err != nil {
			fmt.Printf("error: %v\n", err)
			continue
		}

		if confirmSave(scanner) {
			if kind == "sync" {
				cfg.Sync = append(cfg.Sync, syncEntry{Src: fields[0], Dst: fields[1]})
			} else {
				cfg.Replace = append(cfg.Replace, replaceEntry{Target: fields[0], Expr: fields[1]})
			}
			save(cfgPath, cfg)
		}
	}
}

func startEntry(kind, first, second string) error {
	switch kind {
	case "sync":
		m, err := newMapping(first, second)
		if err != nil {
			return err
		}
		go watch(m)
		fmt.Printf("added watch: %s -> %s\n", m.src, m.dst)
	case "replace":
		target, err := filepath.Abs(first)
		if err != nil {
			return err
		}
		target = filepath.Clean(target)
		info, err := os.Stat(target)
		if err != nil {
			return fmt.Errorf("監視対象を確認できません: %w", err)
		}
		r, err := parseSed(second)
		if err != nil {
			return err
		}
		go watchReplace(target, info.IsDir(), r)
		fmt.Printf("added replace: %s %s\n", target, second)
	default:
		return fmt.Errorf("不明な種別 %q (sync か replace)", kind)
	}
	return nil
}

func confirmSave(scanner *bufio.Scanner) bool {
	fmt.Print("設定を保存しますか? [y/N]: ")
	if !scanner.Scan() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
	case "y", "yes":
		return true
	}
	return false
}

func save(cfgPath string, cfg config) {
	if err := saveConfig(cfgPath, cfg); err != nil {
		fmt.Printf("error: 保存に失敗: %v\n", err)
		return
	}
	fmt.Printf("saved: %s\n", cfgPath)
}

func newMapping(src, dst string) (mapping, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return mapping{}, err
	}
	dst, err = filepath.Abs(dst)
	if err != nil {
		return mapping{}, err
	}

	info, err := os.Stat(src)
	if err != nil {
		return mapping{}, fmt.Errorf("監視元を確認できません: %w", err)
	}
	if filepath.Clean(src) == filepath.Clean(dst) {
		return mapping{}, errors.New("監視元と反映先が同じです")
	}

	m := mapping{src: filepath.Clean(src), dst: filepath.Clean(dst), isDir: info.IsDir()}
	if m.isDir {
		rel, err := filepath.Rel(m.src, m.dst)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return mapping{}, errors.New("反映先を監視元ディレクトリの内部に置くことはできません")
		}
	}
	return m, nil
}

func watch(m mapping) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Printf("[error] watcher: %v\n", err)
		return
	}
	defer w.Close()

	if m.isDir {
		if err := syncDirectory(m.src, m.dst); err != nil {
			fmt.Printf("[error] initial sync: %v\n", err)
		}
		if err := addTree(w, m.src); err != nil {
			fmt.Printf("[error] watch setup: %v\n", err)
			return
		}
	} else {
		if err := copyFile(m.src, m.dst); err != nil {
			fmt.Printf("[error] initial copy: %v\n", err)
		}
		// ファイル自体ではなく親ディレクトリを監視する
		if err := w.Add(filepath.Dir(m.src)); err != nil {
			fmt.Printf("[error] watch setup: %v\n", err)
			return
		}
	}

	schedule := newScheduler()

	for {
		select {
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			path, err := filepath.Abs(event.Name)
			if err != nil {
				continue
			}
			path = filepath.Clean(path)

			if m.isDir {
				rel, err := filepath.Rel(m.src, path)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					continue
				}
				target := filepath.Join(m.dst, rel)

				if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					schedule(path, func() error { return os.RemoveAll(target) })
					continue
				}
				if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Chmod) == 0 {
					continue
				}

				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				if info.IsDir() {
					_ = os.MkdirAll(target, info.Mode().Perm())
					if event.Op&fsnotify.Create != 0 {
						if err := addTree(w, path); err != nil {
							fmt.Printf("[error] add watch %s: %v\n", path, err)
						}
					}
					continue
				}
				schedule(path, func() error { return copyFile(path, target) })
			} else {
				if path != m.src {
					continue
				}
				if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					schedule(path, func() error { return os.Remove(m.dst) })
					continue
				}
				if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Chmod) != 0 {
					schedule(path, func() error { return copyFile(m.src, m.dst) })
				}
			}

		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			fmt.Printf("[watch error] %v\n", err)
		}
	}
}

// Writeイベントは連続して発生することがあるため、短時間まとめて反映する
func newScheduler() func(string, func() error) {
	var mu sync.Mutex
	pending := make(map[string]*time.Timer)

	return func(key string, fn func() error) {
		mu.Lock()
		defer mu.Unlock()
		if timer, ok := pending[key]; ok {
			timer.Stop()
		}
		pending[key] = time.AfterFunc(120*time.Millisecond, func() {
			if err := fn(); err != nil && !os.IsNotExist(err) {
				fmt.Printf("[error] %s: %v\n", key, err)
			}
			mu.Lock()
			delete(pending, key)
			mu.Unlock()
		})
	}
}

func watchReplace(root string, isDir bool, r rule) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Printf("[error] watcher: %v\n", err)
		return
	}
	defer w.Close()

	if isDir {
		err = addTree(w, root)
	} else {
		// ファイル自体ではなく親ディレクトリを監視する
		err = w.Add(filepath.Dir(root))
	}
	if err != nil {
		fmt.Printf("[error] watch setup: %v\n", err)
		return
	}

	schedule := newScheduler()

	for {
		select {
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			path, err := filepath.Abs(event.Name)
			if err != nil {
				continue
			}
			path = filepath.Clean(path)

			if isDir {
				rel, err := filepath.Rel(root, path)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					continue
				}
			} else if path != root {
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}

			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if info.IsDir() {
				if isDir && event.Op&fsnotify.Create != 0 {
					if err := addTree(w, path); err != nil {
						fmt.Printf("[error] add watch %s: %v\n", path, err)
					}
				}
				continue
			}
			schedule(path, func() error { return replaceInFile(path, r) })

		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			fmt.Printf("[watch error] %v\n", err)
		}
	}
}

// 置換結果が元と同じなら書き戻さない。自分の書き込みで再度イベントが発生しても そこで止まる
func replaceInFile(path string, r rule) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out := r.apply(data)
	if bytes.Equal(out, data) {
		return nil
	}
	if err := atomicWrite(path, out, info.Mode().Perm()); err != nil {
		return err
	}
	fmt.Printf("replaced: %s\n", path)
	return nil
}

func atomicWrite(dst string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".sync-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

func addTree(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return w.Add(path)
		}
		return nil
	})
}

func syncDirectory(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".sync-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}
