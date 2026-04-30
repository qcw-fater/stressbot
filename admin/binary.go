package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var validFilenameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// BinaryStore 二进制文件存储。
type BinaryStore struct {
	dir   string
	mu    sync.RWMutex
	metas map[string]BinaryMeta
}

func NewBinaryStore(dir string) (*BinaryStore, error) {
	fullDir := filepath.Join(dir, "binaries")
	if err := os.MkdirAll(fullDir, 0o755); err != nil {
		return nil, fmt.Errorf("create binaries dir: %w", err)
	}

	bs := &BinaryStore{
		dir:   fullDir,
		metas: make(map[string]BinaryMeta),
	}
	bs.scanDir()
	return bs, nil
}

func (bs *BinaryStore) scanDir() {
	entries, err := os.ReadDir(bs.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) == ".sha256" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		shaFile := e.Name() + ".sha256"
		sha256Hex, _ := os.ReadFile(filepath.Join(bs.dir, shaFile))

		meta := BinaryMeta{
			Filename:   e.Name(),
			SHA256:     string(sha256Hex),
			SizeBytes:  info.Size(),
			UploadedAt: info.ModTime(),
		}
		bs.metas[e.Name()] = meta
	}
}

// Upload 上传二进制文件。
func (bs *BinaryStore) Upload(r io.Reader, filename, version, osName, arch string, force bool) (BinaryMeta, error) {
	if !validFilenameRe.MatchString(filename) {
		return BinaryMeta{}, ErrInvalidArgument.WithMessage("filename contains invalid characters")
	}

	bs.mu.Lock()
	defer bs.mu.Unlock()

	if _, exists := bs.metas[filename]; exists && !force {
		return BinaryMeta{}, ErrTaskConflict.WithMessage("binary already exists, use force to overwrite")
	}

	// 原子写入：临时文件 → rename
	tmpPath := filepath.Join(bs.dir, filename+".tmp")
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return BinaryMeta{}, fmt.Errorf("create temp file: %w", err)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)

	written, err := io.Copy(writer, r)
	f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return BinaryMeta{}, fmt.Errorf("write file: %w", err)
	}

	sha256Hex := hex.EncodeToString(hasher.Sum(nil))

	// 写 SHA256 sidecar
	shaPath := filepath.Join(bs.dir, filename+".sha256")
	if err := os.WriteFile(shaPath, []byte(sha256Hex), 0o644); err != nil {
		os.Remove(tmpPath)
		return BinaryMeta{}, fmt.Errorf("write sha256 file: %w", err)
	}

	// Rename 临时文件到最终路径
	dstPath := filepath.Join(bs.dir, filename)
	if err := os.Rename(tmpPath, dstPath); err != nil {
		os.Remove(tmpPath)
		os.Remove(shaPath)
		return BinaryMeta{}, fmt.Errorf("rename: %w", err)
	}

	meta := BinaryMeta{
		Version:    version,
		Filename:   filename,
		OS:         osName,
		Arch:       arch,
		SHA256:     sha256Hex,
		SizeBytes:  written,
		UploadedAt: time.Now(),
	}
	bs.metas[filename] = meta
	return meta, nil
}

// List 列出所有二进制。
func (bs *BinaryStore) List() []BinaryMeta {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	out := make([]BinaryMeta, 0, len(bs.metas))
	for _, m := range bs.metas {
		out = append(out, m)
	}
	return out
}

// Get 获取二进制元信息。
func (bs *BinaryStore) Get(filename string) (BinaryMeta, bool) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	m, ok := bs.metas[filename]
	return m, ok
}

// Open 打开二进制文件。
func (bs *BinaryStore) Open(filename string) (io.ReadCloser, error) {
	if !validFilenameRe.MatchString(filename) {
		return nil, ErrBinaryNotFound
	}
	f, err := os.Open(filepath.Join(bs.dir, filename))
	if err != nil {
		return nil, ErrBinaryNotFound
	}
	return f, nil
}

// Delete 删除二进制文件。
func (bs *BinaryStore) Delete(filename string) error {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	if _, ok := bs.metas[filename]; !ok {
		return ErrBinaryNotFound
	}

	os.Remove(filepath.Join(bs.dir, filename))
	os.Remove(filepath.Join(bs.dir, filename+".sha256"))
	delete(bs.metas, filename)
	return nil
}
