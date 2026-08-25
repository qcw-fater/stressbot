package stresslog

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	fileBufferSize  = 256 * 1024
	fileFlushPeriod = time.Second
)

// logger 负责高性能结构化日志；sugarLogger 兼容少量格式化调用。
var logger *zap.Logger
var sugarLogger *zap.SugaredLogger

var loglevel = zap.NewAtomicLevelAt(zapcore.InfoLevel)
var activeFileSink atomic.Pointer[fileLogSink]

// Config 是日志配置。
type Config struct {
	Path         string `toml:"path"         json:"path"         yaml:"path"`
	PrintConsole bool   `toml:"printConsole" json:"printConsole" yaml:"printConsole"`
	LogLevel     string `toml:"level"        json:"level"        yaml:"logLevel"`
	MaxSize      int    `toml:"maxSizeMB"    json:"maxSizeMB"    yaml:"maxSizeMB"`
	MaxBackups   int    `toml:"maxBackups"   json:"maxBackups"   yaml:"maxBackups"`
	MaxAge       int    `toml:"maxAge"       json:"maxAge"       yaml:"maxAge"`
	LocalTime    bool   `toml:"localTime"    json:"localTime"    yaml:"localTime"`
	Compress     bool   `toml:"compress"     json:"compress"     yaml:"compress"`
	WeChatToken  string `toml:"weChatToken"  json:"weChatToken"  yaml:"weChatToken"`
}

type fileLogSink struct {
	buffered *zapcore.BufferedWriteSyncer
	roller   *lumberjack.Logger
	once     sync.Once
	err      error
}

func (s *fileLogSink) Sync() error {
	if s == nil {
		return nil
	}
	return s.buffered.Sync()
}

func (s *fileLogSink) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.err = errors.Join(s.buffered.Stop(), s.roller.Close())
	})
	return s.err
}

// noSyncWriter 避免 logger.Sync 对 stdout 调用 fsync。在 Windows 控制台和管道中，
// fsync 经常返回无意义的 invalid handle；文件 sink 仍由 BufferedWriteSyncer 正常刷新。
type noSyncWriter struct {
	io.Writer
}

func (w noSyncWriter) Sync() error { return nil }

func defaultConfig() *Config {
	return &Config{
		Path:         "log/stressbot.log",
		PrintConsole: true,
		LogLevel:     "debug",
		MaxSize:      500,
		MaxBackups:   1,
		MaxAge:       7,
		LocalTime:    true,
		Compress:     true,
	}
}

func normalizedConfig(conf *Config, buildLogLevel string) Config {
	effective := *defaultConfig()
	if conf != nil {
		effective = *conf
		if effective.LogLevel == "" {
			effective.LogLevel = "debug"
		}
		if effective.MaxSize == 0 {
			effective.MaxSize = 500
		}
		if effective.MaxBackups == 0 {
			effective.MaxBackups = 1
		}
		if effective.MaxAge == 0 {
			effective.MaxAge = 7
		}
	}
	if buildLogLevel != "" {
		effective.LogLevel = buildLogLevel
	}
	return effective
}

func trimmedPath(path string) string {
	short := path
	for i := len(path) - 1; i > 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			short = path[i+1:]
			break
		}
	}
	return short
}

func callerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(trimmedPath(caller.String()))
}

func utcRFC3339NanoTimeEncoder(value time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(value.UTC().Format(time.RFC3339Nano))
}

func fileEncoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		MessageKey:     "message",
		LevelKey:       "level",
		TimeKey:        "timestamp",
		CallerKey:      "caller",
		NameKey:        "logger",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     utcRFC3339NanoTimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   callerEncoder,
	}
}

func consoleEncoderConfig() zapcore.EncoderConfig {
	config := fileEncoderConfig()
	config.EncodeLevel = zapcore.LowercaseColorLevelEncoder
	config.EncodeTime = zapcore.TimeEncoderOfLayout("2006/01/02 15:04:05.000000Z0700")
	return config
}

// InitLog 初始化日志并返回幂等关闭函数。关闭会刷新缓冲并关闭轮转文件。
// 调用方应在完成初始化后立即 defer 返回的函数。
func InitLog(logPath, serviceName string, conf *Config, buildLogLevel string) func() error {
	effective := normalizedConfig(conf, buildLogLevel)
	SetWechatToken(effective.WeChatToken)
	loglevel = zap.NewAtomicLevelAt(StrToLevel(effective.LogLevel))

	roller := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    effective.MaxSize,
		MaxBackups: effective.MaxBackups,
		MaxAge:     effective.MaxAge,
		LocalTime:  effective.LocalTime,
		Compress:   effective.Compress,
	}
	buffered := &zapcore.BufferedWriteSyncer{
		WS:            zapcore.AddSync(roller),
		Size:          fileBufferSize,
		FlushInterval: fileFlushPeriod,
	}
	sink := &fileLogSink{buffered: buffered, roller: roller}

	cores := []zapcore.Core{
		zapcore.NewCore(zapcore.NewJSONEncoder(fileEncoderConfig()), buffered, loglevel),
	}
	if effective.PrintConsole {
		console := zapcore.AddSync(noSyncWriter{Writer: os.Stdout})
		cores = append(cores, zapcore.NewCore(zapcore.NewConsoleEncoder(consoleEncoderConfig()), console, loglevel))
	}

	options := []zap.Option{
		zap.AddCaller(),
		zap.AddStacktrace(zap.DPanicLevel),
		zap.AddCallerSkip(1),
		zap.Hooks(func(entry zapcore.Entry) error {
			if entry.Level >= zap.DPanicLevel {
				PushPanicMsgToQYWX(entry)
			}
			return nil
		}),
		zap.Fields(zap.String("service", serviceName)),
	}
	logger = zap.New(zapcore.NewTee(cores...), options...)
	sugarLogger = logger.Sugar()

	if previous := activeFileSink.Swap(sink); previous != nil {
		_ = previous.Close()
	}

	return func() error {
		activeFileSink.CompareAndSwap(sink, nil)
		return sink.Close()
	}
}

