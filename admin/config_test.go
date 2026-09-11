package admin

import "testing"

// 租约时长与心跳、离线阈值的联动约束：租约必须落在
// (heartbeatInterval, offlineAfter) 开区间内，且默认值自洽。
func TestValidateConfigLeaseDurationConstraints(t *testing.T) {
	if err := validateConfig(configWith(func(c *ControlPlaneConfig) {})); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}

	cases := []struct {
		name    string
		lease   string
		wantErr string
	}{
		{"等于心跳间隔", "10s", "controlPlane.leaseDuration 必须大于 controlPlane.heartbeatInterval"},
		{"小于心跳间隔", "5s", "controlPlane.leaseDuration 必须大于 controlPlane.heartbeatInterval"},
		{"达到离线阈值", "60s", "controlPlane.leaseDuration 必须小于 controlPlane.offlineAfter"},
		{"超过离线阈值", "90s", "controlPlane.leaseDuration 必须小于 controlPlane.offlineAfter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(configWith(func(c *ControlPlaneConfig) { c.LeaseDuration = tc.lease }))
			if err == nil {
				t.Fatalf("leaseDuration=%s 应校验失败", tc.lease)
			}
			if err.Error() != tc.wantErr {
				t.Fatalf("错误信息 = %q，期望 %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func configWith(mutate func(*ControlPlaneConfig)) *Config {
	cfg := Defaults()
	mutate(&cfg.ControlPlane)
	return &cfg
}
