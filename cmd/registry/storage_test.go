package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNpmPublisherDirectories(t *testing.T) {
	for _, private := range []bool{true, false} {
		t.Run(map[bool]string{true: "private", false: "public"}[private], func(t *testing.T) {
			const alice = "alice-test-token-123456"
			const bob = "bob-test-token-12345678"
			s := &registry{data: t.TempDir(), base: "http://localhost:8080", users: []user{{Nick: "alice", Token: alice}, {Nick: "bob", Token: bob}}, private: private, uploadLimit: maxUpload}
			archive := []byte("archive bytes")
			request := func(method, path, token string, body []byte, status int) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != status {
					t.Fatalf("%s %s: %d, want %d: %s", method, path, w.Code, status, w.Body.String())
				}
				return w
			}
			publish := func(name, version, token string, status int) {
				t.Helper()
				body, err := json.Marshal(map[string]any{
					"name":         name,
					"versions":     map[string]any{version: map[string]string{"name": name, "version": version}},
					"dist-tags":    map[string]string{"latest": version},
					"_attachments": map[string]any{name + "-" + version + ".tgz": map[string]any{"data": base64.StdEncoding.EncodeToString(archive), "length": len(archive)}},
				})
				if err != nil {
					t.Fatal(err)
				}
				request("PUT", "/npm/"+url.PathEscape(name), token, body, status)
			}
			publish("shared", "1.0.0", bob, 201)
			publish("shared", "2.0.0", alice, 201)
			publish("shared", "2.0.0", bob, 409)
			publish("@scope/example", "1.0.0", alice, 201)
			publish("metadata.json", "1.0.0", alice, 201)
			for _, path := range []string{
				filepath.Join(s.data, "npm", "bob", "shared", "1.0.0.tgz"),
				filepath.Join(s.data, "npm", "bob", "shared", "2.0.0.tgz"),
				filepath.Join(s.data, "npm", "alice", "@scope%2Fexample", "metadata.json"),
				filepath.Join(s.data, "npm", "alice", "metadata.json", "1.0.0.tgz"),
			} {
				if _, err := os.Stat(path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(filepath.Join(s.data, "npm", "alice", "shared")); !os.IsNotExist(err) {
				t.Fatal("updating a package changed its publisher directory")
			}
			// Removed users' packages survive restart and are readable by other users.
			s = &registry{data: s.data, base: s.base, users: []user{{Nick: "alice", Token: alice}}, private: private, uploadLimit: maxUpload}
			for _, name := range []string{"shared", "@scope/example", "metadata.json"} {
				path := "/npm/" + url.PathEscape(name)
				request("GET", path, alice, nil, 200)
				download := path + "/-/" + npmFilename(name, "1.0.0")
				w := request("GET", download, alice, nil, 200)
				if !bytes.Equal(w.Body.Bytes(), archive) {
					t.Fatal("download returned different bytes")
				}
				status := 200
				if private {
					status = 401
				}
				request("GET", path, "", nil, status)
				request("GET", download, "", nil, status)
			}
			status := 404
			if private {
				status = 401
			}
			request("GET", "/npm/alice", "", nil, status)
			request("GET", "/npm/missing", "", nil, status)
			catalog := request("GET", "/api/packages", alice, nil, 200)
			var result struct {
				Packages []packageSummary `json:"packages"`
			}
			if err := json.Unmarshal(catalog.Body.Bytes(), &result); err != nil || len(result.Packages) != 3 {
				t.Fatalf("catalog missed packages: %s", catalog.Body.String())
			}
			// Special nicknames must not escape the npm directory or collapse into it.
			for i, nick := range []string{".", "..", "../outside", "alice/admin"} {
				dir, err := s.npmDir("safe-"+string(rune('a'+i)), nick)
				if err != nil {
					t.Fatal(err)
				}
				rel, err := filepath.Rel(filepath.Join(s.data, "npm"), dir)
				if err != nil || (rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator))) || len(strings.Split(rel, string(os.PathSeparator))) != 2 {
					t.Fatalf("unsafe nickname directory: %s", dir)
				}
			}
			// Duplicate global names are an error, not a token-dependent download.
			if err := save(filepath.Join(s.data, "npm", "alice", "shared", "metadata.json"), npmPackage{Name: "shared"}); err != nil {
				t.Fatal(err)
			}
			request("GET", "/npm/shared", alice, nil, 500)
			request("GET", "/api/packages", alice, nil, 500)
		})
	}
}
