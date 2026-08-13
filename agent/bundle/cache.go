package bundle

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"stressbot/controlplane/pb"
)

type Cache struct {
	root string
	mu   sync.Mutex
}

func NewCache(root string) (*Cache, error) {
	root = filepath.Join(root, "stressbot-bundles")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("创建资源包缓存目录失败: %w", err)
	}
	return &Cache{root: root}, nil
}

func (c *Cache) Ensure(ctx context.Context, client controlpb.AgentBundleServiceClient, agentID string, digest []byte, size int64) (string, error) {
	if client == nil || len(digest) != sha256.Size || size <= 0 {
		return "", fmt.Errorf("资源包参数无效")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	hexDigest := hex.EncodeToString(digest)
	published := filepath.Join(c.root, hexDigest)
	if info, err := os.Stat(filepath.Join(published, ".ready")); err == nil && info.Mode().IsRegular() {
		return published, nil
	}
	archive := filepath.Join(c.root, hexDigest+".zip")
	if err := verifyBundleFile(archive, digest, size); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(archive)
		}
		if err := c.download(ctx, client, agentID, digest, size, archive); err != nil {
			return "", err
		}
	}
	if err := extractBundle(archive, published); err != nil {
		return "", err
	}
	return published, nil
}

func (c *Cache) download(ctx context.Context, client controlpb.AgentBundleServiceClient, agentID string, digest []byte, size int64, archive string) error {
	return c.downloadAttempt(ctx, client, agentID, digest, size, archive, true)
}

func (c *Cache) downloadAttempt(ctx context.Context, client controlpb.AgentBundleServiceClient, agentID string, digest []byte, size int64, archive string, retryCorrupt bool) error {
	part := archive + ".part"
	file, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("打开资源包临时文件失败: %w", err)
	}
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		_ = file.Close()
		return err
	}
	if offset > size {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			_ = os.Remove(part)
			return err
		}
		offset, err = file.Seek(0, io.SeekStart)
		if err != nil {
			_ = file.Close()
			_ = os.Remove(part)
			return err
		}
	}
	stream, err := client.DownloadBundle(ctx, &controlpb.BundleRequest{AgentId: agentID, Sha256: digest, Offset: offset})
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("开始下载资源包失败: %w", err)
	}
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			_ = file.Close()
			return fmt.Errorf("下载资源包失败: %w", recvErr)
		}
		if chunk.Offset != offset || chunk.TotalSize != size || !equalDigest(chunk.Sha256, digest) || len(chunk.Data) == 0 {
			_ = file.Close()
			_ = os.Remove(part)
			return fmt.Errorf("资源包分块协议错误: offset=%d want=%d", chunk.Offset, offset)
		}
		if _, err := file.Write(chunk.Data); err != nil {
			_ = file.Close()
			return fmt.Errorf("写入资源包失败: %w", err)
		}
		offset += int64(len(chunk.Data))
		if offset > size {
			_ = file.Close()
			_ = os.Remove(part)
			return fmt.Errorf("资源包大小超出声明值")
		}
	}
	if offset != size {
		_ = file.Close()
		return fmt.Errorf("资源包下载不完整: %d/%d", offset, size)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := verifyBundleFile(part, digest, size); err != nil {
		_ = os.Remove(part)
		if retryCorrupt {
			return c.downloadAttempt(ctx, client, agentID, digest, size, archive, false)
		}
		return err
	}
	_ = os.Remove(archive)
	if err := os.Rename(part, archive); err != nil {
		return fmt.Errorf("发布资源包缓存失败: %w", err)
	}
	return nil
}

func verifyBundleFile(name string, digest []byte, size int64) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != size {
		return fmt.Errorf("资源包大小不一致: %d/%d", info.Size(), size)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if !equalDigest(hash.Sum(nil), digest) {
		return fmt.Errorf("资源包 SHA-256 校验失败")
	}
	return nil
}

func extractBundle(archive, published string) error {
	tmp, err := os.MkdirTemp(filepath.Dir(published), ".extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("打开资源包失败: %w", err)
	}
	defer zr.Close()
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() || !entry.Mode().IsRegular() || entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("资源包包含不允许的条目: %s", entry.Name)
		}
		clean := filepath.Clean(filepath.FromSlash(entry.Name))
		target := filepath.Join(tmp, clean)
		rel, err := filepath.Rel(tmp, target)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("资源包条目越界: %s", entry.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := entry.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = io.Copy(out, rc)
		}
		closeErr := rc.Close()
		if out != nil {
			if syncErr := out.Sync(); err == nil {
				err = syncErr
			}
			if fileCloseErr := out.Close(); err == nil {
				err = fileCloseErr
			}
		}
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	ready, err := os.OpenFile(filepath.Join(tmp, ".ready"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := ready.Close(); err != nil {
		return err
	}
	_ = os.RemoveAll(published)
	if err := os.Rename(tmp, published); err != nil {
		return fmt.Errorf("发布资源包目录失败: %w", err)
	}
	return nil
}

func equalDigest(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for i := range left {
		diff |= left[i] ^ right[i]
	}
	return diff == 0
}
