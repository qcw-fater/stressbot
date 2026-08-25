// Package stresslog 提供基于 zap + lumberjack 的全局日志：控制台与轮转文件
// 双输出、级别可调，DPanic 及以上级别自动推送企业微信告警。
package stresslog

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// FormatProcessInfo 函数返回包含进程信息的字符串
// 返回值data是一个包含进程信息的字符串
func FormatProcessInfo() (data string) {
	// 进程信息
	hostName, _ := os.Hostname()
	p, err := process.NewProcess(int32(os.Getpid()))
	if err == nil {
		// 启动账户
		UserName, _ := p.Username()
		data += "UserName: " + UserName + "\n"
		// 进程启动时间
		StartTime, _ := p.CreateTime()
		data += "StartTime: " + time.Unix(StartTime/1000, 0).Format("2006-01-02 15:04:05") + "\n"
		// 进程路径
		WorkingDirectory, _ := p.Cwd()
		data += "Directory: " + WorkingDirectory + "\n"
		// 主机名
		data += "HostName: " + hostName + "\n"
	}
	return
}

// GetLocalIP 函数用于获取本地局域网IP地址
func GetLocalIP() string {
	addr, err := net.InterfaceAddrs()
	if err != nil {
		Error("", zap.Error(err))
		return ""
	}
	for _, address := range addr {
		// 检查ip地址判断是否回环地址
		if inet, ok := address.(*net.IPNet); ok && !inet.IP.IsLoopback() && inet.IP.To4() != nil {
			if inet.IP.To4() != nil {
				return inet.IP.String()
			}
		}
	}
	return ""
}

// PushPanicMsgToQYWX 把 DPanic 及以上级别的日志条目拼装为带调用位置、
// 进程信息与堆栈的 Markdown 告警文本，推送到企业微信 webhook。
func PushPanicMsgToQYWX(entry zapcore.Entry) {
	data := "# " + entry.Message + "\n"
	data += "Time: " + time.Now().Format("2006-01-02 15:04:05") + "\n"
	data += "File: " + entry.Caller.File + "\n"
	data += fmt.Sprintf("Line: %d\n", entry.Caller.Line)
	data += "Func: " + entry.Caller.Function + "\n"
	data += "GOVersion: " + runtime.Version() + "\n"
	data += "GOOS-GOARCH: " + runtime.GOOS + "-" + runtime.GOARCH + "\n"
	data += "IP: " + GetLocalIP() + "\n"
	data += "ServerVersion: " + "null" + "\n"

	data += FormatProcessInfo()

	data += "堆栈信息:\n"
	data += entry.Stack

	postQYWXMsg(data)
}
