package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCargoPublisherDirectories(t *testing.T) {
	for _, private := range []bool{true, false} {
		t.Run(map[bool]string{true: "private", false: "public"}[private], func(t *testing.T) {
			const alice = "alice-test-token-123456"
			const bob = "bob-test-token-12345678"
			s := &registry{data: t.TempDir(), base: "http://localhost:8080", users: []user{{Nick: "alice", Token: alice}, {Nick: "bob", Token: bob}}, private: private, uploadLimit: maxUpload}
			archive := []byte("crate archive")
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
				meta, err := json.Marshal(map[string]string{"name": name, "vers": version, "publisher": "forged"})
				if err != nil {
					t.Fatal(err)
				}
				var body bytes.Buffer
				_ = binary.Write(&body, binary.LittleEndian, uint32(len(meta)))
				body.Write(meta)
				_ = binary.Write(&body, binary.LittleEndian, uint32(len(archive)))
				body.Write(archive)
				request("PUT", "/cargo/api/v1/crates/new", token, body.Bytes(), status)
			}
			publish("shared-crate", "1.0.0", bob, 200)
			publish("shared-crate", "2.0.0", alice, 200)
			publish("shared-crate", "2.0.0", bob, 409)
			publish("shared_crate", "3.0.0", alice, 409)
			for _, file := range []string{"metadata.json", "1.0.0.crate", "2.0.0.crate"} {
				if _, err := os.Stat(filepath.Join(s.data, "cargo", "bob", "shared_crate", file)); err != nil {
					t.Fatal(err)
				}
			}
			for _, dir := range []string{filepath.Join(s.data, "cargo", "shared_crate"), filepath.Join(s.data, "cargo", "alice", "shared_crate")} {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("unexpected package directory: %s: %v", dir, err)
				}
			}
			// Packages remain readable after their original publisher is removed.
			s = &registry{data: s.data, base: s.base, users: []user{{Nick: "alice", Token: alice}}, private: private, uploadLimit: maxUpload}
			index := "/cargo/" + indexPath("shared-crate")
			download := "/cargo/api/v1/crates/shared-crate/1.0.0/download"
			request("GET", index, alice, nil, 200)
			request("GET", "/cargo/"+indexPath("shared_crate"), alice, nil, 200)
			if w := request("GET", download, alice, nil, 200); !bytes.Equal(w.Body.Bytes(), archive) {
				t.Fatal("download returned different bytes")
			}
			request("DELETE", "/cargo/api/v1/crates/shared-crate/1.0.0/yank", alice, nil, 200)
			if w := request("GET", index, alice, nil, 200); !strings.Contains(w.Body.String(), `"yanked":true`) {
				t.Fatal("yank was not stored")
			}
			request("PUT", "/cargo/api/v1/crates/shared-crate/1.0.0/unyank", alice, nil, 200)
			if w := request("GET", index, alice, nil, 200); strings.Contains(w.Body.String(), `"yanked":true`) {
				t.Fatal("unyank was not stored")
			}
			status := 200
			if private {
				status = 401
			}
			request("GET", index, "", nil, status)
			request("GET", download, "", nil, status)
			catalog := request("GET", "/api/packages", alice, nil, 200)
			var result struct {
				Packages []packageSummary `json:"packages"`
			}
			if err := json.Unmarshal(catalog.Body.Bytes(), &result); err != nil || len(result.Packages) != 1 || result.Packages[0].Publisher != "bob" {
				t.Fatalf("catalog lost the package or its original publisher: %s", catalog.Body.String())
			}
			// Existing flat packages are not moved or assigned a guessed publisher.
			legacy := filepath.Join(s.data, "cargo", "old_crate")
			if err := save(filepath.Join(legacy, "metadata.json"), cratePackage{Name: "old-crate", Versions: []map[string]any{{"vers": "1.0.0"}}}); err != nil {
				t.Fatal(err)
			}
			if err := atomicWrite(filepath.Join(legacy, "1.0.0.crate"), archive); err != nil {
				t.Fatal(err)
			}
			publish("old-crate", "2.0.0", alice, 200)
			request("GET", "/cargo/"+indexPath("old-crate"), alice, nil, 200)
			request("GET", "/cargo/api/v1/crates/old-crate/1.0.0/download", alice, nil, 200)
			if _, err := os.Stat(filepath.Join(legacy, "2.0.0.crate")); err != nil {
				t.Fatal(err)
			}
			if err := save(filepath.Join(s.data, "cargo", "alice", "shared_crate", "metadata.json"), cratePackage{Name: "shared-crate"}); err != nil {
				t.Fatal(err)
			}
			request("GET", index, alice, nil, 500)
			request("GET", "/api/packages", alice, nil, 500)
		})
	}
}

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
			for _, pkg := range result.Packages {
				want := "alice"
				if pkg.Name == "shared" {
					want = "bob"
				}
				if pkg.Publisher != want {
					t.Fatalf("publisher changed after update or user removal: %s: %q", pkg.Name, pkg.Publisher)
				}
			}
			// Special nicknames must not escape the npm directory or collapse into it.
			for i, nick := range []string{".", "..", "../outside", "alice/admin"} {
				dir, err := s.packageDir("npm", "safe-"+string(rune('a'+i)), nick)
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
