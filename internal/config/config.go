package config

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	TelegramBotToken   string
	SupabaseURL        string
	SupabaseServiceKey string
	ReplicateAPIToken  string
}

type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ReplicateID string `json:"replicate_id"`
	Tier        string `json:"tier"`
	Cost        int    `json:"cost"`
	Enabled     bool   `json:"enabled"`
}

type PromptTemplate struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Prompt string `json:"prompt"`
}


func Load() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("INFO: No .env file found, reading from environment variables")
	}

	return &Config{
		TelegramBotToken:   getEnv("TELEGRAM_BOT_TOKEN", ""),
		SupabaseURL:        getEnv("SUPABASE_URL", ""),
		SupabaseServiceKey: getEnv("SUPABASE_SERVICE_KEY", ""),
		ReplicateAPIToken:  getEnv("REPLICATE_API_TOKEN", ""),
	}
}

func LoadModels(file string) []Model {
	data, err := ioutil.ReadFile(file)
	if err != nil {
		log.Fatalf("FATAL: Could not read models file %s: %v", file, err)
	}

	var allModels []Model
	if err := json.Unmarshal(data, &allModels); err != nil {
		log.Fatalf("FATAL: Could not parse models file %s: %v", file, err)
	}

	var enabledModels []Model
	for _, m := range allModels {
		if m.Enabled {
			enabledModels = append(enabledModels, m)
		}
	}

	log.Printf("INFO: Loaded %d enabled models", len(enabledModels))
	return enabledModels
}

func LoadTemplates(file string) []PromptTemplate {
    data, err := ioutil.ReadFile(file)
    if err != nil {
        log.Fatalf("FATAL: Could not read templates file %s: %v", file, err)
    }

    var templates []PromptTemplate
    if err := json.Unmarshal(data, &templates); err != nil {
        log.Fatalf("FATAL: Could not parse templates file %s: %v", file, err)
    }

    log.Printf("INFO: Loaded %d prompt templates", len(templates))
    return templates
}


func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	if fallback == "" {
		log.Fatalf("FATAL: Environment variable %s is not set.", key)
	}
	return fallback
}