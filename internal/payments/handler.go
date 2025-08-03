package payments

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"strconv"
	"telegram-ai-bot/internal/config"
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
	BMACPackages []config.BMACCreditPackage
	Token      string
	ManualInfo string
}

func NewPaymentHandler(bot *tgbotapi.BotAPI, db *database.Client, loc *localization.Localizer, token, manualInfo, packagesFile,  bmacPackagesFile string) *PaymentHandler {
	packages := loadPackages(packagesFile)
	bmacPackages := config.LoadBMACPackages(bmacPackagesFile) 
	return &PaymentHandler{
		Bot:        bot,
		DB:         db,
		Localizer:  loc,
		Packages:   packages,
		BMACPackages: bmacPackages,
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

func (ph *PaymentHandler) ShowTopUpOptions(chatID int64, messageID ...int) {
	lang := "en"
	text := ph.Localizer.Get(lang, "topup_select_method")
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⭐️ Pay With Stars", "topup_stars"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💳 Manual Pay", "topup_manual"),
		),
	)
	if len(messageID) > 0 {
		msg := tgbotapi.NewEditMessageText(chatID, messageID[0], text)
		msg.ReplyMarkup = &keyboard
		msg.ParseMode = "html"
		ph.Bot.Send(msg)
	} else { // Jika tidak, kirim pesan baru.
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ReplyMarkup = &keyboard
		msg.ParseMode = "html"
		ph.Bot.Send(msg)
	}
}

func (ph *PaymentHandler) ShowStarsPackages(chatID int64, messageID int) {
	lang := "en"
	text := ph.Localizer.Get(lang, "topup_select_package")
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, pkg := range ph.Packages {
		buttonText := fmt.Sprintf("%s (%d Stars)", pkg.Title, pkg.StarsAmount)
		button := tgbotapi.NewInlineKeyboardButtonData(buttonText, "buy_stars:"+pkg.ID)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(button))
	}
	
	backButton := tgbotapi.NewInlineKeyboardButtonData(ph.Localizer.Get(lang, "back_button"), "topup_back_to_main")
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(backButton))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	msg.ReplyMarkup = &keyboard
	msg.ParseMode = "html"
	ph.Bot.Send(msg)
}

// internal/payments/handler.go (SESUDAH)

func (ph *PaymentHandler) ShowBMACPackages(chatID int64, messageID int) {
	lang := ph.getUserLang(chatID)
	text := ph.Localizer.Get(lang, "topup_bmac_select_package") + "\n\n" + ph.Localizer.Get(lang, "topup_bmac_instructions")
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, pkg := range ph.BMACPackages {
		buttonText := fmt.Sprintf("%s (%d Credits)", pkg.ProductName, pkg.CreditsAmount)
		button := tgbotapi.NewInlineKeyboardButtonURL(buttonText, pkg.ProductURL)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(button))
	}

	backButton := tgbotapi.NewInlineKeyboardButtonData(ph.Localizer.Get(lang, "back_button"), "topup_back_to_manual")
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(backButton))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = &keyboard
	ph.Bot.Send(msg)
}

func (ph *PaymentHandler) getUserLang(userID int64) string {
    user, err := ph.DB.GetUserByTelegramID(userID)
    if err == nil && user != nil {
        return user.LanguageCode
    }
    return "en"
}

func (ph *PaymentHandler) ShowManualPaymentInfo(chatID int64, messageID int) {
	lang := ph.getUserLang(chatID)
	text := ph.ManualInfo
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(ph.Localizer.Get(lang, "back_button"), "topup_back_to_manual"),
		),
	)

	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	msg.ReplyMarkup = &keyboard
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

func (ph *PaymentHandler) ShowManualPaymentOptions(chatID int64, messageID int) {
	lang := ph.getUserLang(chatID)
	text := ph.Localizer.Get(lang, "topup_manual_select_method")
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(ph.Localizer.Get(lang, "button_bmac"), "topup_bmac"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(ph.Localizer.Get(lang, "button_manual_transfer"), "topup_transfer_bank"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(ph.Localizer.Get(lang, "back_button"), "topup_back_to_main"),
		),
	)

	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	msg.ReplyMarkup = &keyboard
	msg.ParseMode = "HTML"
	ph.Bot.Send(msg)
}