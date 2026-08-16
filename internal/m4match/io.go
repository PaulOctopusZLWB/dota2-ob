package m4match

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func canonical(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeJSON(path string, value any, mode os.FileMode) error {
	payload, err := canonical(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, payload, mode)
}

func writePrivate(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o600)
}

func fileSHA(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), n, nil
}

func payloadSHA(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func safeRoot(root, repo string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil || root == "" {
		return "", errors.New("data root must be an absolute-resolvable non-empty path")
	}
	abs = filepath.Clean(abs)
	protected := []string{filepath.Clean(repo), "/home/paul-zhang/文档/dota2_ob", "/", filepath.Clean(os.Getenv("HOME"))}
	for _, candidate := range protected {
		if candidate == "." || candidate == "" {
			continue
		}
		if abs == candidate || strings.HasPrefix(abs, candidate+string(os.PathSeparator)) {
			return "", errors.New("data root is inside a protected repository, canonical, home, or filesystem root")
		}
	}
	return abs, nil
}

func sortEvidence(e *Evidence) {
	sort.Slice(e.Checks, func(i, j int) bool { return e.Checks[i].ID < e.Checks[j].ID })
	sort.Slice(e.CandidateIdentityEvidence.Checks, func(i, j int) bool {
		return e.CandidateIdentityEvidence.Checks[i].ID < e.CandidateIdentityEvidence.Checks[j].ID
	})
	sort.Strings(e.Faults)
	sort.Slice(e.Artifacts, func(i, j int) bool { return e.Artifacts[i].Path < e.Artifacts[j].Path })
}
