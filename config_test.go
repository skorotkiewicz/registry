package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfig(t *testing.T) {
	const users = "\n[[users]]\nnick = 'alice'\ntoken = 'alice-test-token-123456'\n[[users]]\nnick = 'bob'\ntoken = 'bob-test-token-12345678'\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	read := func(text string) (config, error) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		return loadConfig(path)
	}
	c, err := read(users)
	if err != nil || !c.Private || c.ListenAddr != "127.0.0.1:8080" || c.MaxUploadMiB != 64 || len(c.Users) != 2 {
		t.Fatalf("bad defaults: %v", err)
	}
	c, err = read(`listen_addr = "0.0.0.0:9090"
public_url = "https://packages.example.com/"
data_dir = "/srv/packages"
private = false
max_upload_mib = 8
max_header_bytes = 8192
read_header_timeout = "3s"
read_timeout = "30s"
write_timeout = "45s"
idle_timeout = "20s"
` + users)
	if err != nil {
		t.Fatal(err)
	}
	if c.Private || c.ListenAddr != "0.0.0.0:9090" || c.PublicURL != "https://packages.example.com" || c.DataDir != "/srv/packages" || c.MaxUploadMiB != 8 || c.MaxHeaderBytes != 8192 || c.ReadHeaderTimeout != 3*time.Second || c.ReadTimeout != 30*time.Second || c.WriteTimeout != 45*time.Second || c.IdleTimeout != 20*time.Second {
		t.Fatal("settings were not decoded")
	}
	for _, text := range []string{
		"", "private = 'false'" + users, "privat = false" + users,
		"public_url = 'file:///tmp'" + users, "public_url = 'https://host/path'" + users,
		"public_url = 'https://user:password@host'" + users, "public_url = 'https://host?'" + users,
		"listen_addr = 'localhost'" + users, "listen_addr = 'localhost:65536'" + users,
		"data_dir = ''" + users, "max_upload_mib = 0" + users,
		"max_upload_mib = 1025" + users, "max_header_bytes = 0" + users,
		"read_timeout = 'bad'" + users, "idle_timeout = '0s'" + users,
		strings.Replace(users, "nick = 'bob'", "nick = 'alice'", 1),
		strings.Replace(users, "bob-test-token-12345678", "alice-test-token-123456", 1),
		strings.Replace(users, "alice-test-token-123456", "short", 1),
		strings.Replace(users, "alice-test-token-123456", "", 1),
		strings.Replace(users, "alice-test-token-123456", "token with whitespace", 1),
		strings.Replace(users, "nick = 'alice'", "nick = ''", 1),
		strings.Replace(users, "nick = 'alice'", "name = 'alice'", 1),
	} {
		if _, err := read(text); err == nil {
			t.Fatalf("accepted invalid config: %s", text)
		}
	}
	const secret = "test-secret-must-not-appear-in-errors"
	_, err = read("[[users]]\nnick = 'alice'\ntoken = '" + secret + "' extra")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("config parser leaks secrets or accepts invalid TOML")
	}
	if _, err := loadConfig(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("missing config was ignored")
	}
}

func TestAccessModes(t *testing.T) {
	for _, private := range []bool{true, false} {
		t.Run(fmt.Sprintf("private=%t", private), func(t *testing.T) {
			s := &registry{data: t.TempDir(), base: "http://localhost:8080", users: []user{{Nick: "alice", Token: "alice-test-token-123456"}, {Nick: "bob", Token: "bob-test-token-12345678"}}, private: private, uploadLimit: 1 << 20}
			if err := save(s.path("cargo", "example", "metadata.json"), cratePackage{Name: "example", Versions: []map[string]any{{"vers": "1.0.0", "yanked": false}}}); err != nil {
				t.Fatal(err)
			}
			if err := atomicWrite(s.path("cargo", "example", "1.0.0.crate"), []byte("archive")); err != nil {
				t.Fatal(err)
			}
			if err := save(s.path("npm", "@my/example", "metadata.json"), npmPackage{Name: "@my/example", Versions: map[string]map[string]any{"1.0.0": {}}, Tags: map[string]string{"latest": "1.0.0"}}); err != nil {
				t.Fatal(err)
			}
			if err := atomicWrite(s.path("npm", "@my/example", "1.0.0.tgz"), []byte("archive")); err != nil {
				t.Fatal(err)
			}
			request := func(method, path, token string, body []byte, want int) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				if token != "" {
					r.Header.Set("Authorization", token)
				}
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("%s %s: %d, want %d: %s", method, path, w.Code, want, w.Body.String())
				}
				return w
			}
			cfg := request("GET", "/cargo/config.json", "", nil, 200)
			var m map[string]any
			if err := json.Unmarshal(cfg.Body.Bytes(), &m); err != nil || m["auth-required"] != private {
				t.Fatal("Cargo authentication mode does not match config")
			}
			for _, method := range []string{"GET", "HEAD"} {
				for _, path := range []string{"/api/packages", "/cargo/ex/am/example", "/cargo/api/v1/crates/example/1.0.0/download", "/npm/@my%2fexample", "/npm/@my%2fexample/-/example-1.0.0.tgz"} {
					// The catalog has no HEAD endpoint; test its GET endpoint only.
					if method == "HEAD" && path == "/api/packages" {
						continue
					}
					status := 200
					if private {
						status = 401
					}
					request(method, path, "", nil, status)
					request(method, path, "Bearer invalid", nil, status)
					for _, u := range s.users {
						request(method, path, "Bearer "+u.Token, nil, 200)
					}
				}
			}
			for _, u := range s.users {
				w := request("GET", "/npm/-/whoami", "Bearer "+u.Token, nil, 200)
				if !strings.Contains(w.Body.String(), `"username":"`+u.Nick+`"`) {
					t.Fatal("whoami does not identify the token owner")
				}
			}
			request("GET", "/npm/-/whoami", "", nil, 401)
			request("GET", "/npm/-/whoami", "Bearer invalid", nil, 401)
			for _, token := range []string{"", "invalid"} {
				request("PUT", "/cargo/api/v1/crates/new", token, nil, 401)
				request("PUT", "/npm/@my%2fexample", token, nil, 401)
				request("DELETE", "/cargo/api/v1/crates/example/1.0.0/yank", token, nil, 401)
				request("PUT", "/cargo/api/v1/crates/example/1.0.0/unyank", token, nil, 401)
				request("POST", "/npm/-/npm/v1/security/audits/quick", token, nil, 401)
			}
			// Both users can mutate; there are no per-package ownership rules.
			request("DELETE", "/cargo/api/v1/crates/example/1.0.0/yank", s.users[0].Token, nil, 200)
			request("PUT", "/cargo/api/v1/crates/example/1.0.0/unyank", s.users[1].Token, nil, 200)
			request("PUT", "/cargo/api/v1/crates/new", s.users[0].Token, bytes.Repeat([]byte{'x'}, (1<<20)+1), 413)
			removed := s.users[0].Token
			s.users = s.users[1:]
			request("GET", "/npm/-/whoami", removed, nil, 401)
			request("PUT", "/cargo/api/v1/crates/new", removed, nil, 401)
		})
	}
}
