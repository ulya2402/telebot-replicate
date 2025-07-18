package config

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	TelegramBotToken   string
	SupabaseURL        string
	SupabaseServiceKey string
	ReplicateAPIToken  string
	AdminTelegramIDs   []int64
	WelcomeImageURL    string
	PaymentProviderToken string `json:"payment_provider_token"` // <-- BARU
	ManualPaymentInfo    string `json:"manual_payment_info"`
	ForceSubscribeChannelID int64
}

type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description               string `json:"description"`
	ReplicateID string `json:"replicate_id"`
	Tier        string `json:"tier"`
	Cost        int    `json:"cost"`
	Enabled     bool   `json:"enabled"`
	AcceptsImageInput bool   `json:"accepts_image_input"`
	ConfigurableAspectRatio bool `json:"configurable_aspect_ratio"` // <-- Tambahkan ini
	ConfigurableNumOutputs  bool `json:"configurable_num_outputs"`
	ShowTemplates             bool   `json:"show_templates"`
}

type PromptTemplate struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Prompt string `json:"prompt"`
}

type Provider struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}


func Load() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("INFO: No .env file found, reading from environment variables")
	}

	adminIDsStr := getEnv("ADMIN_TELEGRAM_IDS", "")
	var adminIDs []int64
	if adminIDsStr != "" {
		parts := strings.Split(adminIDsStr, ",")
		for _, part := range parts {
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				log.Printf("WARN: Invalid admin Telegram ID: %s", part)
				continue
			}
			adminIDs = append(adminIDs, id)
		}
	}
	if len(adminIDs) > 0 {
		log.Printf("INFO: Loaded %d admin(s)", len(adminIDs))
	}

	channelIDStr := getEnv("FORCE_SUBSCRIBE_CHANNEL_ID", "0")
	channelID, err := strconv.ParseInt(channelIDStr, 10, 64)
	if err != nil {
		log.Fatalf("FATAL: Invalid FORCE_SUBSCRIBE_CHANNEL_ID: %s", channelIDStr)
	}
	if channelID != 0 {
		log.Printf("INFO: Force subscribe is enabled for channel ID: %d", channelID)
	}
	// <-- SELESAI BLOK BARU

	return &Config{
		TelegramBotToken:   getEnv("TELEGRAM_BOT_TOKEN", ""),
		SupabaseURL:        getEnv("SUPABASE_URL", ""),
		SupabaseServiceKey: getEnv("SUPABASE_SERVICE_KEY", ""),
		ReplicateAPIToken:  getEnv("REPLICATE_API_TOKEN", ""),
		AdminTelegramIDs:   adminIDs,
		WelcomeImageURL:    getEnv("WELCOME_IMAGE_URL", ""),
		PaymentProviderToken: getEnv("PAYMENT_PROVIDER_TOKEN", ""), // <-- BARU
		ManualPaymentInfo:    getEnv("MANUAL_PAYMENT_INFO", "Untuk pembayaran manual, silakan transfer ke:\nBank ABC: `1234567890` a.n. John Doe\n\nKirim bukti transfer ke @Admin."), // <-- BARU
		ForceSubscribeChannelID: channelID,
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

func LoadProviders(file string) []Provider {
	data, err := ioutil.ReadFile(file)
	if err != nil {
		log.Fatalf("FATAL: Could not read providers file %s: %v", file, err)
	}

	var providers []Provider
	if err := json.Unmarshal(data, &providers); err != nil {
		log.Fatalf("FATAL: Could not parse providers file %s: %v", file, err)
	}

	log.Printf("INFO: Loaded %d providers", len(providers))
	return providers
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