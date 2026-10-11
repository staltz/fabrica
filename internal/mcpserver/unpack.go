package mcpserver

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/staltz/fabrica/internal/blueprint"
)

// unpackSkill copies a skill's folder into the local cache and returns the
// folder's path, like ~/.cache/fabrica/skills/<hash>/<name>/.
//
// The folder keeps the skill's name (the Agent Skills spec wants that), keeps
// executable bits so scripts can run, and is read-only. Its parent is named
// after the content hash, so a folder never changes once written: a new
// version of the skill gets a new folder. Nothing is ever written into the
// product repo.
func unpackSkill(cacheRoot string, sk *blueprint.Skill) (string, error) {
	if cacheRoot == "" {
		return "", errors.New("no skill cache folder configured")
	}
	parent := filepath.Join(cacheRoot, "skills", sk.Hash[:16])
	dest := filepath.Join(parent, sk.Ref.Name)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(parent, "."+sk.Ref.Name+".*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	for _, f := range sk.Files {
		if err := copyFile(filepath.Join(sk.Dir, filepath.FromSlash(f.Path)), filepath.Join(tmp, filepath.FromSlash(f.Path)), f.Executable); err != nil {
			return "", fmt.Errorf("%s: %w", f.Path, err)
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		if _, statErr := os.Stat(dest); statErr == nil {
			return dest, nil // another session unpacked it at the same time
		}
		return "", err
	}
	return dest, nil
}

func copyFile(src, dst string, executable bool) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	mode := os.FileMode(0o444)
	if executable {
		mode = 0o555
	}
	return os.Chmod(dst, mode)
}
