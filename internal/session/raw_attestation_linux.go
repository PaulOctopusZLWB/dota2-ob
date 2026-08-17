//go:build linux

package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"
)

// AttestedRawSummary describes bytes actually consumed from one still-bound
// regular-file descriptor. It deliberately exposes no file content.
type AttestedRawSummary struct {
	SHA256 string
	Bytes  int64
	Lines  uint64
	Device uint64
	Inode  uint64
}

// ReadAttestedRaw frames a raw session without trusting a path pre-scan. The
// opened descriptor is bound before reading, the read itself is hard-limited to
// totalLimit+1, and descriptor/path identity and size are reconciled afterward.
func ReadAttestedRaw(path string, totalLimit, lineLimit int64, consume func([]byte, uint64) error) (AttestedRawSummary, error) {
	if totalLimit <= 0 || lineLimit <= 1 || consume == nil {
		return AttestedRawSummary{}, errors.New("invalid attested raw limits")
	}
	file, err := os.Open(path)
	if err != nil {
		return AttestedRawSummary{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return AttestedRawSummary{}, errors.New("raw descriptor is not regular")
	}
	openedStat, ok := opened.Sys().(*syscall.Stat_t)
	if !ok {
		return AttestedRawSummary{}, errors.New("raw descriptor identity unavailable")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return AttestedRawSummary{}, errors.New("raw path cannot be inspected")
	}
	pathStat, pathOK := pathInfo.Sys().(*syscall.Stat_t)
	if !pathOK || pathInfo.Mode()&os.ModeSymlink != 0 || pathStat.Dev != openedStat.Dev || pathStat.Ino != openedStat.Ino {
		return AttestedRawSummary{}, errors.New("raw path does not own opened descriptor")
	}
	hash := sha256.New()
	limited := &countingRawReader{reader: io.LimitReader(file, totalLimit+1)}
	reader := bufio.NewReaderSize(io.TeeReader(limited, hash), int(min64(lineLimit+1, 64<<10)))
	var lines uint64
	for {
		line := make([]byte, 0, minInt64(lineLimit, 64<<10))
		fragment, readErr := reader.ReadSlice('\n')
		for readErr == bufio.ErrBufferFull {
			if int64(len(line))+int64(len(fragment)) > lineLimit {
				return AttestedRawSummary{}, errors.New("raw line limit exceeded")
			}
			line = append(line, fragment...)
			fragment, readErr = reader.ReadSlice('\n')
		}
		if readErr == nil {
			fragment = fragment[:len(fragment)-1]
		}
		if int64(len(line))+int64(len(fragment)) > lineLimit {
			return AttestedRawSummary{}, errors.New("raw line limit exceeded")
		}
		line = append(line, fragment...)
		if readErr == io.EOF {
			if len(line) != 0 {
				return AttestedRawSummary{}, errors.New("raw input has unterminated frame")
			}
			break
		}
		if readErr != nil {
			return AttestedRawSummary{}, readErr
		}
		if limited.read > totalLimit {
			return AttestedRawSummary{}, errors.New("raw total limit exceeded")
		}
		lines++
		if err := consume(line, lines); err != nil {
			return AttestedRawSummary{}, err
		}
	}
	if limited.read > totalLimit {
		return AttestedRawSummary{}, errors.New("raw total limit exceeded")
	}
	closed, err := file.Stat()
	if err != nil {
		return AttestedRawSummary{}, errors.New("raw descriptor cannot be rechecked")
	}
	closedStat, closedOK := closed.Sys().(*syscall.Stat_t)
	pathInfo, pathErr := os.Lstat(path)
	if pathErr != nil {
		return AttestedRawSummary{}, errors.New("raw path cannot be rechecked")
	}
	pathStat, pathOK = pathInfo.Sys().(*syscall.Stat_t)
	if !closedOK || !pathOK || pathInfo.Mode()&os.ModeSymlink != 0 ||
		closedStat.Dev != openedStat.Dev || closedStat.Ino != openedStat.Ino || pathStat.Dev != openedStat.Dev || pathStat.Ino != openedStat.Ino ||
		closed.Size() != limited.read || opened.Size() > totalLimit || closed.Size() != opened.Size() {
		return AttestedRawSummary{}, errors.New("raw descriptor/path changed during admission")
	}
	return AttestedRawSummary{SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: limited.read, Lines: lines, Device: uint64(openedStat.Dev), Inode: openedStat.Ino}, nil
}

type countingRawReader struct {
	reader io.Reader
	read   int64
}

func (r *countingRawReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += int64(n)
	return n, err
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func minInt64(a, b int64) int {
	return int(min64(a, b))
}
