package log

import (
	"io"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// 只能输出结构化日志，但是性能要高于 SugaredLogger
var logger *zap.Logger

// 可以输出 结构化日志、非结构化日志 性能差于 zap.Logger
var sugarLogger *zap.SugaredLogger

var loglevel zap.AtomicLevel

var defaultConf *Config

// logFilePath 记录 InitLog 传入的日志文件路径，供日志下载 endpoint 使用。
var logFilePath string

// Config 日志配置的结构体
type Config struct {
	PrintConsole bool   `json:"printConsole" yaml:"printConsole"` // 是否控制台输出
	LogLevel     string `json:"level" yaml:"logLevel"`            // 日志等级[debug, info, warn, error]
	MaxSize      int    `json:"maxSize" yaml:"maxSize"`           // 日志文件大小，超过则切割，单位M
	MaxBackups   int    `json:"maxBackups" yaml:"maxBackups"`     // 日志文件最大保留个数
	MaxAge       int    `json:"maxAge" yaml:"maxAge"`             // 日志文件最大保存天数
	LocalTime    bool   `json:"localTime" yaml:"localTime"`       // 是否使用服务器本地时间
	Compress     bool   `json:"compress" yaml:"compress"`         // 日志是否压缩
	WeChatToken  string `json:"weChatToken" yaml:"weChatToken"`   // 企微Hook密钥
}

func defaultConfig() *Config {
	return &Config{
		PrintConsole: true,
		LogLevel:     "debug",
		MaxSize:      500,
		MaxBackups:   1,
		MaxAge:       7,
		LocalTime:    true,
		Compress:     true,
	}
}

// 修剪日志路径，保留最后的文件名和行号
func trimmedPath(str string) string {
	short := str
	for i := len(str) - 1; i > 0; i-- {
		if str[i] == '/' {
			short = str[i+1:]
			break
		}
	}
	return short
}

// 重写zap.CallerEncoder方法，将文件路径转换为短路径
func callerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(trimmedPath(caller.String()))
}

// InitLog 初始化日志 logger
func InitLog(logPath, serviceName string, conf *Config, buildLogLevel string) {
	logFilePath = logPath
	// 填充零值默认
	if conf == nil {
		conf = defaultConfig()
	} else {
		def := defaultConfig()
		if conf.LogLevel == "" {
			conf.LogLevel = def.LogLevel
		}
		if conf.MaxSize == 0 {
			conf.MaxSize = def.MaxSize
		}
		if conf.MaxBackups == 0 {
			conf.MaxBackups = def.MaxBackups
		}
		if conf.MaxAge == 0 {
			conf.MaxAge = def.MaxAge
		}
	}
	defaultConf = conf
	if buildLogLevel != "" {
		conf.LogLevel = buildLogLevel
	}
	// 初始化日志等级
	lv := StrToLevel(conf.LogLevel)
	// 初始化zap配置
	config := zapcore.EncoderConfig{
		MessageKey:     "M",                                                            // 结构化（json）输出：msg的key
		LevelKey:       "L",                                                            // 结构化（json）输出：日志级别的key（INFO，WARN，ERROR等）
		TimeKey:        "T",                                                            // 结构化（json）输出：时间的key
		CallerKey:      "C",                                                            // 结构化（json）输出：打印日志的文件对应的Key
		NameKey:        "N",                                                            // 结构化（json）输出: 日志名
		StacktraceKey:  "S",                                                            // 结构化（json）输出: 堆栈
		LineEnding:     zapcore.DefaultLineEnding,                                      // 换行符
		EncodeLevel:    zapcore.LowercaseLevelEncoder,                                  // 将日志级别转换成大写（INFO，WARN，ERROR等）
		EncodeTime:     zapcore.TimeEncoderOfLayout("2006/01/02 15:04:05.000000Z0700"), // 日志时间的输出样式
		EncodeDuration: zapcore.SecondsDurationEncoder,                                 // 消耗时间的输出样式
		EncodeCaller:   zapcore.ShortCallerEncoder,                                     // 采用短文件路径编码输出（test/main.go:14 ）
	}

	loglevel = zap.NewAtomicLevelAt(lv)

	// 获取io.Writer的实现
	loggerWriter := GetLoggerWriter(logPath)
	// 实现多个输出
	var cores []zapcore.Core
	// 将info及以下写入logPath，NewConsoleEncoder 是非结构化输出
	cores = append(cores, zapcore.NewCore(zapcore.NewJSONEncoder(config), zapcore.AddSync(loggerWriter), loglevel))
	if defaultConf.PrintConsole {
		// 同时将日志输出到控制台，NewJSONEncoder 是结构化输出
		config.EncodeCaller = callerEncoder
		config.EncodeLevel = zapcore.LowercaseColorLevelEncoder
		c := zapcore.NewCore(zapcore.NewConsoleEncoder(config), zapcore.NewMultiWriteSyncer(zapcore.AddSync(os.Stdout)), loglevel)
		cores = append(cores, c)
	}
	mulCore := zapcore.NewTee(cores...)
	// 设置初始化字段
	var options = []zap.Option{
		zap.AddCaller(),
		zap.AddStacktrace(zap.DPanicLevel),
		zap.AddCallerSkip(1),
		zap.Hooks(func(entry zapcore.Entry) error {
			if entry.Level >= zap.DPanicLevel {
				PushPanicMsgToQYWX(entry)
			}
			return nil
		}),
		zap.Fields(zap.String("SR", serviceName)),
	}
	// info := strings.Split(serviceName, "_")
	// if len(info) > 0 {
	// 	options = append(options, zap.Fields(zap.String("ST", info[0])))
	// }
	logger = zap.New(mulCore, options...)
	sugarLogger = logger.Sugar()
}

