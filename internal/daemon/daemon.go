// Package daemon 提供把当前进程转为守护进程的能力：父进程 fork 子进程后
// 立即返回，子进程以 ppid=1 继续执行原逻辑。
// Windows 无法 fork。本函数只向 stderr 打印提示并返回 nil；三个入口在调用后
// 都直接 return 0，因此 Windows 上 -d 或配置 daemon=true 会打印提示后退出，
// 不会转成前台继续运行。
package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Daemon 将当前进程转为守护进程。
// 通过 os.Getppid() != 1 判断是否需要 fork：父进程 fork 出子进程后由调用方退出，
// 子进程的 ppid 为 1，再次调用本函数时跳过，继续正常执行。
// skip 参数指定需要从子进程参数中过滤的标志（如 "-d"）。
//
// 返回非 nil 表示后台化失败：调用方必须据此以非零码退出，不能继续按"已后台化"
// 的路径 return 0——那会让 fork 失败的进程看起来像启动成功。
func Daemon(skip ...string) error {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "[DAEMON] Windows 不支持守护进程模式，进程即将退出。请去掉 -d、保持配置 daemon = false，或改用服务包装器前台运行")
		return nil
	}

	if os.Getppid() == 1 {
		return nil
	}

	filePath, err := filepath.Abs(os.Args[0])
	if err != nil {
		return fmt.Errorf("解析可执行文件绝对路径失败（%s）: %w", os.Args[0], err)
	}
	newCmd := []string{os.Args[0]}
	add := 0
	for _, v := range os.Args[1:] {
		if add == 1 {
			add = 0
			continue
		}
		add = 0
		for _, s := range skip {
			if strings.Contains(v, s) {
				if strings.Contains(v, "--") {
					add = 2
				} else {
					add = 1
				}
				break
			}
		}
		if add == 0 {
			newCmd = append(newCmd, v)
		}
	}
	cmd := exec.Command(filePath)
	cmd.Args = newCmd
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动守护子进程失败（%s）: %w", filePath, err)
	}
	return nil
}
