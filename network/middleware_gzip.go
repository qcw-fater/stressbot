package network

import (
	"bytes"
	"compress/gzip"
	"fmt"

	"go.uber.org/zap"
	stresslog "stressbot/utils/log"
)

// GzipMiddleware 标准 gzip 压缩/解压中间件。
// Send: 客户端通常不压缩（no-op）。
// Recv: 检查压缩标志位，解压 body。
func GzipMiddleware(cfg ProtocolConfig) PacketMiddleware {
	compressFlagBit := -1
	if cfg.CompressFlag != nil {
		compressFlagBit = cfg.CompressFlag.Bit
	}

	return func(ctx *PacketContext, next func()) {
		switch ctx.Direction {
		case PacketSend:
			// 客户端通常不压缩
			next()

		case PacketRecv:
			if compressFlagBit >= 0 && ctx.HasFlag(compressFlagBit) {
				decompressed, err := gzipDecompress(ctx.Body)
				if err != nil {
					stresslog.Error("[PROTOCOL] gzip 解压失败",
						zap.Error(err), zap.Int("bodyLen", len(ctx.Body)))
					ctx.SetError(fmt.Errorf("gzip 解压失败: %w", err))
					return
				}
				ctx.Body = decompressed
			}
			next()
		}
	}
}

func gzipDecompress(body []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
