package m4match

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const isolatedBase = "/var/tmp"

type ownedRoot struct {
	abs    string
	rel    string
	base   *os.Root
	root   *os.Root
	device uint64
	inode  uint64
}

var ownedRoots sync.Map

func acquireFreshRoot(path, repo string) (*ownedRoot, error) {
	abs, err := safeRoot(path, repo)
	if err != nil {
		return nil, err
	}
	base, err := os.OpenRoot(isolatedBase)
	if err != nil {
		return nil, errors.New("descriptor root confinement is unsupported")
	}
	rel, _ := filepath.Rel(isolatedBase, abs)
	if err := mkdirRelativeNoLinks(base, filepath.Dir(rel)); err != nil {
		_ = base.Close()
		return nil, err
	}
	if err := base.Mkdir(rel, 0o700); err != nil {
		_ = base.Close()
		return nil, err
	}
	lease, err := finishOwnedRoot(base, abs, rel)
	if err != nil {
		_ = base.RemoveAll(rel)
		_ = base.Close()
		return nil, err
	}
	return lease, nil
}

func acquireExistingRoot(path, repo string) (*ownedRoot, error) {
	abs, err := safeRoot(path, repo)
	if err != nil {
		return nil, err
	}
	base, err := os.OpenRoot(isolatedBase)
	if err != nil {
		return nil, errors.New("descriptor root confinement is unsupported")
	}
	rel, _ := filepath.Rel(isolatedBase, abs)
	if err := rejectRelativeSymlinks(base, rel); err != nil {
		_ = base.Close()
		return nil, err
	}
	lease, err := finishOwnedRoot(base, abs, rel)
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	return lease, nil
}

func finishOwnedRoot(base *os.Root, abs, rel string) (*ownedRoot, error) {
	root, err := base.OpenRoot(rel)
	if err != nil {
		return nil, err
	}
	info, err := root.Lstat(".")
	if err != nil || !info.IsDir() {
		_ = root.Close()
		return nil, errors.New("owned root is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		_ = root.Close()
		return nil, errors.New("owned root identity is unsupported")
	}
	lease := &ownedRoot{abs: abs, rel: filepath.ToSlash(rel), base: base, root: root, device: uint64(stat.Dev), inode: stat.Ino}
	if _, loaded := ownedRoots.LoadOrStore(abs, lease); loaded {
		_ = root.Close()
		return nil, errors.New("owned root is already active")
	}
	return lease, nil
}

func (r *ownedRoot) Close() error {
	if r == nil {
		return nil
	}
	ownedRoots.Delete(r.abs)
	return errors.Join(r.root.Close(), r.base.Close())
}

func (r *ownedRoot) removeAll() error {
	quarantine := r.rel + ".cleanup"
	if _, err := r.base.Lstat(quarantine); err == nil || !os.IsNotExist(err) {
		return errors.New("cleanup quarantine already exists")
	}
	if err := r.base.Rename(r.rel, quarantine); err != nil {
		return err
	}
	info, err := r.base.Lstat(quarantine)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != r.device || stat.Ino != r.inode || info.Mode()&os.ModeSymlink != 0 {
		_ = r.base.RemoveAll(quarantine)
		return errors.New("cleanup root changed after verification")
	}
	ownedRoots.Delete(r.abs)
	if err := r.root.Close(); err != nil {
		return err
	}
	return r.base.RemoveAll(quarantine)
}

func mkdirRelativeNoLinks(root *os.Root, name string) error {
	name = filepath.ToSlash(filepath.Clean(name))
	if name == "." || name == "" {
		return nil
	}
	current := ""
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." {
			continue
		}
		if current == "" {
			current = component
		} else {
			current += "/" + component
		}
		info, err := root.Lstat(current)
		if os.IsNotExist(err) {
			if err := root.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("owned root path contains a symlink or non-directory")
		}
	}
	return nil
}

func rejectRelativeSymlinks(root *os.Root, name string) error {
	name = filepath.ToSlash(filepath.Clean(name))
	current := ""
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." {
			continue
		}
		if current == "" {
			current = component
		} else {
			current += "/" + component
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("owned root path contains a symlink")
		}
	}
	return nil
}

func ownerForPath(path string) (*ownedRoot, string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, ""
	}
	abs = filepath.Clean(abs)
	var found *ownedRoot
	longest := 0
	ownedRoots.Range(func(_, value any) bool {
		candidate := value.(*ownedRoot)
		if (abs == candidate.abs || strings.HasPrefix(abs, candidate.abs+string(os.PathSeparator))) && len(candidate.abs) > longest {
			found = candidate
			longest = len(candidate.abs)
		}
		return true
	})
	if found == nil {
		return nil, ""
	}
	rel, _ := filepath.Rel(found.abs, abs)
	return found, filepath.ToSlash(rel)
}

func rootMkdirAll(path string, mode os.FileMode) error {
	if owner, rel := ownerForPath(path); owner != nil {
		return mkdirRelativeNoLinks(owner.root, rel)
	}
	if strings.HasPrefix(filepath.Clean(path), isolatedBase+string(os.PathSeparator)) {
		return errors.New("unowned isolated-root mutation rejected")
	}
	return os.MkdirAll(path, mode)
}

func rootOpenFile(path string, flag int, mode os.FileMode) (*os.File, error) {
	if owner, rel := ownerForPath(path); owner != nil {
		if err := mkdirRelativeNoLinks(owner.root, filepath.Dir(rel)); err != nil {
			return nil, err
		}
		if err := rejectRelativeSymlinks(owner.root, filepath.Dir(rel)); err != nil {
			return nil, err
		}
		if info, err := owner.root.Lstat(rel); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("owned root file is a symlink")
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		file, err := owner.root.OpenFile(rel, flag, mode)
		if err != nil {
			return nil, err
		}
		opened, openErr := file.Stat()
		linked, linkErr := owner.root.Lstat(rel)
		if openErr != nil || linkErr != nil || linked.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, linked) {
			_ = file.Close()
			return nil, errors.New("owned root file changed during open")
		}
		return file, nil
	}
	if strings.HasPrefix(filepath.Clean(path), isolatedBase+string(os.PathSeparator)) {
		return nil, errors.New("unowned isolated-root mutation rejected")
	}
	return os.OpenFile(path, flag, mode)
}

func rootWriteFile(path string, payload []byte, mode os.FileMode) error {
	file, err := rootOpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(payload)
	return errors.Join(writeErr, file.Close())
}
func rootReadFile(path string) ([]byte, error) {
	if owner, rel := ownerForPath(path); owner != nil {
		if err := rejectRelativeSymlinks(owner.root, rel); err != nil {
			return nil, err
		}
		return owner.root.ReadFile(rel)
	}
	if strings.HasPrefix(filepath.Clean(path), isolatedBase+string(os.PathSeparator)) {
		return nil, errors.New("unowned isolated-root read rejected")
	}
	return os.ReadFile(path)
}
func rootRemove(path string) error {
	if owner, rel := ownerForPath(path); owner != nil {
		return owner.root.Remove(rel)
	}
	if strings.HasPrefix(filepath.Clean(path), isolatedBase+string(os.PathSeparator)) {
		return errors.New("unowned isolated-root mutation rejected")
	}
	return os.Remove(path)
}
