// Package atomicfile performs validated atomic namespace replacement. A
// post-rename directory-sync failure is explicitly uncertain rather than
// claimed crash-durable; callers must reconcile and retry.
package atomicfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// CommitOutcomeUncertainError means rename succeeded but syncing the parent
// directory failed. The canonical name may resolve to the complete old or
// complete intended payload after a crash. Callers must treat this as failure,
// inspect the reconciliation fields, and retry idempotently.
type CommitOutcomeUncertainError struct {
	Path                     string
	Cause                    error
	CanonicalMatchesExpected bool
	ReconcileError           error
}

func (e *CommitOutcomeUncertainError) Error() string {
	if e.ReconcileError != nil {
		return fmt.Sprintf("commit outcome uncertain for %s: %v; canonical reconciliation failed: %v", e.Path, e.Cause, e.ReconcileError)
	}
	return fmt.Sprintf("commit outcome uncertain for %s: %v; canonical_matches_expected=%t", e.Path, e.Cause, e.CanonicalMatchesExpected)
}

func (e *CommitOutcomeUncertainError) Unwrap() error { return e.Cause }

// Ops exposes durability operations for focused failure-path tests. Nil
// functions use the operating-system implementation.
type Ops struct {
	SyncFile  func(*os.File) error
	CloseFile func(*os.File) error
	Rename    func(string, string) error
	SyncDir   func(string) error
}

func (o Ops) withDefaults() Ops {
	if o.SyncFile == nil {
		o.SyncFile = func(f *os.File) error { return f.Sync() }
	}
	if o.Rename == nil {
		o.Rename = os.Rename
	}
	if o.CloseFile == nil {
		o.CloseFile = func(f *os.File) error { return f.Close() }
	}
	if o.SyncDir == nil {
		o.SyncDir = syncDir
	}
	return o
}

// NewTemp creates a randomized exclusive temporary file beside dest. Keeping
// it in the destination directory makes the eventual rename atomic.
func NewTemp(dest string, perm os.FileMode) (*os.File, error) {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create parent directory: %w", err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".atomic-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary file: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return nil, fmt.Errorf("chmod temporary file: %w", err)
	}
	return f, nil
}

// Commit syncs and closes temp, validates the closed temporary path, atomically
// renames it over dest, and syncs the parent directory. The temp file is
// removed on every pre-rename failure.
func Commit(temp *os.File, dest string, validate func(string) error) error {
	return CommitWithOps(temp, dest, validate, Ops{})
}

// CommitWithOps is Commit with injectable durability operations for tests.
func CommitWithOps(temp *os.File, dest string, validate func(string) error, ops Ops) error {
	ops = ops.withDefaults()
	tmp := temp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmp)
		}
	}()
	if err := ops.SyncFile(temp); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := ops.CloseFile(temp); err != nil {
		_ = temp.Close()
		return fmt.Errorf("close temporary file: %w", err)
	}
	if validate != nil {
		if err := validate(tmp); err != nil {
			return fmt.Errorf("validate temporary file: %w", err)
		}
	}
	if err := ops.Rename(tmp, dest); err != nil {
		return fmt.Errorf("rename temporary file: %w", err)
	}
	renamed = true
	if err := ops.SyncDir(filepath.Dir(dest)); err != nil {
		uncertain := &CommitOutcomeUncertainError{Path: dest, Cause: err}
		if validate == nil {
			uncertain.ReconcileError = fmt.Errorf("no expected-content validator")
		} else if reconcileErr := validate(dest); reconcileErr != nil {
			uncertain.ReconcileError = reconcileErr
		} else {
			uncertain.CanonicalMatchesExpected = true
		}
		return uncertain
	}
	return nil
}

// WriteFile atomically replaces path and attempts to make the namespace update
// durable. A post-rename sync failure returns CommitOutcomeUncertainError.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return WriteFileWithOps(path, data, perm, Ops{})
}

// WriteFileWithOps is WriteFile with injectable durability operations.
func WriteFileWithOps(path string, data []byte, perm os.FileMode, ops Ops) error {
	f, err := NewTemp(path, perm)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err != nil {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return fmt.Errorf("write temporary file: %w", err)
	}
	if n != len(data) {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return fmt.Errorf("short temporary write: wrote %d of %d bytes", n, len(data))
	}
	return CommitWithOps(f, path, func(canonical string) error {
		got, err := os.ReadFile(canonical)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, data) {
			return fmt.Errorf("content mismatch: got %d bytes, want %d", len(got), len(data))
		}
		return nil
	}, ops)
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
