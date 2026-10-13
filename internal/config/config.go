package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	LLM        LLMConfig
	Telegram   TelegramConfig
	Storage    StorageConfig
	Agent      AgentConfig
	Security   SecurityConfig
	HealthAddr string
}

type LLMConfig struct {
	BaseURL      string
	APIKey       string
	Model        string
	Timeout      time.Duration
	MaxRetries   int
	MaxInputCh   int
	MaxOutputTok int
}

type TelegramConfig struct {
	BotToken       string
	AllowedUsers   []int64
	MaxMsgLen      int
	RatePerMin     int
	MaxActiveTasks int
}

type StorageConfig struct {
	DataDir       string
	DBPath        string
	RetentionDays int
}

type AgentConfig struct {
	MaxIterations       int
	MaxToolCalls        int
	ToolTimeout         time.Duration
	TaskTimeout         time.Duration
	MaxTokensPerTask    int
	SkillsDir           string
	WorkspaceRoot       string
	ShellAllowlist      []string
	MaxFileBytes        int64
	MaxOutputBytes      int64
	RequireApprovalCmds bool
}

type SecurityConfig struct {
	HTTPTimeout      time.Duration
	MaxHTTPRespBytes int64
	AllowedHosts     []string // empty = deny all
	DenyPrivateNet   bool
}

func Load() (*Config, error) {
	c := &Config{
		LLM: LLMConfig{
			BaseURL:      env("LLM_BASE_URL", "https://api.openai.com/v1"),
			APIKey:       os.Getenv("LLM_API_KEY"),
			Model:        env("LLM_MODEL", "gpt-4o-mini"),
			Timeout:      envDur("LLM_TIMEOUT", 120*time.Second),
			MaxRetries:   envInt("LLM_MAX_RETRIES", 3),
			MaxInputCh:   envInt("LLM_MAX_INPUT_CHARS", 100_000),
			MaxOutputTok: envInt("LLM_MAX_OUTPUT_TOKENS", 4096),
		},
		Telegram: TelegramConfig{
			BotToken:       os.Getenv("TELEGRAM_BOT_TOKEN"),
			MaxMsgLen:      envInt("TG_MAX_MSG_LEN", 4000),
			RatePerMin:     envInt("TG_RATE_PER_MIN", 20),
			MaxActiveTasks: envInt("TG_MAX_ACTIVE_TASKS", 3),
		},
		Storage: StorageConfig{
			DataDir:       env("HERMES_DATA_DIR", "./data"),
			DBPath:        env("HERMES_DB_PATH", "./data/hermes.db"),
			RetentionDays: envInt("MEMORY_RETENTION_DAYS", 90),
		},
		Agent: AgentConfig{
			MaxIterations:       envInt("AGENT_MAX_ITERATIONS", 25),
			MaxToolCalls:        envInt("AGENT_MAX_TOOL_CALLS", 50),
			ToolTimeout:         envDur("TOOL_TIMEOUT", 60*time.Second),
			TaskTimeout:         envDur("TASK_TIMEOUT", 10*time.Minute),
			MaxTokensPerTask:    envInt("TASK_MAX_TOKENS", 200_000),
			SkillsDir:           env("SKILLS_DIR", "./skills"),
			WorkspaceRoot:       env("WORKSPACE_ROOT", "./workspace"),
			ShellAllowlist:      envList("SHELL_ALLOWLIST"),
			MaxFileBytes:        int64(envInt("MAX_FILE_BYTES", 10_000_000)),
			MaxOutputBytes:      int64(envInt("MAX_OUTPUT_BYTES", 200_000)),
			RequireApprovalCmds: envBool("REQUIRE_APPROVAL_CMDS", true),
		},
		Security: SecurityConfig{
			HTTPTimeout:      envDur("HTTP_TIMEOUT", 30*time.Second),
			MaxHTTPRespBytes: int64(envInt("MAX_HTTP_RESP_BYTES", 5_000_000)),
			AllowedHosts:     envList("HTTP_ALLOWED_HOSTS"),
			DenyPrivateNet:   envBool("HTTP_DENY_PRIVATE_NET", true),
		},
		HealthAddr: env("HEALTH_ADDR", "127.0.0.1:8089"),
	}

	// Validasi wajib
	if c.LLM.APIKey == "" {
		return nil, errors.New("config: LLM_API_KEY wajib di-set")
	}
	if c.Telegram.BotToken == "" {
		return nil, errors.New("config: TELEGRAM_BOT_TOKEN wajib di-set")
	}
	if c.Agent.MaxIterations <= 0 || c.Agent.MaxToolCalls <= 0 {
		return nil, errors.New("config: AGENT_MAX_ITERATIONS dan AGENT_MAX_TOOL_CALLS harus > 0")
	}
	if _, err := os.Stat(c.Storage.DataDir); err != nil {
		if err := os.MkdirAll(c.Storage.DataDir, 0o700); err != nil {
			return nil, fmt.Errorf("config: buat data dir: %w", err)
		}
	}
	return c, nil
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return d
}
func envDur(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d2, err := time.ParseDuration(v); err == nil {
			return d2
		}
	}
	return d
}
func envBool(k string, d bool) bool {
	if v := os.Getenv(k); v != "" {
		return v == "1" || v == "true" || v == "yes"
	}
	return d
}
func envList(k string) []string {
	if v := os.Getenv(k); v != "" {
		return strings.Split(v, ",")
	}
	return nil
}
