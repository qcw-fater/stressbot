package standalone

import (
	"fmt"
	"path/filepath"

	"stressbot/config"
	"stressbot/internal/stresslog"
	"stressbot/monitor"
	"stressbot/robot"
	"stressbot/state/shared"
)

// BotConfig 机器人批量参数。
type BotConfig struct {
	AccountPrefix string `toml:"accountPrefix"`
	StartNumber   int    `toml:"startNumber"`
	TotalBots     int    `toml:"totalBots"`
	Concurrency   int    `toml:"concurrency"`
	MainService   string `toml:"mainService"`
}

// StandaloneConfig 单机模式专用配置。
type StandaloneConfig struct {
	Bot        BotConfig           `toml:"bot"`
	StateExtra map[string]string   `toml:"stateExtra"`
	Duration   string              `toml:"duration"`
	RampUp     *robot.RampUpConfig `toml:"rampUp"`
}

// NetworkConfig 定义 gnet 网络引擎调优参数。
type NetworkConfig struct {
	ReadBufferCap      int `toml:"readBufferCap"`
	WriteBufferCap     int `toml:"writeBufferCap"`
	NumEventLoop       int `toml:"numEventLoop"`
	MaxConcurrentDials int `toml:"maxConcurrentDials"`
	MaxBodyLen         int `toml:"maxBodyLen"`
}

// Config 是 stressbot.toml 的根配置。
type Config struct {
	Log        *stresslog.Config        `toml:"log"`
	Monitor    *monitor.CollectorConfig `toml:"monitor"`
	Pprof      *config.PprofConfig      `toml:"pprof"`
	Standalone *StandaloneConfig        `toml:"standalone"`
	Redis      *shared.RedisConfig      `toml:"redis"`
	Network    NetworkConfig            `toml:"network"`
	Daemon     bool                     `toml:"daemon"`
}

// Defaults 返回填充默认值的单机配置。
func Defaults() Config {
	return Config{Standalone: &StandaloneConfig{Bot: BotConfig{
		AccountPrefix: "bot_",
		StartNumber:   1,
		TotalBots:     1,
	}}}
}

func LoadConfig(path string) (*Config, error) {
	cfg, err := config.LoadTOML(path, Defaults())
	if err != nil {
		return nil, err
	}
	if cfg.Standalone == nil {
		cfg.Standalone = &StandaloneConfig{}
	}
	s := cfg.Standalone
	if s.Bot.StartNumber == 0 {
		s.Bot.StartNumber = 1
	}
	if s.Bot.TotalBots == 0 {
		s.Bot.TotalBots = 1
	}
	if s.Bot.AccountPrefix == "" {
		s.Bot.AccountPrefix = "bot_"
	}
	if s.Bot.MainService == "" {
		return nil, fmt.Errorf("standalone.bot.mainService is required")
	}
	if s.RampUp != nil && len(s.RampUp.Stages) > 0 {
		sum := 0
		for _, stage := range s.RampUp.Stages {
			sum += stage.Count
		}
		if sum != s.Bot.TotalBots {
			return nil, fmt.Errorf("standalone.rampUp.stages 各阶段 count 之和 (%d) 不等于 standalone.bot.totalBots (%d)", sum, s.Bot.TotalBots)
		}
	}
	return cfg, nil
}

// Paths 是单机运行所需的资源位置。
type Paths struct {
	Flow    string
	Proto   string
	Scripts string
	Adapter string
}

// ResolvePaths 以配置文件目录为基准解析资源路径，非空覆盖值转为绝对路径。
func ResolvePaths(configPath, flow, proto, scripts, adapter string) (Paths, error) {
	configAbs, err := filepath.Abs(configPath)
	if err != nil {
		return Paths{}, fmt.Errorf("解析配置路径失败: %w", err)
	}
	confDir := filepath.Dir(configAbs)
	return Paths{
		Flow:    resolvePath(flow, filepath.Join(confDir, "flow", "flow.json")),
		Proto:   resolvePath(proto, filepath.Join(confDir, "proto")),
		Scripts: resolvePath(scripts, filepath.Join(confDir, "scripts")),
		Adapter: resolvePath(adapter, filepath.Join(confDir, "adapter")),
	}, nil
}

func resolvePath(override, defaultPath string) string {
	if override == "" {
		return defaultPath
	}
	abs, err := filepath.Abs(override)
	if err != nil {
		return override
	}
	return abs
}
