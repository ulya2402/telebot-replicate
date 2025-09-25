package main

import (
	"log"
	"telegram-ai-bot/internal/bot"
	"telegram-ai-bot/internal/config"
	"telegram-ai-bot/internal/database"
	"telegram-ai-bot/internal/localization"
	"telegram-ai-bot/internal/payments"
	"telegram-ai-bot/internal/services"


	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func main() {
	log.Println("INFO: Starting the bot application...")
	cfg := config.Load()
	providers := config.LoadProviders("providers.json") // <-- BARU
	models := config.LoadModels("models.json")
	templates := config.LoadTemplates("templates/templates.json")
	styles := config.LoadStyles("styles.json")
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

	// PERBAIKAN: Inisialisasi paymentHandler sebelum handler utama
	paymentHandler := payments.NewPaymentHandler(api, dbClient, localizer, cfg.PaymentProviderToken, cfg.ManualPaymentInfo, "internal/payments/packages.json", "bmac_packages.json")

	// PERBAIKAN: paymentHandler diberikan sebagai argumen saat membuat handler utama
	handler := bot.NewHandler(api, dbClient, localizer, providers, models, templates, styles, replicateClient, cfg, paymentHandler)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := api.GetUpdatesChan(u)

	for update := range updates {
		log.Println("DEBUG: Received an update from Telegram")
		if update.Message != nil {
			log.Printf("DEBUG: Update is a Message: [%s]", update.Message.Text)
		} else if update.CallbackQuery != nil {
			log.Printf("DEBUG: Update is a CallbackQuery: [%s]", update.CallbackQuery.Data)
		} else if update.PreCheckoutQuery != nil {
			log.Printf("DEBUG: Update is a PreCheckoutQuery")
		} else {
			log.Printf("DEBUG: Update is of another type")
		}

		go handler.HandleUpdate(update)
	}


}