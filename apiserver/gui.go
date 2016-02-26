// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package apiserver

import (
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/juju/errors"

	"github.com/juju/juju/version"
)

// guiRouter serves the Juju GUI routes.
type guiRouter struct {
	dataDir string
}

func (r *guiRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	root := filepath.Join(r.dataDir, "gui", "jujugui")
	staticDir := filepath.Join(root, "static")
	fs := http.FileServer(http.Dir(staticDir))

	uuid := req.URL.Query().Get(":modeluuid")
	parts := strings.SplitAfterN(req.URL.Path, uuid+"/gui", 2)
	req.URL.Path = parts[1]

	h := &guiHandler{
		base: parts[0],
		root: root,
		uuid: uuid,
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/config.js", h.serveConfig)
	mux.HandleFunc("/combo", h.serveCombo)
	mux.HandleFunc("/", h.serveIndex)
	mux.ServeHTTP(w, req)
}

// guiHandler serves the Juju GUI.
type guiHandler struct {
	base string
	root string
	uuid string
}

// serveCombo serves the GUI JavaScript and CSS files, dynamically combined.
func (h *guiHandler) serveCombo(w http.ResponseWriter, req *http.Request) {
	ctype := ""
	parts := strings.Split(req.URL.RawQuery, "&")
	paths := make([]string, 0, len(parts))
	for _, p := range parts {
		fpath, err := getGUIComboPath(h.root, p)
		if err != nil {
			sendError(w, err)
			return
		}
		if fpath == "" {
			continue
		}
		paths = append(paths, fpath)
		// Assume we don't mix different content types when combining contents.
		if ctype == "" {
			ctype = mime.TypeByExtension(filepath.Ext(fpath))
		}
	}
	w.Header().Set("Content-Type", ctype)
	for _, fpath := range paths {
		sendGUIComboFile(w, fpath)
	}
}

func getGUIComboPath(root, query string) (string, error) {
	k := strings.SplitN(query, "=", 2)[0]
	fname, err := url.QueryUnescape(k)
	if err != nil {
		return "", errors.NewBadRequest(err, fmt.Sprintf("invalid file name %q", k))
	}
	// Ignore pat injected queries.
	if strings.HasPrefix(fname, ":") {
		return "", nil
	}
	fpath := filepath.Join(root, "static", "gui", "build", fname)
	rel, err := filepath.Rel(root, fpath)
	if err != nil {
		return "", errors.NewBadRequest(err, fmt.Sprintf("invalid file path %q", k))
	}
	if strings.HasPrefix(rel, "..") {
		return "", errors.BadRequestf("unauthorized file path %q", k)
	}
	return fpath, nil
}

func sendGUIComboFile(w io.Writer, fpath string) {
	f, err := os.Open(fpath)
	if err != nil {
		logger.Infof("cannot send combo file %q: %s", fpath, err)
		return
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		logger.Infof("cannot copy combo file %q: %s", fpath, err)
		return
	}
	fmt.Fprintf(w, "\n/* %s */\n", filepath.Base(f.Name()))
}

// serveIndex serves the GUI index file.
func (h *guiHandler) serveIndex(w http.ResponseWriter, req *http.Request) {
	spriteFile := filepath.Join(h.root, "static", "gui", "build", "app", "assets", "stack", "svg", "sprite.css.svg")
	spriteContent, err := ioutil.ReadFile(spriteFile)
	if err != nil {
		sendError(w, err)
		return
	}
	tmpl := filepath.Join(h.root, "templates", "index.html.go")
	renderGUITemplate(w, tmpl, map[string]interface{}{
		"comboURL":      h.base + "/combo",
		"configURL":     h.base + "/config.js",
		"debug":         true,
		"spriteContent": string(spriteContent),
	})
}

// serveConfig serves the Juju GUI JavaScript configuration file.
func (h *guiHandler) serveConfig(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", mime.TypeByExtension(".js"))
	tmpl := filepath.Join(h.root, "templates", "config.js.go")
	renderGUITemplate(w, tmpl, map[string]interface{}{
		"base":    h.base,
		"host":    req.Host,
		"socket":  "/model/$uuid/api",
		"uuid":    h.uuid,
		"version": version.Current.String(),
	})
}

func renderGUITemplate(w http.ResponseWriter, tmpl string, ctx map[string]interface{}) {
	t := template.Must(template.ParseFiles(tmpl))
	if err := t.Execute(w, ctx); err != nil {
		sendError(w, err)
	}
}
