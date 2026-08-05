package preflight

import (
	"net"
	"os"
)

const (
	Pass    = "pass"
	Warning = "warning"
	Fail    = "fail"
)

type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}
type Result struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}
type DoctorConfig struct {
	Address, DataRoot, DashboardPath, GSIConfig string
	KnownConfigs                                []string
}
type Dependencies struct {
	Listen     func(network, address string) (net.Listener, error)
	ReadFile   func(string) ([]byte, error)
	MkdirAll   func(string, os.FileMode) error
	CreateTemp func(string, string) (*os.File, error)
	Remove     func(string) error
}
type Doctor struct{ deps Dependencies }

func NewDoctor(deps Dependencies) *Doctor {
	if deps.Listen == nil {
		deps.Listen = net.Listen
	}
	if deps.ReadFile == nil {
		deps.ReadFile = os.ReadFile
	}
	if deps.MkdirAll == nil {
		deps.MkdirAll = os.MkdirAll
	}
	if deps.CreateTemp == nil {
		deps.CreateTemp = os.CreateTemp
	}
	if deps.Remove == nil {
		deps.Remove = os.Remove
	}
	return &Doctor{deps: deps}
}

func (d *Doctor) Run(config DoctorConfig) Result {
	checks := make([]Check, 0, 5)
	normalized, addressErr := NormalizeListenAddress(config.Address)
	if addressErr != nil {
		checks = append(checks, Check{"listen_address", Fail, "listen address must be explicit loopback with a nonzero port"})
	} else {
		checks = append(checks, Check{"listen_address", Pass, "listen address is valid loopback"})
	}
	if addressErr != nil {
		checks = append(checks, Check{"listen_available", Fail, "listen availability skipped because address is invalid"})
	} else if listener, err := d.deps.Listen("tcp", normalized); err != nil {
		checks = append(checks, Check{"listen_available", Fail, "listen address is unavailable"})
	} else {
		_ = listener.Close()
		checks = append(checks, Check{"listen_available", Pass, "listen address is available"})
	}

	if err := d.deps.MkdirAll(config.DataRoot, 0o755); err != nil {
		checks = append(checks, Check{"data_root", Fail, "data root cannot be created"})
	} else if probe, err := d.deps.CreateTemp(config.DataRoot, ".doctor-*"); err != nil {
		checks = append(checks, Check{"data_root", Fail, "data root is not writable"})
	} else {
		name := probe.Name()
		closeErr := probe.Close()
		removeErr := d.deps.Remove(name)
		if closeErr != nil || removeErr != nil {
			checks = append(checks, Check{"data_root", Fail, "data root probe cleanup failed"})
		} else {
			checks = append(checks, Check{"data_root", Pass, "data root is writable"})
		}
	}

	if data, err := d.deps.ReadFile(config.DashboardPath); err != nil || len(data) == 0 {
		checks = append(checks, Check{"dashboard_asset", Fail, "dashboard index is unavailable"})
	} else {
		checks = append(checks, Check{"dashboard_asset", Pass, "dashboard index is readable"})
	}

	checks = append(checks, d.checkConfig(config, normalized, addressErr))
	result := Result{OK: true, Checks: checks}
	for _, check := range checks {
		if check.Status == Fail {
			result.OK = false
		}
	}
	return result
}

func (d *Doctor) checkConfig(config DoctorConfig, normalized string, addressErr error) Check {
	if config.GSIConfig != "" {
		data, err := d.deps.ReadFile(config.GSIConfig)
		if err != nil {
			return Check{"gsi_config", Fail, "explicit GSI config is unavailable"}
		}
		if addressErr != nil || ValidateGSIConfig(data, normalized) != nil {
			return Check{"gsi_config", Fail, "GSI config URI does not match the listener /gsi endpoint"}
		}
		return Check{"gsi_config", Pass, "GSI config targets the selected local endpoint"}
	}
	for _, path := range config.KnownConfigs {
		data, err := d.deps.ReadFile(path)
		if err != nil {
			continue
		}
		if addressErr != nil || ValidateGSIConfig(data, normalized) != nil {
			return Check{"gsi_config", Fail, "discovered GSI config URI does not match the listener"}
		}
		return Check{"gsi_config", Pass, "discovered GSI config targets the selected local endpoint"}
	}
	return Check{"gsi_config", Warning, "no GSI config discovered; install it before a manual Dota session"}
}
