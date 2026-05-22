package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Daemon 将当前进程转为守护进程。
// 通过 os.Getppid() != 1 判断是否需要 fork：父进程 fork 子进程后直接退出，
// 子进程的 ppid 为 1，再次调用本函数时跳过，继续正常执行。
// skip 参数指定需要从子进程参数中过滤的标志（如 "-d"）。
func Daemon(skip ...string) {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "[DAEMON] Windows 不支持守护进程模式，将以前台模式运行")
		return
	}

	if os.Getppid() != 1 {
		filePath, _ := filepath.Abs(os.Args[0])
		newCmd := []string{os.Args[0]}
		add := 0
		for _, v := range os.Args[1:] {
			if add == 1 {
				add = 0
				continue
			} else {
				add = 0
			}
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
		_ = cmd.Start()
	}
}
