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

type replacement struct {
	re   *regexp.Regexp
	with string
}

var (
	patMu    sync.RWMutex
	patterns []replacement
)

func addPattern(pattern, with string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	patMu.Lock()
	defer patMu.Unlock()
	patterns = append(patterns, replacement{re: re, with: with})
	return nil
}

// 反映先に書き出す内容へ登録済みの置換をすべて適用する
func applyPatterns(b []byte) []byte {
	patMu.RLock()
	defer patMu.RUnlock()
	for _, p := range patterns {
		b = p.re.ReplaceAll(b, []byte(p.with))
	}
	return b
}

func main() {
	cfgPath := flag.String("c", defaultConfigPath, "設定ファイル")
	flag.Parse()
	args := flag.Args()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", *cfgPath, err)
		os.Exit(1)
	}

	switch {
	case len(args) == 1 && args[0] == "sync":
		syncCommand(*cfgPath, cfg)
	case len(args) == 2 && args[0] == "replace":
		replaceCommand(*cfgPath, cfg, args[1])
	default:
		fmt.Fprintf(os.Stderr, "usage:\n  %s [-c 設定ファイル] sync\n  %s [-c 設定ファイル] replace {監視対象}\n", os.Args[0], os.Args[0])
		os.Exit(2)
	}
}

// sync: 監視元の変更を反映先へコピーする
func syncCommand(cfgPath string, cfg config) {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Go local file sync")
	fmt.Println("入力形式: {監視元} {反映先}")
	fmt.Println("終了: Ctrl+C")

	for _, e := range cfg.Sync {
		m, err := newMapping(e.Src, e.Dst)
		if err != nil {
			fmt.Printf("error: %s: %v\n", cfgPath, err)
			continue
		}
		go watch(m)
		fmt.Printf("added watch: %s -> %s\n", m.src, m.dst)
	}

	for {
		fmt.Print(">> ")
		if !scanner.Scan() {
			return
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			fmt.Println("error: 監視元と反映先を2つ指定してください")
			continue
		}

		m, err := newMapping(fields[0], fields[1])
		if err != nil {
			fmt.Printf("error: %v\n", err)
			continue
		}

		go watch(m)
		fmt.Printf("added watch: %s -> %s\n", m.src, m.dst)

		if confirmSave(scanner) {
			cfg.Sync = append(cfg.Sync, syncEntry{Src: fields[0], Dst: fields[1]})
			save(cfgPath, cfg)
		}
	}
}

// replace: 監視対象の変更を検知して、その場でパターン置換する
func replaceCommand(cfgPath string, cfg config, root string) {
	root, err := filepath.Abs(root)
	if err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		fmt.Printf("error: 監視対象を確認できません: %v\n", err)
		os.Exit(1)
	}

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Printf("Go local file replace: %s\n", root)
	fmt.Println("入力形式: {パターンマッチ} {置換}")
	fmt.Println("終了: Ctrl+C")

	for _, e := range cfg.Replace {
		if err := addPattern(e.Pattern, e.With); err != nil {
			fmt.Printf("error: %s: %v\n", cfgPath, err)
			continue
		}
		fmt.Printf("added replace pattern: %s -> %s\n", e.Pattern, e.With)
	}

	go watchReplace(root, info.IsDir())

	for {
		fmt.Print(">> ")
		if !scanner.Scan() {
			return
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			fmt.Println("error: パターンマッチと置換を2つ指定してください")
			continue
		}
		if err := addPattern(fields[0], fields[1]); err != nil {
			fmt.Printf("error: %v\n", err)
			continue
		}
		fmt.Println("added replace pattern")

		if confirmSave(scanner) {
			cfg.Replace = append(cfg.Replace, replaceEntry{Pattern: fields[0], With: fields[1]})
			save(cfgPath, cfg)
		}
	}
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

func watchReplace(root string, isDir bool) {
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
			schedule(path, func() error { return replaceInFile(path) })

		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			fmt.Printf("[watch error] %v\n", err)
		}
	}
}

// 置換結果が元と同じなら書き戻さない。自分の書き込みで再度イベントが発生しても そこで止まる
func replaceInFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out := applyPatterns(data)
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
