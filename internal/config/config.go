package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig  `yaml:"server"`
	Sandbox  SandboxConfig `yaml:"sandbox"`
	Auth     AuthConfig    `yaml:"auth"`
	Children []ChildConfig `yaml:"children"`
}

type ServerConfig struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type SandboxConfig struct {
	Workdir string `yaml:"workdir"`
}

type AuthConfig struct {
	Issuer      string `yaml:"issuer"`
	ResourceURL string `yaml:"resource_url"`
	Token       string `yaml:"token"`
}

type ChildConfig struct {
	Name    string            `yaml:"name"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Server.Name == "" {
		cfg.Server.Name = "claude-remote"
	}
	if cfg.Server.Version == "" {
		cfg.Server.Version = "0.1.0"
	}
	if cfg.Sandbox.Workdir == "" {
		cfg.Sandbox.Workdir = "/workspace"
	}
	if t := os.Getenv("AUTH_TOKEN"); t != "" {
		cfg.Auth.Token = t
	}
	return &cfg, nil
}
