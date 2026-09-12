package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxExpanded = 1 << 30

type archiveWriter struct {
	root  string
	os    string
	seen  map[string]bool
	links map[string]string
	total int64
}

// Extract only writes into a freshly created directory. Symlinks are materialized
// last, then resolved as a group; no archive payload is ever written through one.
func extractArchive(archive, destination, platform string) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	w := archiveWriter{root: destination, os: platform, seen: map[string]bool{}, links: map[string]string{}}
	if strings.HasSuffix(archive, ".zip") {
		r, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer r.Close()
		for _, entry := range r.File {
			if entry.UncompressedSize64 > maxExpanded {
				return errors.New("archive entry exceeds size limit")
			}
			body, err := entry.Open()
			if err != nil {
				return err
			}
			err = w.add(entry.Name, entry.Mode(), int64(entry.UncompressedSize64), body)
			closeErr := body.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
	} else if strings.HasSuffix(archive, ".tar.gz") {
		file, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer file.Close()
		gz, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer gz.Close()
		r := tar.NewReader(gz)
		for {
			header, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			mode := fs.FileMode(header.Mode) & 0777
			var body io.Reader = r
			size := header.Size
			switch header.Typeflag {
			case tar.TypeDir:
				mode |= fs.ModeDir
			case tar.TypeReg, tar.TypeRegA:
			case tar.TypeSymlink:
				mode |= fs.ModeSymlink
				body, size = strings.NewReader(header.Linkname), int64(len(header.Linkname))
			default:
				return errors.New("archive contains a hard link or special file")
			}
			if err := w.add(header.Name, mode, size, body); err != nil {
				return err
			}
		}
		// Consume the gzip trailer, including its integrity check.
		if _, err := io.Copy(io.Discard, io.LimitReader(gz, 1<<20)); err != nil {
			return err
		}
	} else {
		return errors.New("unsupported portable archive")
	}
	return w.finishLinks()
}

func cleanEntry(name string) (string, error) {
	if name == "" || len(name) > 4096 || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return "", errors.New("unsafe archive path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", errors.New("archive path traverses its root")
		}
	}
	return path.Clean(name), nil
}

func (w *archiveWriter) add(name string, mode fs.FileMode, size int64, body io.Reader) error {
	name, err := cleanEntry(name)
	if err != nil {
		return err
	}
	if name == "." && mode.IsDir() {
		return nil
	}
	if w.os == "darwin" && (name == "__MACOSX" || strings.HasPrefix(name, "__MACOSX/")) {
		return nil // ditto resource-fork sidecars, not part of the app bundle.
	}
	key := strings.ToLower(name)
	if w.seen[key] || len(w.seen) >= 25000 || name == "." {
		return errors.New("duplicate archive path or too many entries")
	}
	w.seen[key] = true
	if size < 0 || size > maxExpanded-w.total {
		return errors.New("expanded archive exceeds size limit")
	}
	w.total += size
	if w.os == "windows" {
		for _, part := range strings.Split(name, "/") {
			stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
			if strings.TrimRight(part, ". ") != part || strings.ContainsAny(part, "<>\"|?*") || stem == "CON" || stem == "NUL" || stem == "PRN" || stem == "AUX" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9') {
				return errors.New("unsafe Windows archive path")
			}
		}
	}
	full := filepath.Join(w.root, filepath.FromSlash(name))
	if mode.IsDir() {
		return os.MkdirAll(full, 0755)
	}
	if mode&fs.ModeSymlink != 0 {
		if w.os == "windows" || size > 4096 {
			return errors.New("unsupported archive symlink")
		}
		data, err := io.ReadAll(io.LimitReader(body, size+1))
		if err != nil || int64(len(data)) != size {
			return errors.New("invalid archive symlink")
		}
		target := string(data)
		resolved := path.Clean(path.Join(path.Dir(name), target))
		if target == "" || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\:\x00") || resolved == ".." || strings.HasPrefix(resolved, "../") {
			return errors.New("archive symlink escapes its root")
		}
		w.links[name] = target
		return nil
	}
	if !mode.IsRegular() {
		return errors.New("archive contains a special file")
	}
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(file, io.LimitReader(body, size+1))
	if copyErr == nil && n != size {
		copyErr = errors.New("archive entry size mismatch")
	}
	if copyErr == nil {
		copyErr = file.Chmod(mode.Perm() & 0777)
	}
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (w *archiveWriter) finishLinks() error {
	for name, target := range w.links {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, exists := w.links[parent]; exists {
				return errors.New("archive entry has a symlink parent")
			}
		}
		full := filepath.Join(w.root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return err
		}
		if err := os.Symlink(filepath.FromSlash(target), full); err != nil {
			return err
		}
	}
	for name := range w.links {
		resolved, err := filepath.EvalSymlinks(filepath.Join(w.root, filepath.FromSlash(name)))
		if err != nil || !within(w.root, resolved) {
			return fmt.Errorf("archive contains a dangling, cyclic, or escaping symlink: %s", name)
		}
	}
	return nil
}
