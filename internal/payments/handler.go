package payments

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"strconv"
	"telegram-ai-bot/internal/database"
	"telegram-ai-bot/internal/localization"
	

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type CreditPackage struct {
	ID            string `json:"id"`
	StarsAmount   int    `json:"stars_amount"`
	CreditsAmount int    `json:"credits_amount"`
	Title         string `json:"title"`
	Description   string `json:"description"`
}

type PaymentHandler struct {
	Bot        *tgbotapi.BotAPI
	DB         *database.Client
	Localizer  *localization.Localizer
	Packages   []CreditPackage
	Token      string
	ManualInfo string
}

func NewPaymentHandler(bot *tgbotapi.BotAPI, db *database.Client, loc *localization.Localizer, token, manualInfo, packagesFile string) *PaymentHandler {
	packages := loadPackages(packagesFile)
	return &PaymentHandler{
		Bot:        bot,
		DB:         db,
		Localizer:  loc,
		Packages:   packages,
		Token:      token,
		ManualInfo: manualInfo,
	}
}

func loadPackages(file string) []CreditPackage {
	data, err := ioutil.ReadFile(file)
	if err != nil {
		log.Fatalf("FATAL: Could not read packages file %s: %v", file, err)
	}
	var packages []CreditPackage
	if err := json.Unmarshal(data, &packages); err != nil {
		log.Fatalf("FATAL: Could not parse packages file %s: %v", file, err)
	}
	log.Printf("INFO: Loaded %d credit packages", len(packages))
	return packages
}

// --- PERBAIKAN FINAL ---
// File: internal/payments/handler.go

func (ph *PaymentHandler) HandleStarsInvoice(chatID int64, packageID string) {
	var selectedPackage *CreditPackage
	for _, pkg := range ph.Packages {
		if pkg.ID == packageID {
			selectedPackage = &pkg
			break
		}
	}
	if selectedPackage == nil {
		log.Printf("WARN: Invalid package ID selected: %s", packageID)
		return
	}

	// --- PERBAIKAN FINAL GABUNGAN ---

	// 1. Gunakan NewInvoice dengan 8 argumen yang benar untuk menghindari error kompilasi.
	invoice := tgbotapi.NewInvoice(
		chatID,
		selectedPackage.Title,
		selectedPackage.Description,
		packageID,
		ph.Token, // Menggunakan token pembayaran yang benar
		"start_parameter", // Parameter ini bisa diisi string kosong atau dummy
		"XTR",
		[]tgbotapi.LabeledPrice{
			{Label: fmt.Sprintf("%d Credits", selectedPackage.CreditsAmount), Amount: selectedPackage.StarsAmount},
		},
	)
	
	// Tambahan parameter wajib dari Telegram API
	invoice.MaxTipAmount = 0
	invoice.SuggestedTipAmounts = []int{}


	// --- SELESAI PERBAIKAN ---

	_, err := ph.Bot.Send(invoice)
	if err != nil {
		log.Printf("ERROR: Failed to send invoice: %v", err)
	}
}
// --- SELESAI PERBAIKAN ---

func (ph *PaymentHandler) ShowTopUpOptions(chatID int64) {
	lang := "id"
	text := ph.Localizer.Get(lang, "topup_select_method")
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⭐️ Pay With Stars", "topup_stars"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💳 Manual Pay", "topup_manual"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = &keyboard
	msg.ParseMode = "html"
	ph.Bot.Send(msg)
}

func (ph *PaymentHandler) ShowStarsPackages(chatID int64) {
	lang := "en"
	text := ph.Localizer.Get(lang, "topup_select_package")
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, pkg := range ph.Packages {
		buttonText := fmt.Sprintf("%s (%d Stars)", pkg.Title, pkg.StarsAmount)
		button := tgbotapi.NewInlineKeyboardButtonData(buttonText, "buy_stars:"+pkg.ID)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(button))
	}
	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = &keyboard
	msg.ParseMode = "html"
	ph.Bot.Send(msg)
}

func (ph *PaymentHandler) ShowManualPaymentInfo(chatID int64) {
	msg := tgbotapi.NewMessage(chatID, ph.ManualInfo)
	msg.ParseMode = "html"
	ph.Bot.Send(msg)
}

func (ph *PaymentHandler) HandlePreCheckoutQuery(query *tgbotapi.PreCheckoutQuery) {
	ok := true
	preCheckoutConfig := tgbotapi.PreCheckoutConfig{
		PreCheckoutQueryID: query.ID,
		OK:                 ok,
	}
	ph.Bot.Request(preCheckoutConfig)
}

func (ph *PaymentHandler) HandleSuccessfulPayment(message *tgbotapi.Message) {
	paymentInfo := message.SuccessfulPayment
	userID := message.From.ID
	lang := "en"

	var creditsToAdd int
	for _, pkg := range ph.Packages {
		if pkg.ID == paymentInfo.InvoicePayload {
			creditsToAdd = pkg.CreditsAmount
			break
		}
	}

	if creditsToAdd == 0 {
		log.Printf("ERROR: Successful payment for unknown payload '%s' from user %d", paymentInfo.InvoicePayload, userID)
		return
	}

	user, err := ph.DB.GetUserByTelegramID(userID)
	if err != nil || user == nil {
		log.Printf("ERROR: User %d paid successfully but could not be found in DB", userID)
		return
	}

	user.PaidCredits += creditsToAdd
	err = ph.DB.UpdateUser(user)
	if err != nil {
		log.Printf("ERROR: Failed to add %d credits to user %d after successful payment", creditsToAdd, userID)
		ph.Bot.Send(tgbotapi.NewMessage(userID, ph.Localizer.Get(lang, "topup_error_admin")))
		return
	}

	log.Printf("INFO: User %d successfully purchased %d credits.", userID, creditsToAdd)
	args := map[string]string{
		"credits": strconv.Itoa(creditsToAdd),
		"balance": strconv.Itoa(user.PaidCredits + user.FreeCredits),
	}
	successText := ph.Localizer.Getf(lang, "topup_success", args)

	msg := tgbotapi.NewMessage(userID, successText)
	msg.ParseMode = "Markdown" 
	ph.Bot.Send(msg)
}