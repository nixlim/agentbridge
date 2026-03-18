package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig              `yaml:"server" json:"server"`
	Workspace WorkspaceConfig           `yaml:"workspace" json:"workspace"`
	Workflow  WorkflowConfig            `yaml:"workflow" json:"workflow"`
	Brain     BrainConfig               `yaml:"brain" json:"brain"`
	Team      []TeamMemberConfig        `yaml:"team" json:"team"`
	Providers map[string]ProviderConfig `yaml:"providers" json:"providers"`
	Agents    map[string]AgentConfig    `yaml:"agents" json:"agents"` // legacy v1 support
	Log       LogConfig                 `yaml:"log" json:"log"`
}

type ServerConfig struct {
	Host string `yaml:"host" json:"host"`
	Port int    `yaml:"port" json:"port"`
}

type WorkspaceConfig struct {
	Path    string `yaml:"path" json:"path"`
	InitGit bool   `yaml:"init_git" json:"init_git"`
}

type WorkflowConfig struct {
	DefaultReviewRounds int    `yaml:"default_review_rounds" json:"default_review_rounds"`
	DefaultRecipe       string `yaml:"default_recipe" json:"default_recipe"`
	MaxWallClockMinutes int    `yaml:"max_wall_clock_minutes" json:"max_wall_clock_minutes"`
	MaxCostTokens       int    `yaml:"max_cost_tokens" json:"max_cost_tokens"`
	EnableDiscovery     bool   `yaml:"enable_discovery" json:"enable_discovery"`
	EnableHumanGates    bool   `yaml:"enable_human_gates" json:"enable_human_gates"`
}

// BrainConfig is legacy naming kept for config file compatibility (brain: section in YAML).
// In the current architecture this configures the LLM provider used for non-deterministic
// planning styles (upfront/rolling). For the default deterministic workflow, only Provider
// and PlanningStyle are meaningful.
type BrainConfig struct {
	Provider           string   `yaml:"provider" json:"provider"`
	Command            string   `yaml:"command" json:"command"`
	Args               []string `yaml:"args" json:"args"`
	TimeoutSeconds     int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	ModelHint          string   `yaml:"model_hint" json:"model_hint"`
	SystemPromptFile   string   `yaml:"system_prompt_file" json:"system_prompt_file"`
	MaxContextMessages int      `yaml:"max_context_messages" json:"max_context_messages"`
	PlanningStyle      string   `yaml:"planning_style" json:"planning_style"`
}

type TeamMemberConfig struct {
	Name        string `yaml:"name" json:"name"`
	Provider    string `yaml:"provider" json:"provider"`
	Role        string `yaml:"role" json:"role"`
	Count       int    `yaml:"count" json:"count"`
	Description string `yaml:"description" json:"description"`
}

