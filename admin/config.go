package admin

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"stressbot/config"
	"stressbot/internal/stresslog"
	"stressbot/state/shared"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// Config Admin 服务端配置（对应 admin.toml）。
type Config struct {
	Server       ServerConfig        `toml:"server"       json:"server"`       // 管理面（HTTP，面向浏览器）
	ControlPlane ControlPlaneConfig  `toml:"controlPlane" json:"controlPlane"` // 控制面（gRPC，面向 Agent）
	MySQL        *MySQLConfig        `toml:"mysql"        json:"mysql"`        // MySQL 历史归档（nil=不启用）
	Redis        *shared.RedisConfig `toml:"redis"        json:"redis"`        // Redis 共享状态（nil=不启用）
	Log          stresslog.Config    `toml:"log"          json:"log"`          // 日志
	Pprof        *config.PprofConfig `toml:"pprof"        json:"pprof"`        // pprof 调试（nil=不启用）
	Daemon       bool                `toml:"daemon"       json:"daemon"`       // 守护进程模式（仅 Linux）
}

// ServerConfig 管理面（HTTP，面向浏览器）配置。
type ServerConfig struct {
	Port       int    `toml:"port"       json:"port"`       // HTTP 监听端口（默认 7718）
	ListenHost string `toml:"listenHost" json:"listenHost"` // 监听地址（默认 127.0.0.1）
	StaticDir  string `toml:"staticDir"  json:"staticDir"`  // 前端静态文件目录（默认 cmd/web/dist）
}

// ControlPlaneConfig 控制面（gRPC，面向 Agent 节点）配置。
//
// 联动约束（与 Agent 心跳和租约）：
//   - HeartbeatInterval 是期望的 Agent 心报间隔，下发给 Agent
//   - LeaseDuration 是任务租约时长：Agent 与 Admin 失联超过该时长才取消任务。
//     必须明显大于 Agent 重连最坏耗时（reconnect.maxInterval 的 2 倍抖动上界
//     加拨号握手），且小于 OfflineAfter——让 Agent 侧租约先于 Admin 离线
//     清理接管任务生死，避免两套判定窗口重叠
//   - UnhealthyAfter 是心跳超时标记 unhealthy 的阈值，必须明显大于 HeartbeatInterval
//   - OfflineAfter 必须 > UnhealthyAfter
//
// 否则会出现「Admin 已把节点标 unhealthy/删除，但 Agent 任务还在跑」的状态错乱。
type ControlPlaneConfig struct {
	ListenHost        string `toml:"listenHost"        json:"listenHost"`        // gRPC 监听地址（默认 127.0.0.1）
	Port              int    `toml:"port"              json:"port"`              // gRPC 监听端口（默认 7720）
	HeartbeatInterval string `toml:"heartbeatInterval" json:"heartbeatInterval"` // 期望 Agent 心跳间隔（默认 10s）
	UnhealthyAfter    string `toml:"unhealthyAfter"    json:"unhealthyAfter"`    // 心跳超时后标记 unhealthy（默认 30s）
	LeaseDuration     string `toml:"leaseDuration"     json:"leaseDuration"`     // 任务租约时长：Agent 断连超过此时长取消任务（默认 45s）
	OfflineAfter      string `toml:"offlineAfter"      json:"offlineAfter"`      // 超过此时间标记 offline 并删除（默认 60s）
}

// MySQLConfig MySQL 连接配置（对应 [mysql] 段；host 留空=不启用）。
// retentionDays 从原 [history] 段并入——历史归档强依赖 MySQL。
type MySQLConfig struct {
	Host          string                `toml:"host"          json:"host"`          // 主机地址
	Port          int                   `toml:"port"          json:"port"`          // 端口号（默认 3306）
	Username      string                `toml:"username"      json:"username"`      // 用户名
	Password      string                `toml:"password"      json:"password"`      // 密码
	Database      string                `toml:"database"      json:"database"`      // 数据库名
	RetentionDays int                   `toml:"retentionDays" json:"retentionDays"` // 历史数据保留天数（默认 90）
	Pool          config.ConnPoolConfig `toml:"pool"       json:"pool"`             // 连接池参数
}

// DSN 用驱动自带的 mysql.Config 生成连接字符串，而非手工拼接：DSN 不是 URL，
// 但 `@` `/` `:` `?` 在其中都是分隔符，密码或库名里带这些字符时手工拼接会让
// 驱动解析错位（把密码的后半段当成主机名等）。FormatDSN 负责按驱动规则转义。
// 各 timeout 留空表示沿用驱动默认（不写进 DSN）。
func (c MySQLConfig) DSN() (string, error) {
	port := c.Port
	if port == 0 {
		port = 3306
	}
	dialTimeout, err := parseOptionalDuration(c.Pool.DialTimeout, "mysql.pool.dialTimeout")
	if err != nil {
		return "", err
	}
	readTimeout, err := parseOptionalDuration(c.Pool.ReadTimeout, "mysql.pool.readTimeout")
	if err != nil {
		return "", err
	}
	writeTimeout, err := parseOptionalDuration(c.Pool.WriteTimeout, "mysql.pool.writeTimeout")
	if err != nil {
		return "", err
	}
	dsn := mysqldriver.NewConfig()
	dsn.User = c.Username
	dsn.Passwd = c.Password
	dsn.Net = "tcp"
	dsn.Addr = net.JoinHostPort(c.Host, strconv.Itoa(port))
	dsn.DBName = c.Database
	dsn.ParseTime = true
	dsn.Loc = time.Local
	dsn.Timeout = dialTimeout
	dsn.ReadTimeout = readTimeout
	dsn.WriteTimeout = writeTimeout
	return dsn.FormatDSN(), nil
}

