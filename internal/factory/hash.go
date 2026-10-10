package factory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// hashBytes fingerprints a file's content.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hashTree fingerprints a whole folder (every file's path, executable bit
// and content) and lists its files. Two folders with the same hash have
// exactly the same files.
//
// Symlinks are refused: a skill must work wherever its folder is copied,
// exactly as it is in git, and a link could point anywhere.
func hashTree(dir string) (string, []SkillFile, error) {
	var files []SkillFile
	err := fs.WalkDir(os.DirFS(dir), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if isSymlink(d) {
			return fmt.Errorf("%s is a symlink, and Fabrica doesn't allow symlinks in skills or providers; copy the file in instead", p)
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s isn't a regular file", p)
		}
		files = append(files, SkillFile{Path: p, Size: info.Size(), Executable: info.Mode().Perm()&0o111 != 0})
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	h := sha256.New()
	for _, f := range files {
		mode := "-"
		if f.Executable {
			mode = "x"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00", f.Path, mode, f.Size)
		file, err := os.Open(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil {
			return "", nil, err
		}
		_, err = io.Copy(h, file)
		file.Close()
		if err != nil {
			return "", nil, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), files, nil
}
