package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveEntry struct {
	name     string
	content  string
	typeflag byte // tar only; 0 means a regular file
	link     string
}

func makeTarGz(t *testing.T, entries ...archiveEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: e.typeflag, Linkname: e.link}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.content))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeZip(t *testing.T, entries ...archiveEntry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The real release archives are built with `tar -C stage .`, so every entry
// carries a "./" prefix and there is no top-level directory.
func TestExtractOnePicksTheRoleBinaryFromARealShapedTarball(t *testing.T) {
	archive := makeTarGz(t,
		archiveEntry{name: "./", typeflag: tar.TypeDir},
		archiveEntry{name: "./README.md", content: "readme"},
		archiveEntry{name: "./tunnelmesh-server", content: "SERVER"},
		archiveEntry{name: "./tunnelmesh-agent", content: "AGENT"},
		archiveEntry{name: "./tunnelmesh-client", content: "CLIENT"},
		archiveEntry{name: "./docs/guide.md", content: "docs"},
	)
	dst := filepath.Join(t.TempDir(), "tunnelmesh-client")
	if err := ExtractOne(archive, ArchiveTarGz, "tunnelmesh-client", dst, 1<<20); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "CLIENT" {
		t.Fatalf("extracted %q, %v, want exactly the client binary", data, err)
	}
	if info, _ := os.Stat(dst); info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("mode %o is not executable", info.Mode().Perm())
	}
}

func TestExtractOneFromAZip(t *testing.T) {
	archive := makeZip(t,
		archiveEntry{name: "tunnelmesh-agent.exe", content: "AGENT"},
		archiveEntry{name: "tunnelmesh-client.exe", content: "CLIENT"},
		archiveEntry{name: "docs/x.md", content: "x"},
	)
	dst := filepath.Join(t.TempDir(), "tunnelmesh-client.exe")
	if err := ExtractOne(archive, ArchiveZip, "tunnelmesh-client.exe", dst, 1<<20); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(dst); string(data) != "CLIENT" {
		t.Fatalf("extracted %q", data)
	}
}

// An archive is untrusted input: only a plain file named exactly like the binary,
// at the archive root, is ever written, and only to the fixed destination.
func TestExtractOneRefusesEverythingExceptAPlainRootFile(t *testing.T) {
	cases := map[string][]archiveEntry{
		"traversal":        {{name: "../tunnelmesh-client", content: "x"}},
		"deep traversal":   {{name: "./a/../../tunnelmesh-client", content: "x"}},
		"absolute":         {{name: "/tmp/tunnelmesh-client", content: "x"}},
		"nested":           {{name: "./bin/tunnelmesh-client", content: "x"}},
		"symlink":          {{name: "./tunnelmesh-client", typeflag: tar.TypeSymlink, link: "/bin/sh"}},
		"hardlink":         {{name: "./tunnelmesh-client", typeflag: tar.TypeLink, link: "./other"}},
		"directory":        {{name: "./tunnelmesh-client/", typeflag: tar.TypeDir}},
		"missing":          {{name: "./tunnelmesh-agent", content: "x"}},
		"ambiguous":        {{name: "./tunnelmesh-client", content: "one"}, {name: "tunnelmesh-client", content: "two"}},
		"different suffix": {{name: "./tunnelmesh-client.exe", content: "x"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			outside := t.TempDir()
			dst := filepath.Join(outside, "out", "tunnelmesh-client")
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				t.Fatal(err)
			}
			err := ExtractOne(makeTarGz(t, entries...), ArchiveTarGz, "tunnelmesh-client", dst, 1<<20)
			if err == nil {
				t.Fatal("ExtractOne accepted a hostile or wrong archive")
			}
			if _, statErr := os.Stat(dst); statErr == nil {
				t.Fatal("something was written despite the error")
			}
			// Nothing may appear next to the destination or above it either.
			var created []string
			_ = filepath.WalkDir(outside, func(p string, d os.DirEntry, _ error) error {
				if !d.IsDir() {
					created = append(created, p)
				}
				return nil
			})
			if len(created) != 0 {
				t.Fatalf("files created: %v", created)
			}
		})
	}
}

// Entries that are not the binary are never extracted, so a hostile name among
// them is harmless and does not stop a legitimate upgrade.
func TestExtractOneIgnoresHostileEntriesThatAreNotTheBinary(t *testing.T) {
	archive := makeTarGz(t,
		archiveEntry{name: "../../escape", content: "evil"},
		archiveEntry{name: "./link", typeflag: tar.TypeSymlink, link: "/etc/passwd"},
		archiveEntry{name: "./tunnelmesh-client", content: "CLIENT"},
	)
	dir := t.TempDir()
	dst := filepath.Join(dir, "tunnelmesh-client")
	if err := ExtractOne(archive, ArchiveTarGz, "tunnelmesh-client", dst, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "escape")); err == nil {
		t.Fatal("a traversal entry was written")
	}
}

func TestExtractOneEnforcesTheSizeLimit(t *testing.T) {
	archive := makeTarGz(t, archiveEntry{name: "./tunnelmesh-client", content: strings.Repeat("A", 4096)})
	dst := filepath.Join(t.TempDir(), "tunnelmesh-client")
	err := ExtractOne(archive, ArchiveTarGz, "tunnelmesh-client", dst, 1024)
	if err == nil {
		t.Fatal("an oversize binary was accepted")
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Fatal("a partial file was left behind")
	}
}

func TestExtractOneRejectsAGarbageArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.tar.gz")
	if err := os.WriteFile(path, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ExtractOne(path, ArchiveTarGz, "tunnelmesh-client", filepath.Join(t.TempDir(), "x"), 1<<20); err == nil {
		t.Fatal("garbage accepted")
	}
	if err := ExtractOne(path, ArchiveZip, "tunnelmesh-client.exe", filepath.Join(t.TempDir(), "x"), 1<<20); err == nil {
		t.Fatal("garbage accepted as zip")
	}
}
