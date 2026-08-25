// Package bundle 实现内容寻址的任务资源包存储：将 flow/proto/Lua/codec 等资源
// 构建为 zip 归档，按 sha256 摘要落盘复用，并以受限并发流式下发。
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

// 资源包下发参数校验错误：ErrBundleArgument 表示摘要长度非法或偏移为负，
// ErrBundleOffset 表示下发偏移超出归档大小。
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

// NewStore 创建以 root 为根目录的资源包存储；maxConcurrentStreams 限制同时打开的
// 下发句柄数（<=0 时取 128）。
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

// GetBuffer 从复用池租借 bundleChunkSize 大小的临时缓冲，供下发流分块读取使用。
func (s *Store) GetBuffer() []byte { return s.buffers.Get().(*[bundleChunkSize]byte)[:] }

// PutBuffer 归还 GetBuffer 租借的缓冲；容量不符的切片直接丢弃，归还前清零内容
// 防止经池钉住已读取的归档数据。
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

// Build 将 Source 暴露的任务资源（flow.json、proto、Lua 脚本、codec 与 errors.json）
// 打包为 Deflate zip，按内容 sha256 寻址经临时文件原子落盘；同摘要归档已存在时
// 校验大小一致后直接复用。返回归档描述符。
func (s *Store) Build(cfg Source) (Descriptor, error) {
	if cfg == nil || len(cfg.BundleFlowJSON()) == 0 {
		return Descriptor{}, errors.New("任务缺少 flow.json")
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
			return nil, errors.New("资源包路径重复: adapter/errors.json")
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

// Open 打开摘要对应的归档并定位到 offset，返回文件句柄、归档总大小与释放函数。
// 并发受 streamSem 限制（ctx 取消时等待让位）；调用方必须关闭文件并调用释放函数归还信号量。
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
