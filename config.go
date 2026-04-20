package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

var envRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func splitTrimCSV(s string) []string {
	parts := strings.Split(s, ",")

	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}

	return parts
}

func expandEnv(s string) string {
	return envRefRe.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-1]
		v, ok := os.LookupEnv(name)

		if !ok {
			ThrowFmt("config references unset env var ${%s}", name)
		}

		return v
	})
}

type Endpoint struct {
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"`
	User    string `json:"user"`
	Path    string `json:"path"`
	SSHKey  string `json:"ssh_key,omitempty"`
	LogPath string `json:"log_path,omitempty"`
}

type EtcdConfig struct {
	Endpoints []string `json:"endpoints"`
}

type ControlConfig struct {
	Listen string `json:"listen"`
}

type WebConfig struct {
	API    string `json:"api"`
	Listen string `json:"listen"`
}

type S3Config struct {
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	UsePathStyle bool   `json:"use_path_style"`
}

type HostConfig struct {
	CpusPerSlot int `json:"cpus_per_slot"`
}

type Config struct {
	Endpoints      []Endpoint            `json:"endpoints"`
	Hosts          map[string]HostConfig `json:"hosts,omitempty"`
	CpuOvercommit  float64               `json:"cpu_overcommit,omitempty"`
	Etcd           EtcdConfig            `json:"etcd"`
	S3             S3Config              `json:"s3"`
	Control        ControlConfig         `json:"control,omitempty"`
	Web            WebConfig             `json:"web,omitempty"`
	SSHKeyPath     string                `json:"ssh_key_path"`
	RemoteWrapPath string                `json:"remote_wrap_path,omitempty"`
}

func LoadConfig(path string) *Config {
	data := Throw2(os.ReadFile(path))
	expanded := expandEnv(string(data))

	var cfg Config
	Throw(json.Unmarshal([]byte(expanded), &cfg))

	if cfg.CpuOvercommit <= 0 {
		cfg.CpuOvercommit = 1.25
	}

	if v := os.Getenv("ETCDCTL_ENDPOINTS"); v != "" {
		cfg.Etcd.Endpoints = splitTrimCSV(v)
	}

	if v := os.Getenv("AWS_ACCESS_KEY_ID"); v != "" {
		cfg.S3.AccessKey = v
	}

	if v := os.Getenv("AWS_SECRET_ACCESS_KEY"); v != "" {
		cfg.S3.SecretKey = v
	}

	return &cfg
}
