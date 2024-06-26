package config

import (
	"os"
	"path/filepath"

	"go.uber.org/atomic"

	"github.com/BurntSushi/toml"
	"github.com/trim21/errgo"
)

type Application struct {
	DownloadDir     string `json:"download-dir"`
	Crypto          string `json:"crypto"`
	MaxHTTPParallel int    `json:"max-http-parallel"`
	P2PPort         uint16 `json:"p2p-port"`
	NumWant         uint16 `json:"num-want"`
	// hard global connection limit
	GlobalConnectionLimit uint16      `json:"global-connections-limit"`
	Fallocate             atomic.Bool `json:"fallocate"`
}

type Config struct {
	App Application `toml:"application"`
}

func LoadFromFile(path string) (Config, error) {
	var cfg = Config{
		App: Application{MaxHTTPParallel: 100, GlobalConnectionLimit: 50},
	}

	if _, err := toml.DecodeFile(path, &cfg); err != nil && !os.IsNotExist(err) {
		return cfg, errgo.Wrap(err, "failed to parse config file")
	}

	if cfg.App.DownloadDir == "" {
		hd, err := os.UserHomeDir()
		if err != nil {
			panic(errgo.Wrap(err, "failed to get user homedir"))
		}

		cfg.App.DownloadDir = filepath.Join(hd, "downloads")
	}

	return cfg, nil
}