// GetLogger 返回当前全局 zap logger；未调用 InitLog 前为 nil。
func GetLogger() *zap.Logger {
	return logger
}

// ReplaceLogger 为测试提供无输出 logger 注入能力。
func ReplaceLogger(replacement *zap.Logger) {
	logger = replacement
	sugarLogger = replacement.Sugar()
}

func syncFile() {
	if sink := activeFileSink.Load(); sink != nil {
		_ = sink.Sync()
	}
}

// Debug 以结构化字段打 debug 级别日志。
func Debug(msg string, fields ...zap.Field) { logger.Debug(msg, fields...) }

// Info 以结构化字段打 info 级别日志。
func Info(msg string, fields ...zap.Field) { logger.Info(msg, fields...) }

// Warn 以结构化字段打 warn 级别日志。
func Warn(msg string, fields ...zap.Field) { logger.Warn(msg, fields...) }

// Error 以结构化字段打 error 级别日志，并立即刷新文件缓冲避免崩溃丢日志。
func Error(msg string, fields ...zap.Field) {
	logger.Error(msg, fields...)
	syncFile()
}

// DPanic 以结构化字段打 dpanic 级别日志（开发态触发 panic），并立即刷新文件缓冲。
func DPanic(msg string, fields ...zap.Field) {
	logger.DPanic(msg, fields...)
	syncFile()
}

// Fatal 以结构化字段打 fatal 级别日志，随后由 zap 退出进程。
func Fatal(msg string, fields ...zap.Field) { logger.Fatal(msg, fields...) }

// DebugS 以松散键值对打 debug 级别日志。
func DebugS(msg string, keysAndValues ...any) { sugarLogger.Debugw(msg, keysAndValues...) }

// InfoS 以松散键值对打 info 级别日志。
func InfoS(msg string, keysAndValues ...any) { sugarLogger.Infow(msg, keysAndValues...) }

// WarnS 以松散键值对打 warn 级别日志。
func WarnS(msg string, keysAndValues ...any) { sugarLogger.Warnw(msg, keysAndValues...) }

// ErrorS 以松散键值对打 error 级别日志，并立即刷新文件缓冲。
func ErrorS(msg string, keysAndValues ...any) {
	sugarLogger.Errorw(msg, keysAndValues...)
	syncFile()
}

// DPanicS 以松散键值对打 dpanic 级别日志，并立即刷新文件缓冲。
func DPanicS(msg string, keysAndValues ...any) {
	sugarLogger.DPanicw(msg, keysAndValues...)
	syncFile()
}

// DebugF 按 printf 模板打 debug 级别日志。
func DebugF(template string, args ...any) { sugarLogger.Debugf(template, args...) }

// InfoF 按 printf 模板打 info 级别日志。
func InfoF(template string, args ...any) { sugarLogger.Infof(template, args...) }

// WarnF 按 printf 模板打 warn 级别日志。
func WarnF(template string, args ...any) { sugarLogger.Warnf(template, args...) }

// ErrorF 按 printf 模板打 error 级别日志，并立即刷新文件缓冲。
func ErrorF(template string, args ...any) {
	sugarLogger.Errorf(template, args...)
	syncFile()
}

// DPanicF 按 printf 模板打 dpanic 级别日志，并立即刷新文件缓冲。
func DPanicF(template string, args ...any) {
	sugarLogger.DPanicf(template, args...)
	syncFile()
}

// FatalF 按 printf 模板打 fatal 级别日志，随后由 zap 退出进程。
func FatalF(template string, args ...any) { sugarLogger.Fatalf(template, args...) }

// SetLogLevel 原子调整全局日志级别，对文件与控制台输出同时生效。
func SetLogLevel(level zapcore.Level) { loglevel.SetLevel(level) }

// GetLogLevel 返回当前全局日志级别。
func GetLogLevel() zapcore.Level { return loglevel.Level() }

// DebugEnabled 报告当前是否启用 debug 级别日志。
func DebugEnabled() bool { return LevelEnabled(zapcore.DebugLevel) }

// LevelEnabled 报告指定级别是否被当前全局级别放行；logger 未初始化时恒为 false。
func LevelEnabled(level zapcore.Level) bool {
	if logger == nil {
		return false
	}
	return loglevel.Enabled(level)
}

// StrToLevel 把配置字符串解析为 zap 级别：debug/info/warn/error 之外的取值回退 info。
func StrToLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
