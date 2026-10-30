package main

import (
	"bytes"
	"os"

	"github.com/BurntSushi/toml"
)

const defaultConfigPath = ".filewatch.toml"

type config struct {
	Sync    []syncEntry    `toml:"sync"`
	Replace []replaceEntry `toml:"replace"`
}

type syncEntry struct {
	Src string `toml:"src"`
	Dst string `toml:"dst"`
}

type replaceEntry struct {
	Pattern string `toml:"pattern"`
	With    string `toml:"with"`
}

// 設定ファイルが無い場合は空の設定として扱う
func loadConfig(path string) (config, error) {
	var cfg config
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	return cfg, toml.Unmarshal(b, &cfg)
}

func saveConfig(path string, cfg config) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	return atomicWrite(path, buf.Bytes(), 0644)
}
