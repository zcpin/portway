package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type zipEntry struct {
	name, body string
	mode       fs.FileMode
}

func makeZip(t *testing.T, entries ...zipEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		h.SetMode(entry.mode)
		body, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := body.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestArchiveRejectsTraversalAndAmbiguity(t *testing.T) {
	cases := map[string][]zipEntry{}
	for _, name := range []string{"../escaped", "/absolute", "C:/absolute", `a\..\escaped`, "a/../../escaped", "file:stream", "CON", "name. ", "dir/NUL.txt"} {
		cases[name] = []zipEntry{{name, "untrusted", 0644}}
	}
	cases["duplicate"] = []zipEntry{{"a", "first", 0644}, {"A", "second", 0644}}
	cases["link escape"] = []zipEntry{{"link", "../escaped", fs.ModeSymlink | 0777}}
	cases["link parent"] = []zipEntry{{"dir", "../outside", fs.ModeSymlink | 0777}, {"dir/escaped", "untrusted", 0644}}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := makeZip(t, entries...)
			if err := extractArchive(archive, filepath.Join(dir, "stage"), "windows"); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, "escaped")); !os.IsNotExist(err) {
				t.Fatal("archive escaped staging directory")
			}
		})
	}
}

func TestArchivePreservesFilesAndInternalLinks(t *testing.T) {
	entries := []zipEntry{{"portway.app/Contents/MacOS/client", "binary", 0755}}
	if runtime.GOOS != "windows" {
		entries = append(entries, zipEntry{"portway.app/Contents/current", "MacOS", fs.ModeSymlink | 0777})
	}
	dir := filepath.Join(t.TempDir(), "extracted")
	if err := extractArchive(makeZip(t, entries...), dir, "darwin"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "portway.app", "Contents", "MacOS", "client")
	if runtime.GOOS != "windows" {
		file = filepath.Join(dir, "portway.app", "Contents", "current", "client")
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "binary" {
		t.Fatalf("valid archive broken: %s, %v", data, err)
	}
	if err := extractArchive(makeZip(t, entries...), dir, "darwin"); err == nil {
		t.Fatal("extraction must require a new staging directory")
	}
}

func TestTarAndArchiveLimits(t *testing.T) {
	for _, hardLink := range []bool{false, true} {
		file := filepath.Join(t.TempDir(), "package.tar.gz")
		f, _ := os.Create(file)
		gz := gzip.NewWriter(f)
		w := tar.NewWriter(gz)
		kind, size := byte(tar.TypeReg), int64(6)
		if hardLink {
			kind, size = tar.TypeLink, 0
		}
		if err := w.WriteHeader(&tar.Header{Name: "./client", Typeflag: kind, Size: size, Mode: 0755, Linkname: "../outside"}); err != nil {
			t.Fatal(err)
		}
		if !hardLink {
			_, _ = w.Write([]byte("binary"))
		}
		_ = w.Close()
		_ = gz.Close()
		_ = f.Close()
		dir := filepath.Join(t.TempDir(), "stage")
		err := extractArchive(file, dir, "linux")
		if hardLink && err == nil {
			t.Fatal("hardlink accepted")
		}
		if !hardLink && err != nil {
			t.Fatal(err)
		}
	}
	w := archiveWriter{root: t.TempDir(), os: "linux", seen: map[string]bool{}, links: map[string]string{}}
	if err := w.add("bomb", 0644, maxExpanded+1, strings.NewReader("")); err == nil {
		t.Fatal("archive expansion limit ignored")
	}
}
