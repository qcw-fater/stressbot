package stresslog

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// zap 的 JSON 编码器不会为重名字段去重：业务字段一旦和编码器内建键同名，一行 JSON 里就会
// 出现两个同名键，解析器一律取后者，内建值被静默吃掉。真实踩过的案例是 engine/executor.go
// 用 zap.String("caller", 机器人账号) 覆盖了 zap.AddCaller() 写入的源码位置——动作失败日志
// 里定位不到是哪一行报的。
//
// 保留键直接取自 fileEncoderConfig()，改了编码器配置这里自动跟着变。
func TestNoZapFieldsCollideWithEncoderKeys(t *testing.T) {
	config := fileEncoderConfig()
	reserved := map[string]string{
		config.MessageKey:    "MessageKey",
		config.LevelKey:      "LevelKey",
		config.TimeKey:       "TimeKey",
		config.CallerKey:     "CallerKey",
		config.NameKey:       "NameKey",
		config.StacktraceKey: "StacktraceKey",
	}
	delete(reserved, "")

	names := make([]string, 0, len(reserved))
	for key := range reserved {
		names = append(names, regexp.QuoteMeta(key))
	}
	// 匹配 zap.String("caller", ...) / zap.Any("level", ...) 等任意构造器
	pattern := regexp.MustCompile(`zap\.\w+\("(` + strings.Join(names, "|") + `)"`)

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("定位仓库根目录失败: %v", err)
	}

	var offenders []string
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "testdata", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// 本测试文件自身含示例字面量，跳过
		if strings.HasSuffix(path, "reserved_keys_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(content), "\n") {
			for _, match := range pattern.FindAllStringSubmatch(line, -1) {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				offenders = append(offenders, filepath.ToSlash(rel)+":"+strconv.Itoa(i+1)+
					" 使用了保留键 "+match[1]+"（编码器 "+reserved[match[1]]+"）")
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("遍历源码失败: %v", walkErr)
	}

	if len(offenders) > 0 {
		t.Fatalf("以下业务日志字段与 zap 编码器内建键同名，会在 JSON 里产生重复键并覆盖内建值，请改名：\n%s",
			strings.Join(offenders, "\n"))
	}
}
