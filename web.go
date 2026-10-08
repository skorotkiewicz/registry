package main

import (
	"embed"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

//go:embed web/index.html web/style.css web/app.js
var webFiles embed.FS

func serveWeb(w http.ResponseWriter, r *http.Request) bool {
	var file, contentType string
	switch r.URL.Path {
	case "/":
		file, contentType = "web/index.html", "text/html; charset=utf-8"
	case "/web/style.css":
		file, contentType = "web/style.css", "text/css; charset=utf-8"
	case "/web/app.js":
		file, contentType = "web/app.js", "text/javascript; charset=utf-8"
	default:
		return false
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	data, _ := webFiles.ReadFile(file) // These exact files are required at compile time.
	if r.Method != "HEAD" {
		_, _ = w.Write(data)
	}
	return true
}

type packageVersion struct {
	Version string `json:"version"`
	Yanked  bool `json:"yanked"`
	Tags    []string `json:"tags"`
}

type packageSummary struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Versions []packageVersion `json:"versions"`
}

func (s *registry) catalog(w http.ResponseWriter) error {
	packages := []packageSummary{}
	// ponytail: scan metadata on each refresh; paginate and cache if the registry outgrows a small team.
	for _, kind := range []string{"cargo", "npm"} {
		dirs, err := os.ReadDir(filepath.Join(s.data, kind))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, dir := range dirs {
			if !dir.IsDir() {
				continue
			}
			path := filepath.Join(s.data, kind, dir.Name(), "metadata.json")
			p := packageSummary{Kind: kind, Versions: []packageVersion{}}
			if kind == "cargo" {
				var pkg cratePackage
				if err := load(path, &pkg); err != nil {
					if errors.Is(err, os.ErrNotExist) { continue }
					return err
				}
				p.Name = pkg.Name
				for _, entry := range pkg.Versions {
					version, _ := entry["vers"].(string)
					yanked, _ := entry["yanked"].(bool)
					p.Versions = append(p.Versions, packageVersion{Version: version, Yanked: yanked, Tags: []string{}})
				}
			} else {
				var pkg npmPackage
				if err := load(path, &pkg); err != nil {
					if errors.Is(err, os.ErrNotExist) { continue }
					return err
				}
				p.Name = pkg.Name
				for version := range pkg.Versions {
					v := packageVersion{Version: version, Tags: []string{}}
					for tag, target := range pkg.Tags {
						if target == version { v.Tags = append(v.Tags, tag) }
					}
					sort.Strings(v.Tags)
					p.Versions = append(p.Versions, v)
				}
			}
			sort.Slice(p.Versions, func(i, j int) bool { return p.Versions[i].Version < p.Versions[j].Version })
			packages = append(packages, p)
		}
	}
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Kind != packages[j].Kind { return packages[i].Kind < packages[j].Kind }
		return packages[i].Name < packages[j].Name
	})
	reply(w, 200, map[string]any{"url": s.base, "packages": packages})
	return nil
}
