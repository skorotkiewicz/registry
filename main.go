package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const maxUpload = 64 << 20

var (
	crateName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	npmName   = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	semver    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
)

type registry struct {
	data, base  string
	users       []user
	private     bool
	uploadLimit int64
	mu          sync.Mutex
}

type npmPackage struct {
	Name        string                    `json:"name"`
	Versions    map[string]map[string]any `json:"versions"`
	Tags        map[string]string         `json:"dist-tags"`
	Attachments map[string]struct {
		Data   string `json:"data"`
		Length int    `json:"length"`
	} `json:"_attachments,omitempty"`
}

type cratePackage struct {
	Name     string           `json:"name"`
	Versions []map[string]any `json:"versions"`
}

func main() {
	path := flag.String("config", "config.toml", "path to server TOML configuration")
	flag.Parse()
	c, err := loadConfig(*path)
	if err != nil {
		log.Fatal(err)
	}
	r := &registry{data: c.DataDir, base: c.PublicURL, users: c.Users, private: c.Private, uploadLimit: c.MaxUploadMiB << 20}
	if err := os.MkdirAll(r.data, 0700); err != nil {
		log.Fatal(err)
	}
	s := &http.Server{Addr: c.ListenAddr, Handler: r, ReadHeaderTimeout: c.ReadHeaderTimeout, ReadTimeout: c.ReadTimeout, WriteTimeout: c.WriteTimeout, IdleTimeout: c.IdleTimeout, MaxHeaderBytes: c.MaxHeaderBytes}
	log.Printf("registry listening on %s, public URL %s, private=%t", s.Addr, r.base, r.private)
	log.Fatal(s.ListenAndServe())
}

func (s *registry) nickname(token string) string {
	if token == "" {
		return ""
	}
	nick := ""
	for _, u := range s.users {
		if subtle.ConstantTimeCompare([]byte(token), []byte(u.Token)) == 1 {
			nick = u.Nick
		}
	}
	return nick
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, message string) {
	// Both clients understand their own error field.
	reply(w, status, map[string]any{"error": message, "errors": []map[string]string{{"detail": message}}})
}

func (s *registry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if (r.Method == "GET" || r.Method == "HEAD") && serveWeb(w, r) {
		return
	}
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		reply(w, 200, map[string]bool{"ok": true})
		return
	}
	if r.Method == "GET" && r.URL.Path == "/cargo/config.json" {
		reply(w, 200, map[string]any{"dl": s.base + "/cargo/api/v1/crates/{crate}/{version}/download", "api": s.base + "/cargo", "auth-required": s.private})
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	publicRead := !s.private && (r.Method == "GET" || r.Method == "HEAD") && r.URL.Path != "/npm/-/whoami"
	if s.nickname(token) == "" && !publicRead {
		w.Header().Set("WWW-Authenticate", `Bearer realm="registry"`)
		fail(w, 401, "valid registry token required")
		return
	}
	// ponytail: one process and a global lock serialize requests; use a database for multiple writers or higher throughput.
	s.mu.Lock()
	defer s.mu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, s.uploadLimit)
	var err error
	switch {
	case r.URL.Path == "/api/packages" && r.Method == "GET":
		err = s.catalog(w)
	case strings.HasPrefix(r.URL.Path, "/cargo/"):
		err = s.cargo(w, r)
	case strings.HasPrefix(r.URL.Path, "/npm/"):
		err = s.npm(w, r)
	default:
		fail(w, 404, "route not found")
	}
	if err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			fail(w, 413, fmt.Sprintf("upload exceeds %d MiB", s.uploadLimit>>20))
			return
		}
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		fail(w, 500, "storage or request error")
	}
}

func (s *registry) path(kind, name, file string) string {
	if kind == "cargo" {
		name = strings.ReplaceAll(name, "-", "_")
	}
	return filepath.Join(s.data, kind, url.PathEscape(name), file)
}

func load(path string, value any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}

// Commit the archive before metadata. An interrupted publish can leave an unused
// archive, but never an index entry pointing at an incomplete upload.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".upload-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // Only removes this request's temporary file.
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func save(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicWrite(path, b)
}

func indexPath(name string) string {
	switch len(name) {
	case 1:
		return "1/" + name
	case 2:
		return "2/" + name
	case 3:
		return "3/" + name[:1] + "/" + name
	default:
		return name[:2] + "/" + name[2:4] + "/" + name
	}
}

func validVersion(v string) bool {
	if len(v) > 128 || !semver.MatchString(v) {
		return false
	}
	pre := strings.SplitN(strings.SplitN(v, "+", 2)[0], "-", 2)
	if len(pre) == 2 {
		for _, part := range strings.Split(pre[1], ".") {
			if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
				return false
			}
		}
	}
	return true
}

