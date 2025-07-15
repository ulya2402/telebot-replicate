package main

import (
	"log"
	"telegram-ai-bot/internal/bot"
	"telegram-ai-bot/internal/config"
	"telegram-ai-bot/internal/database"
	"telegram-ai-bot/internal/localization"
	"telegram-ai-bot/internal/services"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	
)

func main() {
	cfg := config.Load()
	models := config.LoadModels("models.json")
	templates := config.LoadTemplates("templates/templates.json")
	localizer := localization.New("locales")
	dbClient := database.NewClient(cfg)
	
	replicateClient, err := services.NewReplicateClient(cfg.ReplicateAPIToken)
	if err != nil {
		log.Fatalf(err.Error())
	}

	api, err := tgbotapi.NewBotAPI(cfg.TelegramBotToken)
	if err != nil {
		log.Fatalf("FATAL: Failed to create bot: %v", err)
	}

	api.Debug = false
	log.Printf("INFO: Authorized on account %s", api.Self.UserName)

	handler := bot.NewHandler(api, dbClient, localizer, models, templates, replicateClient, cfg)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := api.GetUpdatesChan(u)

	for update := range updates {
		go handler.HandleUpdate(update)
	}
}