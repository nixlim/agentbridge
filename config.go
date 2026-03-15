package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig           `yaml:"server" json:"server"`
	Workspace WorkspaceConfig        `yaml:"workspace" json:"workspace"`
	Agents    map[string]AgentConfig `yaml:"agents" json:"agents"`
	Log       LogConfig              `yaml:"log" json:"log"`
}

type ServerConfig struct {
	Host string `yaml:"host" json:"host"`
	Port int    `yaml:"port" json:"port"`
}

type WorkspaceConfig struct {
	Path    string `yaml:"path" json:"path"`
	InitGit bool   `yaml:"init_git" json:"init_git"`
}

type AgentConfig struct {
	Command        string            `yaml:"command" json:"command"`
	Args           []string          `yaml:"args" json:"args"`
	TimeoutSeconds int               `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxRetries     int               `yaml:"max_retries" json:"max_retries"`
	WorkingDir     string            `yaml:"working_dir" json:"working_dir"`
	Env            map[string]string `yaml:"env" json:"env"`
}

type LogConfig struct {
	File  string `yaml:"file" json:"file"`
	Level string `yaml:"level" json:"level"`
}

type CLIOptions struct {
	ConfigPath string
	Port       int
	Workspace  string
	LogLevel   string
}

func DefaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Host: "127.0.0.1",
			Port: 8080,
		},
		Workspace: WorkspaceConfig{
			Path:    "./workspace",
			InitGit: true,
		},
		Agents: map[string]AgentConfig{
			"claude": {
				Command:        "claude",
				Args:           []string{"--dangerously-skip-permissions", "--output-format", "json", "--verbose"},
				TimeoutSeconds: 300,
				MaxRetries:     2,
				Env: map[string]string{
					"CLAUDE_CODE_MAX_TURNS": "50",
				},
			},
			"codex": {
				Command:        "codex",
				Args:           []string{"exec", "--full-auto"},
				TimeoutSeconds: 300,
				MaxRetries:     2,
				Env:            map[string]string{},
			},
		},
		Log: LogConfig{
			File:  "./agentbridge.log",
			Level: "info",
		},
	}
}

func ParseFlags(args []string) CLIOptions {
	fs := flag.NewFlagSet("agentbridge", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)

	opts := CLIOptions{}
	fs.StringVar(&opts.ConfigPath, "config", "agentbridge.yaml", "Path to config file")
	fs.IntVar(&opts.Port, "port", 0, "Override server port")
	fs.StringVar(&opts.Workspace, "workspace", "", "Override workspace path")
	fs.StringVar(&opts.LogLevel, "log-level", "", "Override log level")
	_ = fs.Parse(args)

	return opts
}

func LoadConfig(opts CLIOptions) (Config, error) {
	cfg := DefaultConfig()

	if opts.ConfigPath != "" {
		if data, err := os.ReadFile(opts.ConfigPath); err == nil {
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return Config{}, fmt.Errorf("parse config: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
	}

	if opts.Port > 0 {
		cfg.Server.Port = opts.Port
	}
	if opts.Workspace != "" {
		cfg.Workspace.Path = opts.Workspace
	}
	if opts.LogLevel != "" {
		cfg.Log.Level = opts.LogLevel
	}

	cfg.Workspace.Path = cleanPath(cfg.Workspace.Path)
	cfg.Log.File = cleanPath(cfg.Log.File)
	for name, agentCfg := range cfg.Agents {
		if agentCfg.TimeoutSeconds <= 0 {
			agentCfg.TimeoutSeconds = int((5 * time.Minute).Seconds())
		}
		if agentCfg.MaxRetries < 0 {
			agentCfg.MaxRetries = 0
		}
		if agentCfg.Env == nil {
			agentCfg.Env = map[string]string{}
		}
		if name == "claude" {
			agentCfg.Args = ensureArg(agentCfg.Args, "--dangerously-skip-permissions")
		}
		cfg.Agents[name] = agentCfg
	}

	return cfg, ValidateConfig(cfg)
}

func ValidateConfig(cfg Config) error {
	if cfg.Server.Host == "" {
		return errors.New("server.host is required")
	}
	if cfg.Server.Port <= 0 {
		return errors.New("server.port must be > 0")
	}
	if cfg.Workspace.Path == "" {
		return errors.New("workspace.path is required")
	}
	if len(cfg.Agents) == 0 {
		return errors.New("at least one agent must be configured")
	}
	for name, agentCfg := range cfg.Agents {
		if name == "" {
			return errors.New("agent name cannot be empty")
		}
		if agentCfg.Command == "" {
			return fmt.Errorf("agent %s command is required", name)
		}
		if agentCfg.TimeoutSeconds <= 0 {
			return fmt.Errorf("agent %s timeout_seconds must be > 0", name)
		}
	}
	return nil
}

func cleanPath(path string) string {
	if path == "" {
		return path
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	wd, err := os.Getwd()
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Join(wd, path)
}

func ensureArg(args []string, target string) []string {
	for _, arg := range args {
		if arg == target {
			return args
		}
	}
	return append([]string{target}, args...)
}
