package bundle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const bundleChunkSize = 256 << 10

var bundleModTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

var (
	ErrBundleArgument = errors.New("资源包参数无效")
	ErrBundleOffset   = errors.New("资源包偏移无效")
)

// Descriptor identifies an immutable archive by the digest of its bytes.
type Descriptor struct {
	Digest [sha256.Size]byte
	Size   int64
}

type bundleEntry struct {
	name string
	data []byte
}

// Store owns immutable, content-addressed task resource archives.
type Store struct {
	root      string
	streamSem chan struct{}
	buffers   sync.Pool
}

func NewStore(root string, maxConcurrentStreams int) (*Store, error) {
	if maxConcurrentStreams <= 0 {
		maxConcurrentStreams = 128
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("创建资源包目录失败: %w", err)
	}
	store := &Store{root: root, streamSem: make(chan struct{}, maxConcurrentStreams)}
	store.buffers.New = func() any { return new([bundleChunkSize]byte) }
	return store, nil
}

func (s *Store) GetBuffer() []byte { return s.buffers.Get().(*[bundleChunkSize]byte)[:] }

func (s *Store) PutBuffer(buffer []byte) {
	if cap(buffer) != bundleChunkSize {
		return
	}
	clear(buffer)
	s.buffers.Put((*[bundleChunkSize]byte)(buffer[:bundleChunkSize]))
}

// Source exposes only the immutable task resources needed to build a bundle.
type Source interface {
	BundleFlowJSON() []byte
	BundleProtoFiles() map[string][]byte
	BundleLuaScripts() map[string][]byte
	BundleCodecs() map[string][]byte
	BundleErrorMap() []byte
}

func (s *Store) Build(cfg Source) (Descriptor, error) {
	if cfg == nil || len(cfg.BundleFlowJSON()) == 0 {
		return Descriptor{}, fmt.Errorf("任务缺少 flow.json")
	}
	entries, err := collectBundleEntries(cfg)
	if err != nil {
		return Descriptor{}, err
	}
	tmp, err := os.CreateTemp(s.root, ".bundle-*.tmp")
	if err != nil {
		return Descriptor{}, fmt.Errorf("创建资源包临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()

	hash := sha256.New()
	zw := zip.NewWriter(io.MultiWriter(tmp, hash))
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.Modified = bundleModTime
		header.SetMode(0o644)
		writer, createErr := zw.CreateHeader(header)
		if createErr != nil {
			_ = zw.Close()
			return Descriptor{}, fmt.Errorf("创建资源包条目 %s 失败: %w", entry.name, createErr)
		}
		if _, writeErr := io.Copy(writer, bytes.NewReader(entry.data)); writeErr != nil {
			_ = zw.Close()
			return Descriptor{}, fmt.Errorf("写入资源包条目 %s 失败: %w", entry.name, writeErr)
		}
	}
	if err := zw.Close(); err != nil {
		return Descriptor{}, fmt.Errorf("完成资源包失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return Descriptor{}, fmt.Errorf("同步资源包失败: %w", err)
	}
	info, err := tmp.Stat()
	if err != nil {
		return Descriptor{}, fmt.Errorf("读取资源包大小失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Descriptor{}, fmt.Errorf("关闭资源包失败: %w", err)
	}

	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	finalName := filepath.Join(s.root, hex.EncodeToString(digest[:])+".zip")
	if existing, statErr := os.Stat(finalName); statErr == nil {
		if existing.Size() != info.Size() {
			return Descriptor{}, fmt.Errorf("资源包摘要冲突: %s", finalName)
		}
		return Descriptor{Digest: digest, Size: info.Size()}, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Descriptor{}, fmt.Errorf("检查资源包失败: %w", statErr)
	}
	if err := os.Rename(tmpName, finalName); err != nil {
		if existing, statErr := os.Stat(finalName); statErr == nil && existing.Size() == info.Size() {
			return Descriptor{Digest: digest, Size: info.Size()}, nil
		}
		return Descriptor{}, fmt.Errorf("发布资源包失败: %w", err)
	}
	keep = true
	return Descriptor{Digest: digest, Size: info.Size()}, nil
}

func collectBundleEntries(cfg Source) ([]bundleEntry, error) {
	entries := []bundleEntry{{name: "flow/flow.json", data: append([]byte(nil), cfg.BundleFlowJSON()...)}}
	seen := map[string]struct{}{"flow/flow.json": {}}
	appendMap := func(dir string, files map[string][]byte) error {
		for name, data := range files {
			clean, err := normalizeBundlePath(dir, name)
			if err != nil {
				return err
			}
			if _, exists := seen[clean]; exists {
				return fmt.Errorf("资源包路径重复: %s", clean)
			}
			seen[clean] = struct{}{}
			entries = append(entries, bundleEntry{name: clean, data: append([]byte(nil), data...)})
		}
		return nil
	}
	if err := appendMap("proto", cfg.BundleProtoFiles()); err != nil {
		return nil, err
	}
	if err := appendMap("scripts", cfg.BundleLuaScripts()); err != nil {
		return nil, err
	}
	if err := appendMap("adapter", cfg.BundleCodecs()); err != nil {
		return nil, err
	}
	if len(cfg.BundleErrorMap()) > 0 {
		if _, exists := seen["adapter/errors.json"]; exists {
			return nil, fmt.Errorf("资源包路径重复: adapter/errors.json")
		}
		entries = append(entries, bundleEntry{name: "adapter/errors.json", data: append([]byte(nil), cfg.BundleErrorMap()...)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries, nil
}

func normalizeBundlePath(dir, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("资源包路径无效: %q", name)
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if name == "" || strings.HasPrefix(name, "/") || path.IsAbs(name) {
		return "", fmt.Errorf("资源包路径无效: %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("资源包路径越界: %q", name)
	}
	return path.Join(dir, clean), nil
}

func (s *Store) Open(ctx context.Context, digest []byte, offset int64) (*os.File, int64, func(), error) {
	if len(digest) != sha256.Size || offset < 0 {
		return nil, 0, nil, ErrBundleArgument
	}
	select {
	case s.streamSem <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, nil, ctx.Err()
	}
	release := func() { <-s.streamSem }
	name := filepath.Join(s.root, hex.EncodeToString(digest)+".zip")
	file, err := os.Open(name)
	if err != nil {
		release()
		return nil, 0, nil, err
	}
	info, err := file.Stat()
	if err != nil || offset > info.Size() {
		_ = file.Close()
		release()
		if err != nil {
			return nil, 0, nil, err
		}
		return nil, 0, nil, fmt.Errorf("%w: %d 超出大小 %d", ErrBundleOffset, offset, info.Size())
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		release()
		return nil, 0, nil, err
	}
	return file, info.Size(), release, nil
}
