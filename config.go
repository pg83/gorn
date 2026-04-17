package main

import (
	"encoding/json"
	"os"
)

type Endpoint struct {
	Host string `json:"host"`
	User string `json:"user"`
	Path string `json:"path"`
}

type EtcdConfig struct {
	Endpoints []string `json:"endpoints"`
}

type S3Config struct {
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	UsePathStyle bool   `json:"use_path_style"`
}

type Config struct {
	Endpoints  []Endpoint `json:"endpoints"`
	Etcd       EtcdConfig `json:"etcd"`
	S3         S3Config   `json:"s3"`
	SSHKeyPath string     `json:"ssh_key_path"`
}

func LoadConfig(path string) *Config {
	data := Throw2(os.ReadFile(path))

	var cfg Config
	Throw(json.Unmarshal(data, &cfg))

	return &cfg
}
