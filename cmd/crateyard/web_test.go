package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWeb(t *testing.T) {
	const testToken = "web-test-token-123456"
	s := &registry{data: t.TempDir(), base: "https://packages.example.com", users: []user{{Nick: "tester", Token: testToken}}, private: true, uploadLimit: maxUpload}
	request := func(method, path, token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
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
	for path, contentType := range map[string]string{"/": "text/html", "/web/app.js": "text/javascript", "/web/style.css": "text/css"} {
		w := request("GET", path, "", 200)
		if !strings.HasPrefix(w.Header().Get("Content-Type"), contentType) || w.Header().Get("Content-Security-Policy") == "" || strings.Contains(w.Body.String(), testToken) {
			t.Fatalf("unsafe or missing asset: %s", path)
		}
		if request("HEAD", path, "", 200).Body.Len() != 0 {
			t.Fatal("HEAD sent a body")
		}
	}
	request("GET", "/api/packages", "", 401)
	request("GET", "/api/packages", "wrong", 401)
	request("GET", "/web/../main.go", testToken, 404)
	w := request("GET", "/api/packages", testToken, 200)
	if !strings.Contains(w.Body.String(), `"packages":[]`) {
		t.Fatalf("empty catalog is not an array: %s", w.Body.String())
	}
	// Interrupted publishes and unrelated files are not packages.
	if err := os.MkdirAll(s.path("cargo", "unfinished", ""), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.data, "cargo", ".upload-leftover"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := save(s.path("cargo", "example-crate", "metadata.json"), cratePackage{Name: "example-crate", Publisher: "crate-user", Versions: []map[string]any{{"vers": "1.0.0", "yanked": true}, {"vers": "2.0.0", "yanked": false}}}); err != nil {
		t.Fatal(err)
	}
	dir, err := s.npmDir("@my/example", "tester/admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := save(filepath.Join(dir, "metadata.json"), npmPackage{Name: "@my/example", Versions: map[string]map[string]any{"1.0.0": {"secret-extra": "not-for-catalog", "publisher": "forged"}, "2.0.0-beta.1": {}}, Tags: map[string]string{"latest": "1.0.0", "beta": "2.0.0-beta.1"}}); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/api/packages", testToken, 200)
	var catalog struct {
		URL      string           `json:"url"`
		Packages []packageSummary `json:"packages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.URL != s.base || len(catalog.Packages) != 2 || strings.Contains(w.Body.String(), "not-for-catalog") {
		t.Fatalf("bad catalog: %s", w.Body.String())
	}
	crate, npm := catalog.Packages[0], catalog.Packages[1]
	if crate.Kind != "cargo" || crate.Name != "example-crate" || crate.Publisher != "crate-user" || len(crate.Versions) != 2 || !crate.Versions[0].Yanked || crate.Versions[1].Yanked {
		t.Fatalf("incorrect crate summary: %+v", crate)
	}
	if npm.Kind != "npm" || npm.Name != "@my/example" || npm.Publisher != "tester/admin" || len(npm.Versions) != 2 || strings.Join(npm.Versions[0].Tags, ",") != "latest" || strings.Join(npm.Versions[1].Tags, ",") != "beta" {
		t.Fatalf("incorrect npm summary: %+v", npm)
	}
	if err := os.WriteFile(s.path("cargo", "example-crate", "metadata.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/packages", testToken, 500)
}
