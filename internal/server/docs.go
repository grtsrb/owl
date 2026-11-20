package server

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"sync"

	"owl/internal/docs"
)

// The built-in documentation (rendered at build time, internal/docs): readable by every signed-in
// user and by API tokens (both are plain reads), never public. The docs describe the running build.

type docsIndexJSON struct {
	Version  string         `json:"version"`
	Pages    []docs.Page    `json:"pages"`
	Sections []docs.Section `json:"sections"`
}

type docsPageJSON struct {
	docs.Page
	HTML string `json:"html"`
}

type docsCache struct {
	once sync.Once
	ix   *docs.Index
	err  error
}

const docsMissing = "docs not built into this server (run `make build`, which runs docsgen)"

func (s *Server) docsIndex() (*docs.Index, error) {
	s.docs.once.Do(func() {
		if s.Docs == nil {
			s.docs.err = fs.ErrNotExist
			return
		}
		s.docs.ix, s.docs.err = docs.Load(s.Docs)
	})
	return s.docs.ix, s.docs.err
}

func (s *Server) listDocs(w http.ResponseWriter, r *http.Request) {
	ix, err := s.docsIndex()
	if errors.Is(err, fs.ErrNotExist) {
		httpError(w, http.StatusNotFound, docsMissing)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, docsIndexJSON{Version: s.Version, Pages: ix.Pages, Sections: ix.Sections})
}

func (s *Server) docPage(w http.ResponseWriter, r *http.Request) {
	ix, err := s.docsIndex()
	if errors.Is(err, fs.ErrNotExist) {
		httpError(w, http.StatusNotFound, docsMissing)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	id := r.PathValue("page")
	for _, p := range ix.Pages { // only listed ids reach the file system
		if p.ID != id {
			continue
		}
		b, err := fs.ReadFile(s.Docs, path.Join(docs.PageDir, id+".html"))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, docsPageJSON{Page: p, HTML: string(b)})
		return
	}
	httpError(w, http.StatusNotFound, "no such docs page")
}
