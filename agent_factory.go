package main

import (
	"context"
	"fmt"
)

const claudeBrainDecisionSchema = `{"type":"object","properties":{"thinking":{"type":"string"},"decisions":{"type":"array","items":{"type":"object","properties":{"action":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"},"assign_to":{"type":"string"},"depends_on":{"type":"array","items":{"type":"string"}},"review_by":{"type":"string"},"priority":{"type":"integer"},"files_touched":{"type":"array","items":{"type":"string"}},"to":{"type":"string"},"content":{"type":"string"},"task_id":{"type":"string"},"feedback":{"type":"string"},"new_agent":{"type":"string"},"reason":{"type":"string"},"summary":{"type":"string"},"question":{"type":"string"},"context":{"type":"string"},"provider":{"type":"string"},"plan":{"type":"object"}},"required":["action"],"additionalProperties":true}},"required":["thinking","decisions"],"additionalProperties":false}`

type namedAgent struct {
	name  string
	inner Agent
}

func (a *namedAgent) Name() string {
	return a.name
}

func (a *namedAgent) Execute(ctx context.Context, prompt string, workDir string) (*AgentResult, error) {
	return a.inner.Execute(ctx, prompt, workDir)
}

func (a *namedAgent) ParseOutput(raw []byte) (*AgentResult, error) {
	return a.inner.ParseOutput(raw)
}

func (a *namedAgent) IsAvailable() bool {
	return a.inner.IsAvailable()
}

func instantiateTeamAgents(cfg Config, workspacePath string) (map[string]Agent, error) {
	agents := make(map[string]Agent, len(cfg.Team))
	for _, member := range cfg.Team {
		providerCfg, ok := cfg.Providers[member.Provider]
		if !ok {
			return nil, fmt.Errorf("unknown provider %q for team member %s", member.Provider, member.Name)
		}
		inner, err := newProviderAdapter(member.Provider, providerCfg, workspacePath)
		if err != nil {
			return nil, err
		}
		agents[member.Name] = &namedAgent{name: member.Name, inner: inner}
	}
	return agents, nil
}

func instantiateBrainAdapter(cfg Config, workspacePath string) (Agent, error) {
	providerCfg, ok := cfg.Providers[cfg.Brain.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown brain provider %q", cfg.Brain.Provider)
	}
	if cfg.Brain.Command != "" {
		providerCfg.Command = cfg.Brain.Command
	}
	if len(cfg.Brain.Args) > 0 {
		providerCfg.Args = append([]string(nil), cfg.Brain.Args...)
	}
	if cfg.Brain.TimeoutSeconds > 0 {
		providerCfg.TimeoutSeconds = cfg.Brain.TimeoutSeconds
	}
	providerCfg = constrainBrainProvider(cfg.Brain.Provider, providerCfg)
	return newProviderAdapter(cfg.Brain.Provider, providerCfg, workspacePath)
}

func newProviderAdapter(providerName string, cfg ProviderConfig, workspacePath string) (Agent, error) {
	agentCfg := providerToAgentConfig(cfg)
	switch providerName {
	case "claude":
		return NewClaudeAdapter(agentCfg, workspacePath), nil
	case "codex":
		return NewCodexAdapter(agentCfg, workspacePath), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", providerName)
	}
}

func constrainBrainProvider(providerName string, cfg ProviderConfig) ProviderConfig {
	switch providerName {
	case "claude":
		cfg.Args = removeArg(cfg.Args, "--verbose")
		cfg.Args = ensureArg(cfg.Args, "--disable-slash-commands")
		cfg.Args = ensureArg(cfg.Args, "--strict-mcp-config")
		cfg.Args = ensureOptionPair(cfg.Args, "--tools", "")
		cfg.Args = ensureOptionPair(cfg.Args, "--json-schema", claudeBrainDecisionSchema)
		cfg.Env = cloneStringMap(cfg.Env)
		cfg.Env["CLAUDE_CODE_MAX_TURNS"] = "1"
	}
	return cfg
}

func ensureOptionPair(args []string, flag string, value string) []string {
	for i := 0; i < len(args); i++ {
		if args[i] != flag {
			continue
		}
		if i+1 < len(args) && args[i+1] == value {
			return args
		}
		break
	}
	return append(append([]string{}, args...), flag, value)
}