// ConnMaxLifetime 解析连接最大存活时间；留空返回 0，表示不设上限。
func (c MySQLConfig) ConnMaxLifetime() (time.Duration, error) {
	return parseOptionalDuration(c.Pool.ConnMaxLifetime, "mysql.pool.connMaxLifetime")
}

// parseOptionalDuration 解析可选的连接池时长：留空返回 0 交给驱动自带默认，写错则报错。
// 这里用 ParseDuration 而非 ParsePositiveDuration——0 在 MySQL 驱动里是「不限制」的
// 合法显式值（如 connMaxLifetime = "0"），不能当非法值拒掉。
func parseOptionalDuration(raw, field string) (time.Duration, error) {
	return config.ParseDuration(raw, 0, field)
}

// RedisEnabled 返回服务器是否配置了 Redis（host 非空）。
func (c *Config) RedisEnabled() bool {
	return c.Redis != nil && c.Redis.Enabled()
}

// MySQLEnabled 返回服务器是否配置了 MySQL；host 为空时明确禁用。
func (c *Config) MySQLEnabled() bool {
	return c.MySQL != nil && strings.TrimSpace(c.MySQL.Host) != ""
}

// Defaults 返回填充了默认值的 Admin 配置。
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			Port:       7718,
			ListenHost: "127.0.0.1",
			StaticDir:  "cmd/web/dist",
		},
		ControlPlane: ControlPlaneConfig{
			ListenHost:        "127.0.0.1",
			Port:              7720,
			HeartbeatInterval: "10s",
			UnhealthyAfter:    "30s",
			LeaseDuration:     "45s",
			OfflineAfter:      "60s",
		},
		MySQL: &MySQLConfig{
			RetentionDays: 90,
			Pool: config.ConnPoolConfig{
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: "1h",
				DialTimeout:     "5s",
				ReadTimeout:     "30s",
				WriteTimeout:    "30s",
			},
		},
		Log: stresslog.Config{
			Path:       "log/admin.log",
			LogLevel:   "info",
			MaxSize:    100,
			MaxBackups: 10,
			MaxAge:     30,
		},
	}
}

// LoadConfig 从 TOML 文件加载配置。
func LoadConfig(path string) (*Config, error) {
	cfg, err := config.LoadTOML(path, Defaults())
	if err != nil {
		return nil, err
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateConfig 校验配置合法性和联动约束。
func validateConfig(cfg *Config) error {
	if cfg.Server.Port <= 0 {
		return errors.New("server.port 必须配置且大于 0")
	}
	if cfg.ControlPlane.Port <= 0 {
		return errors.New("controlPlane.port 必须配置且大于 0")
	}
	if cfg.Server.ListenHost == "" {
		return errors.New("server.listenHost 未配置")
	}
	if cfg.ControlPlane.ListenHost == "" {
		return errors.New("controlPlane.listenHost 未配置")
	}
	heartbeat, err := time.ParseDuration(cfg.ControlPlane.HeartbeatInterval)
	if err != nil {
		return fmt.Errorf("controlPlane.heartbeatInterval 不是合法时长: %w", err)
	}
	if heartbeat <= 0 {
		return errors.New("controlPlane.heartbeatInterval 必须大于 0")
	}
	unhealthy, err := time.ParseDuration(cfg.ControlPlane.UnhealthyAfter)
	if err != nil {
		return fmt.Errorf("controlPlane.unhealthyAfter 不是合法时长: %w", err)
	}
	if unhealthy <= heartbeat {
		return errors.New("controlPlane.unhealthyAfter 必须大于 controlPlane.heartbeatInterval")
	}
	lease, err := time.ParseDuration(cfg.ControlPlane.LeaseDuration)
	if err != nil {
		return fmt.Errorf("controlPlane.leaseDuration 不是合法时长: %w", err)
	}
	if lease <= heartbeat {
		return errors.New("controlPlane.leaseDuration 必须大于 controlPlane.heartbeatInterval")
	}
	offline, err := time.ParseDuration(cfg.ControlPlane.OfflineAfter)
	if err != nil {
		return fmt.Errorf("controlPlane.offlineAfter 不是合法时长: %w", err)
	}
	if offline <= unhealthy {
		return errors.New("controlPlane.offlineAfter 必须大于 controlPlane.unhealthyAfter")
	}
	if lease >= offline {
		return errors.New("controlPlane.leaseDuration 必须小于 controlPlane.offlineAfter")
	}
	if cfg.RedisEnabled() {
		if _, err := cfg.Redis.Resolve(); err != nil {
			return fmt.Errorf("redis 配置无效: %w", err)
		}
	}
	// 连接池的时长参数在加载期一并校验：写错的值必须现在报错，
	// 不能等到装配阶段再静默退化成驱动默认。
	if cfg.MySQLEnabled() {
		if _, err := cfg.MySQL.DSN(); err != nil {
			return err
		}
		if _, err := cfg.MySQL.ConnMaxLifetime(); err != nil {
			return err
		}
	}
	// MySQL 连通性校验推迟到装配阶段（NewServer 里 mysql.Open + ping）。
	return nil
}