type ProviderConfig struct {
	Command        string            `yaml:"command" json:"command"`
	Args           []string          `yaml:"args" json:"args"`
	TimeoutSeconds int               `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxRetries     int               `yaml:"max_retries" json:"max_retries"`
	WorkingDir     string            `yaml:"working_dir" json:"working_dir"`
	Env            map[string]string `yaml:"env" json:"env"`
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
		Workflow: WorkflowConfig{
			DefaultReviewRounds: 6,
			DefaultRecipe:       workflowRecipeSpecReviewLoop,
		},
		Brain: BrainConfig{
			Provider:           "claude",
			Command:            "claude",
			Args:               []string{"--output-format", "json", "--verbose"},
			TimeoutSeconds:     900,
			SystemPromptFile:   "./brain_system_prompt.md",
			MaxContextMessages: 50,
			PlanningStyle:      "deterministic",
		},
		Providers: map[string]ProviderConfig{
			"claude": {
				Command:        "claude",
				Args:           []string{"--dangerously-skip-permissions", "--output-format", "json", "--verbose"},
				TimeoutSeconds: 900,
				MaxRetries:     2,
				WorkingDir:     "",
				Env: map[string]string{
					"CLAUDE_CODE_MAX_TURNS": "50",
				},
			},
			"codex": {
				Command:        "codex",
				Args:           []string{"exec", "--full-auto"},
				TimeoutSeconds: 900,
				MaxRetries:     2,
				WorkingDir:     "",
				Env:            map[string]string{},
			},
		},
		Team: []TeamMemberConfig{
			{
				Name:        "claude",
				Provider:    "claude",
				Role:        "implementer",
				Count:       1,
				Description: "General-purpose implementation agent.",
			},
			{
				Name:        "codex",
				Provider:    "codex",
				Role:        "implementer",
				Count:       1,
				Description: "General-purpose implementation agent.",
			},
		},
		Agents: map[string]AgentConfig{
			"claude": providerToAgentConfig(ProviderConfig{
				Command:        "claude",
				Args:           []string{"--dangerously-skip-permissions", "--output-format", "json", "--verbose"},
				TimeoutSeconds: 900,
				MaxRetries:     2,
				Env: map[string]string{
					"CLAUDE_CODE_MAX_TURNS": "50",
				},
			}),
			"codex": providerToAgentConfig(ProviderConfig{
				Command:        "codex",
				Args:           []string{"exec", "--full-auto"},
				TimeoutSeconds: 900,
				MaxRetries:     2,
				Env:            map[string]string{},
			}),
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
	if cfg.Workflow.DefaultReviewRounds <= 0 {
		cfg.Workflow.DefaultReviewRounds = 6
	}

	cfg.Workspace.Path = cleanPath(cfg.Workspace.Path)
	cfg.Log.File = cleanPath(cfg.Log.File)
	cfg.Brain.SystemPromptFile = cleanPath(cfg.Brain.SystemPromptFile)

	if len(cfg.Providers) == 0 && len(cfg.Agents) > 0 {
		cfg.Providers = make(map[string]ProviderConfig, len(cfg.Agents))
		for name, agentCfg := range cfg.Agents {
			cfg.Providers[name] = providerFromAgentConfig(agentCfg)
		}
	}
	normalizeProviders(cfg.Providers)

	if len(cfg.Team) == 0 {
		cfg.Team = teamFromLegacyAgents(cfg.Agents)
	}
	cfg.Team = expandTeam(cfg.Team)
	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentConfig{}
	}
	for _, member := range cfg.Team {
		providerCfg, ok := cfg.Providers[member.Provider]
		if ok {
			cfg.Agents[member.Name] = providerToAgentConfig(providerCfg)
		}
	}

	if cfg.Brain.Provider != "" {
		if providerCfg, ok := cfg.Providers[cfg.Brain.Provider]; ok {
			if cfg.Brain.Command == "" {
				cfg.Brain.Command = providerCfg.Command
			}
			if len(cfg.Brain.Args) == 0 {
				cfg.Brain.Args = append([]string(nil), providerCfg.Args...)
			}
			if cfg.Brain.TimeoutSeconds <= 0 {
				cfg.Brain.TimeoutSeconds = providerCfg.TimeoutSeconds
			}
		}
	}
	if cfg.Brain.Provider == "claude" {
		cfg.Brain.Args = removeArg(cfg.Brain.Args, "-p")
	}
	if cfg.Brain.TimeoutSeconds <= 0 {
		cfg.Brain.TimeoutSeconds = int((15 * time.Minute).Seconds())
	}
	if cfg.Brain.MaxContextMessages <= 0 {
		cfg.Brain.MaxContextMessages = 50
	}
	if cfg.Brain.PlanningStyle == "" {
		cfg.Brain.PlanningStyle = "deterministic"
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
	if len(cfg.Providers) == 0 {
		return errors.New("at least one provider must be configured")
	}
	if len(cfg.Team) == 0 {
		return errors.New("at least one team member must be configured")
	}
	for name, providerCfg := range cfg.Providers {
		if name == "" {
			return errors.New("provider name cannot be empty")
		}
		if providerCfg.Command == "" {
			return fmt.Errorf("provider %s command is required", name)
		}
		if providerCfg.TimeoutSeconds <= 0 {
			return fmt.Errorf("provider %s timeout_seconds must be > 0", name)
		}
	}
	if cfg.Brain.Provider == "" {
		return errors.New("brain.provider is required")
	}
	if _, ok := cfg.Providers[cfg.Brain.Provider]; !ok {
		return fmt.Errorf("brain.provider %q is not defined in providers", cfg.Brain.Provider)
	}
	if cfg.Brain.Command == "" {
		return errors.New("brain.command is required")
	}
	switch cfg.Brain.PlanningStyle {
	case "upfront", "rolling", "deterministic", "manual", "boundary":
	default:
		return fmt.Errorf("brain.planning_style must be one of \"upfront\", \"rolling\", \"deterministic\", \"manual\", or \"boundary\"")
	}
	seenNames := map[string]bool{}
	for _, member := range cfg.Team {
		if member.Name == "" {
			return errors.New("team member name is required")
		}
		if seenNames[member.Name] {
			return fmt.Errorf("duplicate team member name %q", member.Name)
		}
		seenNames[member.Name] = true
		if member.Provider == "" {
			return fmt.Errorf("team member %s provider is required", member.Name)
		}
		if _, ok := cfg.Providers[member.Provider]; !ok {
			return fmt.Errorf("team member %s references unknown provider %q", member.Name, member.Provider)
		}
		if member.Role == "" {
			return fmt.Errorf("team member %s role is required", member.Name)
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

func removeArg(args []string, target string) []string {
	if len(args) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == target {
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func normalizeProviders(providers map[string]ProviderConfig) {
	for name, providerCfg := range providers {
		if providerCfg.TimeoutSeconds <= 0 {
			providerCfg.TimeoutSeconds = int((15 * time.Minute).Seconds())
		}
		if providerCfg.MaxRetries < 0 {
			providerCfg.MaxRetries = 0
		}
		if providerCfg.Env == nil {
			providerCfg.Env = map[string]string{}
		}
		if name == "claude" {
			providerCfg.Args = ensureArg(providerCfg.Args, "--dangerously-skip-permissions")
		}
		providers[name] = providerCfg
	}
}

func providerToAgentConfig(providerCfg ProviderConfig) AgentConfig {
	return AgentConfig{
		Command:        providerCfg.Command,
		Args:           append([]string(nil), providerCfg.Args...),
		TimeoutSeconds: providerCfg.TimeoutSeconds,
		MaxRetries:     providerCfg.MaxRetries,
		WorkingDir:     providerCfg.WorkingDir,
		Env:            cloneStringMap(providerCfg.Env),
	}
}

func providerFromAgentConfig(agentCfg AgentConfig) ProviderConfig {
	return ProviderConfig{
		Command:        agentCfg.Command,
		Args:           append([]string(nil), agentCfg.Args...),
		TimeoutSeconds: agentCfg.TimeoutSeconds,
		MaxRetries:     agentCfg.MaxRetries,
		WorkingDir:     agentCfg.WorkingDir,
		Env:            cloneStringMap(agentCfg.Env),
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func teamFromLegacyAgents(agents map[string]AgentConfig) []TeamMemberConfig {
	if len(agents) == 0 {
		return nil
	}
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	team := make([]TeamMemberConfig, 0, len(names))
	for _, name := range names {
		team = append(team, TeamMemberConfig{
			Name:        name,
			Provider:    name,
			Role:        "implementer",
			Count:       1,
			Description: fmt.Sprintf("%s provider-backed agent", name),
		})
	}
	return team
}

func expandTeam(team []TeamMemberConfig) []TeamMemberConfig {
	expanded := make([]TeamMemberConfig, 0, len(team))
	for _, member := range team {
		count := member.Count
		if count <= 0 {
			count = 1
		}
		if count == 1 {
			member.Count = 1
			expanded = append(expanded, member)
			continue
		}
		for i := 1; i <= count; i++ {
			clone := member
			clone.Name = fmt.Sprintf("%s-%d", member.Name, i)
			clone.Count = 1
			expanded = append(expanded, clone)
		}
	}
	return expanded
}

func (cfg Config) configuredBrainCommand(provider string) string {
	if providerCfg, ok := cfg.Providers[provider]; ok {
		return providerCfg.Command
	}
	return cfg.Brain.Command
}

func (cfg Config) configuredBrainArgs(provider string) []string {
	if providerCfg, ok := cfg.Providers[provider]; ok {
		return append([]string(nil), providerCfg.Args...)
	}
	return append([]string(nil), cfg.Brain.Args...)
}

func (cfg Config) configuredBrainTimeout(provider string) int {
	if providerCfg, ok := cfg.Providers[provider]; ok {
		return providerCfg.TimeoutSeconds
	}
	return cfg.Brain.TimeoutSeconds
}
