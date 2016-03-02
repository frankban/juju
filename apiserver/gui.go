// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package apiserver

import (
	"archive/tar"
	"compress/bzip2"
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
	baseDir string
	ctxt    httpContext
}

func (router *guiRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rootDir, err := router.ensureFiles(req)
	if err != nil {
		sendError(w, err)
		return
	}

	staticDir := filepath.Join(rootDir, "static")
	fs := http.FileServer(http.Dir(staticDir))

	uuid := req.URL.Query().Get(":modeluuid")
	parts := strings.SplitAfterN(req.URL.Path, uuid, 2)
	req.URL.Path = parts[1]

	h := &guiHandler{
		baseURLPath: parts[0],
		rootDir:     rootDir,
		uuid:        uuid,
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	mux.HandleFunc("/config.js", h.serveConfig)
	mux.HandleFunc("/combo", h.serveCombo)
	mux.HandleFunc("/", h.serveIndex)
	mux.ServeHTTP(w, req)
}

func (router *guiRouter) ensureFiles(req *http.Request) (string, error) {
	version := "2.0.3"
	rootDir := filepath.Join(router.baseDir, version)
	info, err := os.Stat(rootDir)
	if err == nil {
		if info.IsDir() {
			return rootDir, nil
		}
		return "", errors.Errorf("cannot use Juju GUI root directory %q: not a directory", rootDir)
	}
	if !os.IsNotExist(err) {
		return "", errors.Annotate(err, "cannot stat Juju GUI root directory")
	}
	st, err := router.ctxt.stateForRequestUnauthenticated(req)
	if err != nil {
		return "", errors.Annotate(err, "cannot open state")
	}
	storage, err := st.GUIStorage()
	if err != nil {
		return "", errors.Annotate(err, "cannot open GUI storage")
	}
	defer storage.Close()
	meta, r, err := storage.Open(version)
	if err != nil {
		return "", errors.Annotatef(err, "cannot find GUI archive version %q", version)
	}
	defer r.Close()
	if err := os.MkdirAll(router.baseDir, 0755); err != nil {
		return "", errors.Annotate(err, "cannot create Juju GUI base directory")
	}
	if err := uncompressGUI(r, meta.Version, rootDir); err != nil {
		return "", errors.Annotate(err, "cannot uncompress Juju GUI archive")
	}
	return rootDir, nil
}

func uncompressGUI(r io.Reader, version, targetDir string) error {
	tempDir, err := ioutil.TempDir("", "gui")
	if err != nil {
		return errors.Annotate(err, "cannot create Juju GUI temporary directory")
	}
	defer os.Remove(tempDir)
	guiDir := "jujugui-" + version + "/jujugui"
	tr := tar.NewReader(bzip2.NewReader(r))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.Trace(err)
		}
		if !strings.HasPrefix(hdr.Name, guiDir+"/") {
			continue
		}
		info := hdr.FileInfo()
		path := filepath.Join(tempDir, hdr.Name)
		if info.IsDir() {
			if err := os.MkdirAll(path, info.Mode()); err != nil {
				return errors.Trace(err)
			}
			continue
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return errors.Trace(err)
		}
		defer f.Close()
		if _, err := io.Copy(f, tr); err != nil {
			return errors.Trace(err)
		}
	}
	if err := os.Rename(filepath.Join(tempDir, guiDir), targetDir); err != nil {
		return errors.Annotate(err, "cannot rename Juju GUI root directory")
	}
	return nil
}

// guiHandler serves the Juju GUI.
type guiHandler struct {
	baseURLPath string
	rootDir     string
	uuid        string
}

// serveCombo serves the GUI JavaScript and CSS files, dynamically combined.
func (h *guiHandler) serveCombo(w http.ResponseWriter, req *http.Request) {
	ctype := ""
	parts := strings.Split(req.URL.RawQuery, "&")
	paths := make([]string, 0, len(parts))
	for _, p := range parts {
		fpath, err := getGUIComboPath(h.rootDir, p)
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

func getGUIComboPath(rootDir, query string) (string, error) {
	k := strings.SplitN(query, "=", 2)[0]
	fname, err := url.QueryUnescape(k)
	if err != nil {
		return "", errors.NewBadRequest(err, fmt.Sprintf("invalid file name %q", k))
	}
	// Ignore pat injected queries.
	if strings.HasPrefix(fname, ":") {
		return "", nil
	}
	fpath := filepath.Join(rootDir, "static", "gui", "build", fname)
	rel, err := filepath.Rel(rootDir, fpath)
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
	spriteFile := filepath.Join(h.rootDir, "static", "gui", "build", "app", "assets", "stack", "svg", "sprite.css.svg")
	spriteContent, err := ioutil.ReadFile(spriteFile)
	if err != nil {
		sendError(w, err)
		return
	}
	tmpl := filepath.Join(h.rootDir, "templates", "index.html.go")
	renderGUITemplate(w, tmpl, map[string]interface{}{
		"comboURL":      h.baseURLPath + "/combo",
		"configURL":     h.baseURLPath + "/config.js",
		"debug":         true,
		"spriteContent": string(spriteContent),
	})
}

// serveConfig serves the Juju GUI JavaScript configuration file.
func (h *guiHandler) serveConfig(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", mime.TypeByExtension(".js"))
	tmpl := filepath.Join(h.rootDir, "templates", "config.js.go")
	renderGUITemplate(w, tmpl, map[string]interface{}{
		"base":    h.baseURLPath,
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
