package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type mapping struct {
	src, dst string
	isDir    bool
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Go local file sync")
	fmt.Println("入力形式: {監視元} {反映先}")
	fmt.Println("終了: Ctrl+C")

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
	}
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

	var mu sync.Mutex
	pending := make(map[string]*time.Timer)

	// Writeイベントは連続して発生することがあるため、短時間まとめて反映する
	schedule := func(srcPath string, fn func() error) {
		mu.Lock()
		defer mu.Unlock()
		if timer, ok := pending[srcPath]; ok {
			timer.Stop()
		}
		pending[srcPath] = time.AfterFunc(120*time.Millisecond, func() {
			if err := fn(); err != nil && !os.IsNotExist(err) {
				fmt.Printf("[error] %s: %v\n", srcPath, err)
			}
			mu.Lock()
			delete(pending, srcPath)
			mu.Unlock()
		})
	}

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
