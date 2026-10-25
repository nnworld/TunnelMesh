package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// ArchiveKind is the container format of a release archive.
type ArchiveKind int

const (
	ArchiveTarGz ArchiveKind = iota
	ArchiveZip
)

// ErrTooLarge means the data exceeded its size limit.
var ErrTooLarge = errors.New("selfupdate: data exceeds the size limit")

// ExtractOne writes the single file named wantBase from the archive to dst and
// touches nothing else.
//
// The archive is untrusted input. Only one entry is ever considered: a regular
// file whose cleaned name is exactly wantBase at the archive root. Entries with
// ".." or absolute names, links, directories and files in sub-directories never
// match, so they cannot be extracted, and whatever an archive names elsewhere is
// irrelevant because it is never written. The destination is the caller's fixed
// path, never derived from an entry name.
func ExtractOne(archivePath string, kind ArchiveKind, wantBase, dst string, maxBytes int64) (err error) {
	// Never overwrite: dst is a fresh path in a staging directory, so anything
	// already there was put there by someone else.
	if _, statErr := os.Lstat(dst); statErr == nil {
		return fmt.Errorf("selfupdate: %s already exists", dst)
	}
	// An archive that fails after the binary was written (a second, ambiguous
	// match, say) must not leave a half-trusted file behind.
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	switch kind {
	case ArchiveTarGz:
		return extractFromTarGz(archivePath, wantBase, dst, maxBytes)
	case ArchiveZip:
		return extractFromZip(archivePath, wantBase, dst, maxBytes)
	default:
		return fmt.Errorf("selfupdate: unknown archive kind %d", kind)
	}
}

// cleanRootName returns the entry name relative to the archive root, or false if
// the name is absolute or climbs out of the root.
func cleanRootName(name string) (string, bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if path.IsAbs(name) {
		return "", false
	}
	cleaned := path.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func extractFromTarGz(archivePath, wantBase, dst string, maxBytes int64) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("selfupdate: not a gzip archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	matches := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("selfupdate: reading archive: %w", err)
		}
		name, ok := cleanRootName(hdr.Name)
		if !ok || name != wantBase {
			continue
		}
		matches++
		if matches > 1 {
			return fmt.Errorf("selfupdate: archive holds more than one %s", wantBase)
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("selfupdate: %s in the archive is not a regular file", wantBase)
		}
		if err := writeLimited(dst, tr, maxBytes); err != nil {
			return err
		}
	}
	if matches == 0 {
		return fmt.Errorf("selfupdate: archive does not contain %s", wantBase)
	}
	return nil
}

func extractFromZip(archivePath, wantBase, dst string, maxBytes int64) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("selfupdate: not a zip archive: %w", err)
	}
	defer zr.Close()
	matches := 0
	for _, f := range zr.File {
		name, ok := cleanRootName(f.Name)
		if !ok || name != wantBase {
			continue
		}
		matches++
		if matches > 1 {
			return fmt.Errorf("selfupdate: archive holds more than one %s", wantBase)
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("selfupdate: %s in the archive is not a regular file", wantBase)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeLimited(dst, rc, maxBytes)
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	if matches == 0 {
		return fmt.Errorf("selfupdate: archive does not contain %s", wantBase)
	}
	return nil
}

// writeLimited copies src to a new file at dst, failing if it is longer than max
// and removing the partial file on any error. O_EXCL means a path that already
// exists, including a symlink planted there, is never followed.
func writeLimited(dst string, src io.Reader, max int64) (err error) {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(dst)
		}
	}()
	n, err := io.Copy(out, io.LimitReader(src, max+1))
	if err != nil {
		return err
	}
	if n > max {
		return ErrTooLarge
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return out.Close()
}
