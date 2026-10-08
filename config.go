package main

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
)

type user struct {
	Nick  string `toml:"nick"`
	Token string `toml:"token"`
}

type config struct {
	ListenAddr        string        `toml:"listen_addr"`
	PublicURL         string        `toml:"public_url"`
	DataDir           string        `toml:"data_dir"`
	Private           bool          `toml:"private"`
	MaxUploadMiB      int64         `toml:"max_upload_mib"`
	MaxHeaderBytes    int           `toml:"max_header_bytes"`
	ReadHeaderTimeout time.Duration `toml:"read_header_timeout"`
	ReadTimeout       time.Duration `toml:"read_timeout"`
	WriteTimeout      time.Duration `toml:"write_timeout"`
	IdleTimeout       time.Duration `toml:"idle_timeout"`
	Users             []user        `toml:"users"`
}

func defaultConfig() config {
	return config{
		ListenAddr: "127.0.0.1:8080", PublicURL: "http://localhost:8080", DataDir: "data", Private: true,
		MaxUploadMiB: maxUpload >> 20, MaxHeaderBytes: 16 << 10, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: time.Minute,
	}
}

func loadConfig(path string) (config, error) {
	c := defaultConfig()
	meta, err := toml.DecodeFile(path, &c)
	if err != nil {
		// Parser errors may quote a token. Never include the original error in logs.
		return c, fmt.Errorf("cannot read config %q or parse its TOML; check the file and field types", path)
	}
	if len(meta.Undecoded()) != 0 {
		return c, fmt.Errorf("config contains unknown settings; check the names against config.toml")
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		return c, fmt.Errorf("public_url must be an http(s) origin without a path, credentials, query, or fragment")
	}
	_, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return c, fmt.Errorf("listen_addr must contain a host and port")
	}
	if p, err := strconv.ParseUint(port, 10, 16); err != nil || p == 0 {
		return c, fmt.Errorf("listen_addr port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return c, fmt.Errorf("data_dir must not be empty")
	}
	if c.MaxUploadMiB < 1 || c.MaxUploadMiB > 1024 {
		return c, fmt.Errorf("max_upload_mib must be between 1 and 1024")
	}
	if c.MaxHeaderBytes < 1 || c.MaxHeaderBytes > 1<<20 {
		return c, fmt.Errorf("max_header_bytes must be between 1 and 1048576")
	}
	if c.ReadHeaderTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.IdleTimeout <= 0 {
		return c, fmt.Errorf("all HTTP timeouts must be positive durations")
	}
	if len(c.Users) == 0 {
		return c, fmt.Errorf("configure at least one [[users]] entry")
	}
	nicks, tokens := map[string]bool{}, map[string]bool{}
	for i, u := range c.Users {
		if u.Nick == "" || len(u.Nick) > 64 || strings.TrimSpace(u.Nick) != u.Nick || strings.IndexFunc(u.Nick, unicode.IsControl) >= 0 {
			return c, fmt.Errorf("users entry %d requires a nick of 1 to 64 characters without surrounding whitespace or control characters", i+1)
		}
		if len(u.Token) < 16 || strings.IndexFunc(u.Token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return c, fmt.Errorf("users entry %d requires a token of at least 16 printable ASCII characters without whitespace", i+1)
		}
		if nicks[u.Nick] || tokens[u.Token] {
			return c, fmt.Errorf("user nicknames and tokens must be unique")
		}
		nicks[u.Nick], tokens[u.Token] = true, true
	}
	return c, nil
}
