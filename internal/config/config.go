package config

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Telegram TelegramConfig
	LLM      LLMConfig
}

type TelegramConfig struct {
	BotToken string
}

type LLMConfig struct {
	APIKey   string
	Provider string
	Model    string
}

func Load() (*Config, error) {
	godotenv.Load()

	return &Config{
		Telegram: TelegramConfig{
			BotToken: getEnv("TELEGRAM_BOT_TOKEN", ""),
		},
		LLM: LLMConfig{
			APIKey:   getEnv("LLM_API_KEY", ""),
			Provider: getEnv("LLM_PROVIDER", "mistral"),
			Model:    getEnv("LLM_MODEL", "mistral-large-latest"),
		},
	}, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
