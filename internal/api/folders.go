package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"seelynx/internal/storage"
)

// The folder dialog on the Storage page browses the server's folders, because a storage
// location is a folder on the server, not on the computer someone is viewing from. Only
// folder names are shown, never files.

type folderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type folderListing struct {
	Path    string        `json:"path"`   // "" for the list of drives
	Parent  string        `json:"parent"` // "" when going up shows the drives, or at the top
	Up      bool          `json:"up"`     // whether there's anywhere to go up to
	Folders []folderEntry `json:"folders"`
}

const (
	maxFolders  = 2000
	listTimeout = 5 * time.Second // a network path that's gone can hang
)

// hiddenFolder leaves out folders nobody would record into.
func hiddenFolder(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") || strings.EqualFold(name, "System Volume Information")
}

// readFolders lists the folders in dir, giving up after listTimeout.
func readFolders(dir string) ([]folderEntry, error) {
	type result struct {
		entries []os.DirEntry
		err     error
	}
	done := make(chan result, 1)
	go func() {
		e, err := os.ReadDir(dir)
		done <- result{e, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(listTimeout):
		return nil, errSlowFolder
	}
	if r.err != nil && len(r.entries) == 0 {
		return nil, r.err
	}
	out := []folderEntry{}
	for _, e := range r.entries {
		if hiddenFolder(e.Name()) {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				isDir = info.IsDir()
			}
		}
		if isDir {
			out = append(out, folderEntry{e.Name(), filepath.Join(dir, e.Name())})
		}
		if len(out) == maxFolders {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

var errSlowFolder = errors.New("slow")

// drives lists the server's drive letters. A and B are skipped, because checking an empty
// floppy drive can hang.
func drives() []folderEntry {
	out := []folderEntry{}
	for c := 'C'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if _, err := os.Stat(root); err == nil {
			out = append(out, folderEntry{root, root})
		}
	}
	return out
}

func (s *Server) listFolders(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" && runtime.GOOS == "windows" {
		writeJSON(w, http.StatusOK, folderListing{Folders: drives()})
		return
	}
	if path == "" {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		fail(w, http.StatusBadRequest, "Enter a full folder path, such as D:\\Video or /mnt/nas/video.")
		return
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" && len(path) == 2 && path[1] == ':' {
		path += `\`
	}
	folders, err := readFolders(path)
	switch {
	case errors.Is(err, errSlowFolder):
		fail(w, http.StatusGatewayTimeout, "This folder isn't answering. If it's on a network share, the share may be offline.")
		return
	case errors.Is(err, os.ErrNotExist):
		fail(w, http.StatusNotFound, "This folder isn't there.")
		return
	case errors.Is(err, os.ErrPermission):
		fail(w, http.StatusForbidden, "Seelynx can't open this folder: access is denied. Give the account Seelynx runs as permission to it.")
		return
	case err != nil:
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		fail(w, http.StatusBadRequest, "Seelynx can't open this folder: "+err.Error())
		return
	}
	out := folderListing{Path: path, Folders: folders}
	if parent := filepath.Dir(path); parent != path {
		out.Parent, out.Up = parent, true
	} else if runtime.GOOS == "windows" {
		out.Up = true // up from a drive or a share shows the drives
	}
	writeJSON(w, http.StatusOK, out)
}

// makeFolder creates a folder inside another, for the dialog's New folder button.
func (s *Server) makeFolder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	bad := in.Name == "" || in.Name == "." || in.Name == ".." || len(in.Name) > 100 || strings.ContainsAny(in.Name, `/\`)
	if runtime.GOOS == "windows" {
		bad = bad || strings.ContainsAny(in.Name, `<>:"|?*`) || strings.HasSuffix(in.Name, ".")
	}
	if bad {
		fail(w, http.StatusBadRequest, "Enter a folder name, without / \\ : * ? \" < > or |.")
		return
	}
	if !filepath.IsAbs(in.Parent) {
		fail(w, http.StatusBadRequest, "Choose where to make the folder first.")
		return
	}
	path := filepath.Join(filepath.Clean(in.Parent), in.Name)
	if err := os.Mkdir(path, 0o750); errors.Is(err, os.ErrExist) {
		fail(w, http.StatusConflict, "There's already a folder with this name.")
		return
	} else if errors.Is(err, os.ErrPermission) {
		fail(w, http.StatusForbidden, "Seelynx can't make a folder here: access is denied.")
		return
	} else if err != nil {
		fail(w, http.StatusBadRequest, fmt.Sprintf("Seelynx can't make this folder: %v", err))
		return
	}
	writeJSON(w, http.StatusCreated, folderEntry{in.Name, path})
}

// connectShare signs in to a network share, so the folder dialog can browse it before
// the location is saved. Without a password it uses the one stored for location id.
func (s *Server) connectShare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path     string  `json:"path"`
		Username string  `json:"username"`
		Password *string `json:"password"`
		ID       int64   `json:"id"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Path, in.Username = strings.TrimSpace(in.Path), strings.TrimSpace(in.Username)
	if !storage.IsShare(in.Path) || in.Username == "" {
		fail(w, http.StatusBadRequest, `Enter a share path like \\nas\video, and its username.`)
		return
	}
	password := ""
	if in.Password != nil {
		password = *in.Password
	} else if in.ID != 0 {
		l, err := s.Store.Location(r.Context(), in.ID)
		if err == nil && l.Username == in.Username {
			password = l.Password
		}
	}
	if err := storage.Connect(in.Path, in.Username, password); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