func (s *registry) cargo(w http.ResponseWriter, r *http.Request) error {
	p := strings.TrimPrefix(r.URL.Path, "/cargo/")
	if p == "api/v1/crates/new" && r.Method == "PUT" {
		return s.publishCrate(w, r)
	}
	parts := strings.Split(p, "/")
	if len(parts) == 6 && strings.Join(parts[:3], "/") == "api/v1/crates" && crateName.MatchString(parts[3]) && validVersion(parts[4]) {
		name, version, action := parts[3], parts[4], parts[5]
		var pkg cratePackage
		if err := load(s.path("cargo", name, "metadata.json"), &pkg); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fail(w, 404, "crate not found")
				return nil
			}
			return err
		}
		for _, entry := range pkg.Versions {
			if entry["vers"] != version {
				continue
			}
			if action == "download" && (r.Method == "GET" || r.Method == "HEAD") {
				http.ServeFile(w, r, s.path("cargo", name, version+".crate"))
				return nil
			}
			if (action == "yank" && r.Method == "DELETE") || (action == "unyank" && r.Method == "PUT") {
				entry["yanked"] = action == "yank"
				if err := save(s.path("cargo", name, "metadata.json"), pkg); err != nil {
					return err
				}
				reply(w, 200, map[string]bool{"ok": true})
				return nil
			}
		}
	} else if r.Method == "GET" || r.Method == "HEAD" {
		name := parts[len(parts)-1]
		if crateName.MatchString(name) && p == indexPath(name) {
			var pkg cratePackage
			if err := load(s.path("cargo", name, "metadata.json"), &pkg); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fail(w, 404, "crate not found")
					return nil
				}
				return err
			}
			w.Header().Set("Content-Type", "text/plain")
			if r.Method != "HEAD" {
				for _, entry := range pkg.Versions {
					if err := json.NewEncoder(w).Encode(entry); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	fail(w, 404, "cargo route or version not found")
	return nil
}

func (s *registry) publishCrate(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	reader := bytes.NewReader(body)
	chunk := func() ([]byte, error) {
		var size uint32
		if err := binary.Read(reader, binary.LittleEndian, &size); err != nil {
			return nil, err
		}
		if uint64(size) > uint64(reader.Len()) {
			return nil, io.ErrUnexpectedEOF
		}
		b := make([]byte, int(size))
		_, err := io.ReadFull(reader, b)
		return b, err
	}
	meta, err := chunk()
	if err != nil {
		fail(w, 400, "invalid cargo upload framing")
		return nil
	}
	archive, err := chunk()
	if err != nil || reader.Len() != 0 || len(archive) == 0 {
		fail(w, 400, "invalid cargo archive framing")
		return nil
	}
	var m struct {
		Name        string              `json:"name"`
		Version     string              `json:"vers"`
		Deps        []map[string]any    `json:"deps"`
		Features    map[string][]string `json:"features"`
		Links       *string             `json:"links"`
		RustVersion *string             `json:"rust_version"`
	}
	if json.Unmarshal(meta, &m) != nil || !crateName.MatchString(m.Name) || !validVersion(m.Version) {
		fail(w, 400, "invalid crate name, version, or metadata")
		return nil
	}
	if m.Deps == nil {
		m.Deps = []map[string]any{}
	}
	for _, dep := range m.Deps {
		name, ok := dep["name"].(string)
		req, reqOK := dep["version_req"].(string)
		if !ok || !crateName.MatchString(name) || !reqOK || req == "" {
			fail(w, 400, "invalid dependency")
			return nil
		}
		dep["req"] = req
		delete(dep, "version_req")
		if alias, ok := dep["explicit_name_in_toml"].(string); ok && alias != "" {
			if !crateName.MatchString(alias) {
				fail(w, 400, "invalid dependency alias")
				return nil
			}
			dep["name"], dep["package"] = alias, name
		}
		delete(dep, "explicit_name_in_toml")
	}
	if m.Features == nil {
		m.Features = map[string][]string{}
	}
	path := s.path("cargo", m.Name, "metadata.json")
	pkg := cratePackage{Name: m.Name}
	if err := load(path, &pkg); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if pkg.Name != m.Name {
		fail(w, 409, "crate name collides with an existing name")
		return nil
	}
	for _, e := range pkg.Versions {
		if e["vers"] == m.Version {
			fail(w, 409, "version already published")
			return nil
		}
	}
	hash := sha256.Sum256(archive)
	entry := map[string]any{"name": m.Name, "vers": m.Version, "deps": m.Deps, "cksum": hex.EncodeToString(hash[:]), "features": map[string]any{}, "features2": m.Features, "v": 2, "yanked": false, "links": m.Links, "rust_version": m.RustVersion}
	if err := atomicWrite(s.path("cargo", m.Name, m.Version+".crate"), archive); err != nil {
		return err
	}
	pkg.Versions = append(pkg.Versions, entry)
	if err := save(path, pkg); err != nil {
		return err
	}
	reply(w, 200, map[string]any{"warnings": map[string]any{"other": []string{}}})
	return nil
}

func (s *registry) npm(w http.ResponseWriter, r *http.Request) error {
	p := strings.TrimPrefix(r.URL.Path, "/npm/")
	if p == "-/ping" && r.Method == "GET" {
		reply(w, 200, map[string]bool{"ok": true})
		return nil
	}
	if p == "-/whoami" && r.Method == "GET" {
		reply(w, 200, map[string]string{"username": s.nickname(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))})
		return nil
	}
	if strings.HasPrefix(p, "-/npm/v1/security/") && r.Method == "POST" {
		fail(w, 501, "audit is not supported; use --no-audit")
		return nil
	}
	name := p
	file := ""
	if pieces := strings.SplitN(p, "/-/", 2); len(pieces) == 2 {
		name, file = pieces[0], pieces[1]
	}
	if !npmName.MatchString(name) || len(name) > 214 {
		fail(w, 400, "invalid npm package name")
		return nil
	}
	path := s.path("npm", name, "metadata.json")
	pkg := npmPackage{Name: name, Versions: map[string]map[string]any{}, Tags: map[string]string{}}
	if err := load(path, &pkg); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if pkg.Name != name || pkg.Versions == nil || pkg.Tags == nil {
		return fmt.Errorf("invalid stored npm metadata for %s", name)
	}
	if file != "" && (r.Method == "GET" || r.Method == "HEAD") {
		for version := range pkg.Versions {
			if file == npmFilename(name, version) {
				http.ServeFile(w, r, s.path("npm", name, version+".tgz"))
				return nil
			}
		}
		fail(w, 404, "tarball not found")
		return nil
	}
	if file == "" && (r.Method == "GET" || r.Method == "HEAD") {
		if len(pkg.Versions) == 0 {
			fail(w, 404, "package not found")
			return nil
		}
		reply(w, 200, pkg)
		return nil
	}
	if file != "" || r.Method != "PUT" {
		fail(w, 405, "only publish, metadata, and download are supported")
		return nil
	}
	var incoming npmPackage
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			return err
		}
		fail(w, 400, "invalid npm publish JSON")
		return nil
	}
	if incoming.Name != name || len(incoming.Versions) != 1 || len(incoming.Attachments) != 1 {
		fail(w, 400, "publish one version and one tarball at a time")
		return nil
	}
	for version, metadata := range incoming.Versions {
		if !validVersion(version) || metadata == nil || metadata["name"] != name || metadata["version"] != version {
			fail(w, 400, "invalid npm version metadata")
			return nil
		}
		if _, exists := pkg.Versions[version]; exists {
			fail(w, 409, "version already published")
			return nil
		}
		attachment, exists := incoming.Attachments[name+"-"+version+".tgz"]
		archive, err := base64.StdEncoding.Strict().DecodeString(attachment.Data)
		if !exists || err != nil || len(archive) == 0 || attachment.Length != len(archive) {
			fail(w, 400, "invalid npm tarball")
			return nil
		}
		for tag, target := range incoming.Tags {
			if tag == "" || len(tag) > 128 || strings.ContainsAny(tag, "/\\ \t\r\n") {
				fail(w, 400, "invalid dist-tag")
				return nil
			}
			if _, exists := pkg.Versions[target]; !exists && target != version {
				fail(w, 400, "dist-tag refers to an unknown version")
				return nil
			}
		}
		sha := sha1.Sum(archive)
		integrity := sha512.Sum512(archive)
		metadata["dist"] = map[string]string{"tarball": s.base + "/npm/" + url.PathEscape(name) + "/-/" + npmFilename(name, version), "shasum": hex.EncodeToString(sha[:]), "integrity": "sha512-" + base64.StdEncoding.EncodeToString(integrity[:])}
		if err := atomicWrite(s.path("npm", name, version+".tgz"), archive); err != nil {
			return err
		}
		pkg.Versions[version] = metadata
	}
	for tag, version := range incoming.Tags {
		pkg.Tags[tag] = version
	}
	if err := save(path, pkg); err != nil {
		return err
	}
	reply(w, 201, map[string]bool{"ok": true})
	return nil
}

func npmFilename(name, version string) string {
	parts := strings.Split(name, "/")
	return parts[len(parts)-1] + "-" + version + ".tgz"
}
