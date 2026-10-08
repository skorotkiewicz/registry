package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistry(t *testing.T) {
	const testToken = "test-token-at-least-16"
	s := &registry{data: t.TempDir(), base: "http://localhost:8080", users: []user{{Nick: "tester", Token: testToken}}, private: true, uploadLimit: maxUpload}
	request := func(method, path string, body []byte, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", token)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
		}
	}
	check(request("GET", "/healthz", nil, ""), 200)
	config := request("GET", "/cargo/config.json", nil, "")
	check(config, 200)
	if !strings.Contains(config.Body.String(), `"auth-required":true`) {
		t.Fatal("missing cargo authentication configuration")
	}
	check(request("GET", "/npm/example", nil, ""), 401)
	check(request("GET", "/cargo/ex/am/example", nil, "wrong"), 401)
	check(request("PUT", "/cargo/api/v1/crates/new", []byte{255, 255, 255, 255}, testToken), 400)
	check(request("PUT", "/npm/../escape", []byte(`{}`), "Bearer "+testToken), 400)

	archive := []byte("test archive bytes, clients check real archives in tests/smoke.sh")
	publish := func(name, version string) *httptest.ResponseRecorder {
		t.Helper()
		meta := []byte(`{"name":"` + name + `","vers":"` + version + `","deps":[{"name":"other","version_req":"^1","explicit_name_in_toml":"alias"}],"features":{"optional":["dep:alias"]}}`)
		var b bytes.Buffer
		_ = binary.Write(&b, binary.LittleEndian, uint32(len(meta)))
		b.Write(meta)
		_ = binary.Write(&b, binary.LittleEndian, uint32(len(archive)))
		b.Write(archive)
		return request("PUT", "/cargo/api/v1/crates/new", b.Bytes(), testToken)
	}
	check(publish("my-crate", "1.0.0"), 200)
	check(publish("my-crate", "1.0.0"), 409)
	check(publish("my_crate", "2.0.0"), 409)
	check(publish("my-crate", "1.0.0-01"), 400)
	index := request("GET", "/cargo/my/-c/my-crate", nil, testToken)
	check(index, 200)
	var entry map[string]any
	if err := json.Unmarshal(index.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	dep := entry["deps"].([]any)[0].(map[string]any)
	if dep["name"] != "alias" || dep["package"] != "other" || dep["req"] != "^1" {
		t.Fatalf("incorrect dependency translation: %v", dep)
	}
	check(request("DELETE", "/cargo/api/v1/crates/my-crate/1.0.0/yank", nil, testToken), 200)
	index = request("GET", "/cargo/my/-c/my-crate", nil, testToken)
	if !strings.Contains(index.Body.String(), `"yanked":true`) {
		t.Fatal("yank not stored")
	}
	check(request("PUT", "/cargo/api/v1/crates/my-crate/1.0.0/unyank", nil, testToken), 200)
	download := request("GET", "/cargo/api/v1/crates/my-crate/1.0.0/download", nil, testToken)
	check(download, 200)
	if !bytes.Equal(download.Body.Bytes(), archive) {
		t.Fatal("crate bytes changed")
	}

	npmPublish := func(version, tag string) *httptest.ResponseRecorder {
		t.Helper()
		name := "@mine/example"
		body := map[string]any{
			"name":         name,
			"versions":     map[string]any{version: map[string]any{"name": name, "version": version}},
			"dist-tags":    map[string]string{tag: version},
			"_attachments": map[string]any{name + "-" + version + ".tgz": map[string]any{"data": base64.StdEncoding.EncodeToString(archive), "length": len(archive)}},
		}
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return request("PUT", "/npm/@mine%2fexample", b, "Bearer "+testToken)
	}
	check(npmPublish("1.0.0", "latest"), 201)
	check(npmPublish("1.0.0", "latest"), 409)
	check(npmPublish("2.0.0-beta.1", "beta"), 201)
	check(npmPublish("2.0.0", "../bad"), 400)
	// A fresh server reads the same records without in-memory state.
	s = &registry{data: s.data, base: s.base, users: s.users, private: s.private, uploadLimit: s.uploadLimit}
	metadata := request("GET", "/npm/@mine%2fexample", nil, "Bearer "+testToken)
	check(metadata, 200)
	var pkg npmPackage
	if err := json.Unmarshal(metadata.Body.Bytes(), &pkg); err != nil {
		t.Fatal(err)
	}
	if len(pkg.Versions) != 2 || pkg.Tags["latest"] != "1.0.0" || pkg.Tags["beta"] != "2.0.0-beta.1" || pkg.Attachments != nil {
		t.Fatalf("bad persisted metadata: %+v", pkg)
	}
	download = request("GET", "/npm/@mine%2fexample/-/example-1.0.0.tgz", nil, "Bearer "+testToken)
	check(download, 200)
	if !bytes.Equal(download.Body.Bytes(), archive) {
		t.Fatal("npm bytes changed")
	}
	check(request("GET", "/npm/@mine%2fexample/-/../../metadata.json", nil, "Bearer "+testToken), 404)
	check(request("GET", "/npm/missing", nil, "Bearer "+testToken), 404)
	// Corrupt records must not be mistaken for a missing package and overwritten.
	dir, err := s.npmDir("broken", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	check(request("GET", "/npm/broken", nil, "Bearer "+testToken), 500)
}

func TestVersionFlag(t *testing.T) {
	if os.Getenv("REGISTRY_TEST_VERSION") == "1" {
		flag.CommandLine = flag.NewFlagSet("registry", flag.ExitOnError)
		os.Args = []string{"registry", "--version", "--config", "missing.toml"}
		version = "1.2.3"
		main()
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestVersionFlag$")
	cmd.Env = append(os.Environ(), "REGISTRY_TEST_VERSION=1")
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "registry 1.2.3\n" {
		t.Fatalf("version flag failed: %v, %s", err, output)
	}
}

func TestVersionValidation(t *testing.T) {
	for _, v := range []string{"0.0.0", "1.2.3-alpha.0+build.01", "1.0.0-0abc"} {
		if !validVersion(v) {
			t.Errorf("rejected %s", v)
		}
	}
	for _, v := range []string{"../x", "1.2", "01.2.3", "1.2.3-01", "1.2.3-a..b", "1.2.3/escape"} {
		if validVersion(v) {
			t.Errorf("accepted %s", v)
		}
	}
}
