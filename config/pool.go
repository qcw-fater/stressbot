package config

// ConnPoolConfig 连接池通用配置。
// MySQL 和 Redis 的连接池参数共享同一形态，各字段均为可选（留空用驱动默认）。
type ConnPoolConfig struct {
	DialTimeout     string `toml:"dialTimeout"     json:"dialTimeout"`     // 拨号超时
	ReadTimeout     string `toml:"readTimeout"     json:"readTimeout"`     // 读超时
	WriteTimeout    string `toml:"writeTimeout"    json:"writeTimeout"`    // 写超时
	MaxOpenConns    int    `toml:"maxOpenConns"    json:"maxOpenConns"`    // 最大打开连接数
	MaxIdleConns    int    `toml:"maxIdleConns"    json:"maxIdleConns"`    // 最大空闲连接数
	ConnMaxLifetime string `toml:"connMaxLifetime" json:"connMaxLifetime"` // 连接最大存活时间
}