func GetLogger() *zap.Logger {
	return logger
}

// GetLogFilePath 返回 InitLog 配置的日志文件路径。
func GetLogFilePath() string {
	return logFilePath
}

// ReplaceLogger 替换内部 logger 实例（供 logview.AttachRingBuffer 后同步）。
func ReplaceLogger(l *zap.Logger) {
	logger = l
	sugarLogger = l.Sugar()
}

func GetLoggerWriter(filename string) io.Writer {
	var writer = &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    defaultConf.MaxSize,    // 最大M数，超过则切割
		MaxBackups: defaultConf.MaxBackups, // 最大文件保留数，超过就删除最老的日志文件
		MaxAge:     defaultConf.MaxAge,     // 保存30天
		LocalTime:  defaultConf.LocalTime,  // 本地时间
		Compress:   defaultConf.Compress,   // 是否压缩
	}
	return writer
}

// Debug 调试日志接口
func Debug(msg string, fields ...zap.Field) {
	logger.Debug(msg, fields...)
}

// Info 关键信息日志接口
func Info(msg string, fields ...zap.Field) {
	logger.Info(msg, fields...)
}

// Warn 警告信息日志接口
func Warn(msg string, fields ...zap.Field) {
	logger.Warn(msg, fields...)
}

// Error 错误信息日志接口
func Error(msg string, fields ...zap.Field) {
	logger.Error(msg, fields...)
}

// DPanic Panic日志接口, 在生产环境中触发Panic后不退出,产生堆栈信息 (该接口可设置运行环境)
func DPanic(msg string, fields ...zap.Field) {
	logger.DPanic(msg, fields...)
}

// Fatal 触发Fatal日志，程序退出（用于配置加载）
func Fatal(msg string, fields ...zap.Field) {
	logger.Fatal(msg, fields...)
}

// DebugS KV形式日志接口
func DebugS(msg string, keysAndValues ...interface{}) {
	sugarLogger.Debugw(msg, keysAndValues...)
}

// InfoS 关键信息日志接口
func InfoS(msg string, keysAndValues ...interface{}) {
	sugarLogger.Infow(msg, keysAndValues...)
}

// WarnS 警告信息日志接口
func WarnS(msg string, keysAndValues ...interface{}) {
	sugarLogger.Warnw(msg, keysAndValues...)
}

// ErrorS 错误信息日志接口
func ErrorS(msg string, keysAndValues ...interface{}) {
	sugarLogger.Errorw(msg, keysAndValues...)
}

// DPanicS Panic日志接口, 在生产环境中触发Panic后不退出,产生堆栈信息 (该接口可设置运行环境)
func DPanicS(msg string, keysAndValues ...interface{}) {
	sugarLogger.DPanicw(msg, keysAndValues...)
}

// DebugF 非结构化日志接口
func DebugF(template string, args ...interface{}) {
	sugarLogger.Debugf(template, args...)
}

// InfoF 非结构化日志接口
func InfoF(template string, args ...interface{}) {
	sugarLogger.Infof(template, args...)
}

// WarnF 非结构化日志接口
func WarnF(template string, args ...interface{}) {
	sugarLogger.Warnf(template, args...)
}

// ErrorF 非结构化日志接口
func ErrorF(template string, args ...interface{}) {
	sugarLogger.Errorf(template, args...)
}

// DPanicF 非结构化日志接口
func DPanicF(template string, args ...interface{}) {
	sugarLogger.DPanicf(template, args...)
}

// FatalF 非结构化日志接口，触发Fatal日志后程序退出
func FatalF(template string, args ...interface{}) {
	sugarLogger.Fatalf(template, args...)
}

// SetLogLevel 设置日志等级接口
func SetLogLevel(logLevel zapcore.Level) {
	loglevel.SetLevel(logLevel)
}

// GetLogLevel 获取日志等级接口
func GetLogLevel() zapcore.Level {
	return loglevel.Level()
}

// StrToLevel 日志等级装换
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
