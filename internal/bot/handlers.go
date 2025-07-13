package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"telegram-ai-bot/internal/config"
	"telegram-ai-bot/internal/database"
	"telegram-ai-bot/internal/localization"
	"telegram-ai-bot/internal/services"
	"time"
	"html"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Handler struct {
	Bot             *tgbotapi.BotAPI
	DB              *database.Client
	Localizer       *localization.Localizer
	Models          []config.Model
	PromptTemplates []config.PromptTemplate
	Replicate       *services.ReplicateClient
	userStates      map[int64]string
}

func NewHandler(api *tgbotapi.BotAPI, db *database.Client, localizer *localization.Localizer, models []config.Model, templates []config.PromptTemplate, replicate *services.ReplicateClient) *Handler {
	return &Handler{
		Bot:             api,
		DB:              db,
		Localizer:       localizer,
		Models:          models,
		PromptTemplates: templates,
		Replicate:       replicate,
		userStates:      make(map[int64]string),
	}
}

func (h *Handler) HandleUpdate(update tgbotapi.Update) {
	switch {
	case update.Message != nil:
		if update.Message.IsCommand() {
			h.handleCommand(update.Message)
		} else {
			h.handleMessage(update.Message)
		}
	case update.CallbackQuery != nil:
		h.handleCallbackQuery(update.CallbackQuery)
	}
}

func (h *Handler) handleCommand(message *tgbotapi.Message) {
	switch message.Command() {
	case "start":
		h.handleStart(message)
	case "help":
		h.handleHelp(message)
	case "img", "gen":
		h.handleImageCommand(message)
	case "profile", "status":
		h.handleProfile(message)
	case "referral":
		h.handleReferral(message)
	case "lang":
		h.handleLang(message)
	case "cancel": // LOGIKA BARU UNTUK /cancel
		h.handleCancel(message)
	default:
		msg := tgbotapi.NewMessage(message.Chat.ID, "Unknown command")
		h.Bot.Send(msg)
	}
}

func (h *Handler) handleCallbackQuery(callback *tgbotapi.CallbackQuery) {
	parts := strings.Split(callback.Data, ":")
	action := parts[0]
	data := ""
	if len(parts) > 1 {
		data = parts[1]
	}

	h.Bot.Request(tgbotapi.NewCallback(callback.ID, ""))

	switch action {
	case "model_page":
		page, _ := strconv.Atoi(data)
		h.navigateModels(callback, page)
	case "model_select":
		h.handleModelSelection(callback, data)
	case "lang_select":
		h.handleLangSelection(callback, data)
	case "show_templates":
		page, _ := strconv.Atoi(data)
		h.showTemplates(callback, page)
	case "template_page":
		page, _ := strconv.Atoi(data)
		h.navigateTemplates(callback, page)
	case "template_select":
		h.handleTemplateSelection(callback, data)
	case "cancel_flow": // LOGIKA BARU UNTUK TOMBOL BATAL
		h.handleCancelCallback(callback)
	}
}

func (h *Handler) handleCancel(message *tgbotapi.Message) {
	if _, ok := h.userStates[message.From.ID]; ok {
		delete(h.userStates, message.From.ID)
		
		user, _ := h.getOrCreateUser(message.From)
		lang := user.LanguageCode
		
		msg := tgbotapi.NewMessage(message.Chat.ID, h.Localizer.Get(lang, "flow_cancelled"))
		h.Bot.Send(msg)
	}
}

// FUNGSI BARU UNTUK TOMBOL BATAL
func (h *Handler) handleCancelCallback(callback *tgbotapi.CallbackQuery) {
	if _, ok := h.userStates[callback.From.ID]; ok {
		delete(h.userStates, callback.From.ID)
		
		user, _ := h.getOrCreateUser(callback.From)
		lang := user.LanguageCode
		
		// Edit pesan sebelumnya untuk menghapus tombol dan memberi konfirmasi
		responseText := h.Localizer.Get(lang, "flow_cancelled")
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, responseText)
		h.Bot.Send(msg)
	}
}


func (h *Handler) showTemplates(callback *tgbotapi.CallbackQuery, page int) {
	user, _ := h.getOrCreateUser(callback.From)
	keyboard := h.createTemplateSelectionKeyboard(h.PromptTemplates, user.LanguageCode, page)
	msg := tgbotapi.NewEditMessageReplyMarkup(callback.Message.Chat.ID, callback.Message.MessageID, keyboard)
	h.Bot.Send(msg)
}

func (h *Handler) navigateTemplates(callback *tgbotapi.CallbackQuery, page int) {
	user, _ := h.getOrCreateUser(callback.From)
	keyboard := h.createTemplateSelectionKeyboard(h.PromptTemplates, user.LanguageCode, page)
	msg := tgbotapi.NewEditMessageReplyMarkup(callback.Message.Chat.ID, callback.Message.MessageID, keyboard)
	h.Bot.Send(msg)
}

func (h *Handler) handleTemplateSelection(callback *tgbotapi.CallbackQuery, templateID string) {
	modelID, ok := h.userStates[callback.From.ID]
	if !ok {
		return
	}
	var selectedTemplate *config.PromptTemplate
	for _, t := range h.PromptTemplates {
		if t.ID == templateID {
			selectedTemplate = &t
			break
		}
	}
	if selectedTemplate == nil {
		return
	}
	user, _ := h.getOrCreateUser(callback.From)
	h.triggerImageGeneration(user, modelID, selectedTemplate.Prompt, callback.Message.Chat.ID)
	deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
	h.Bot.Send(deleteMsg)
}


func (h *Handler) handleModelSelection(callback *tgbotapi.CallbackQuery, modelID string) {
	user, err := h.getOrCreateUser(callback.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode

	var selectedModel *config.Model
	for _, m := range h.Models {
		if m.ID == modelID {
			selectedModel = &m
			break
		}
	}
	if selectedModel == nil {
		return
	}

	if user.Credits < selectedModel.Cost {
		args := map[string]string{"required": strconv.Itoa(selectedModel.Cost), "balance": strconv.Itoa(user.Credits)}
		text := h.Localizer.Getf(lang, "insufficient_credits", args)
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, text)
		h.Bot.Send(msg)
		return
	}

	h.userStates[user.TelegramID] = modelID
	text := h.Localizer.Get(lang, "enter_prompt")
	msg := tgbotapi.NewMessage(callback.Message.Chat.ID, text)

	// Buat keyboard dengan tombol Template dan Batal
	templateButton := tgbotapi.NewInlineKeyboardButtonData("✨ Choose from Template", "show_templates:0")
	cancelButton := tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "cancel_button"), "cancel_flow")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(templateButton, cancelButton))
	
	h.Bot.Send(msg)

	deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
	h.Bot.Send(deleteMsg)
}

func (h *Handler) handleMessage(message *tgbotapi.Message) {
	modelID, ok := h.userStates[message.From.ID]
	if !ok {
		return
	}
	user, _ := h.getOrCreateUser(message.From)
	h.triggerImageGeneration(user, modelID, message.Text, message.Chat.ID)
}


func (h *Handler) triggerImageGeneration(user *database.User, modelID, prompt string, chatID int64) {
	delete(h.userStates, user.TelegramID)
	lang := user.LanguageCode

	var selectedModel *config.Model
	for _, m := range h.Models {
		if m.ID == modelID {
			selectedModel = &m
			break
		}
	}
	if selectedModel == nil || user.Credits < selectedModel.Cost {
		return
	}

	waitMsg := tgbotapi.NewMessage(chatID, h.Localizer.Get(lang, "generating"))
	sentMsg, _ := h.Bot.Send(waitMsg)
	defer h.Bot.Send(tgbotapi.NewDeleteMessage(chatID, sentMsg.MessageID))

	// Konteks sederhana, tidak perlu bisa dibatalkan dari luar
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	imageUrls, err := h.Replicate.CreatePrediction(ctx, selectedModel.ReplicateID, prompt)
	if err != nil || len(imageUrls) == 0 {
		h.Bot.Send(tgbotapi.NewMessage(chatID, h.Localizer.Get(lang, "generation_failed")))
		return
	}

	user.Credits -= selectedModel.Cost
	user.GeneratedImageCount++
	h.DB.UpdateUser(user)

	if user.GeneratedImageCount == 3 && user.ReferrerID != 0 {
		referrer, err := h.DB.GetUserByTelegramID(user.ReferrerID)
		if err == nil && referrer != nil {
			referrer.Credits += 5
			h.DB.UpdateUser(referrer)
			log.Printf("INFO: Awarded 5 referral credits to user %d", referrer.TelegramID)
		}
	}

	safePrompt := html.EscapeString(prompt)
	caption := fmt.Sprintf("Prompt: <code>%s</code>\nModel: %s\nCost: %d 💎", safePrompt, selectedModel.Name, selectedModel.Cost)

	if len(imageUrls) == 1 {
		msg := tgbotapi.NewPhoto(chatID, tgbotapi.FileURL(imageUrls[0]))
		msg.Caption = caption
		msg.ParseMode = "HTML"
		h.Bot.Send(msg)
	} else {
		var media []interface{}
		for i, url := range imageUrls {
			photo := tgbotapi.NewInputMediaPhoto(tgbotapi.FileURL(url))
			if i == 0 {
				photo.Caption = caption
				photo.ParseMode = "HTML"
			}
			media = append(media, photo)
		}
		msg := tgbotapi.NewMediaGroup(chatID, media)
		h.Bot.Send(msg)
	}
}


func (h *Handler) navigateModels(callback *tgbotapi.CallbackQuery, page int) {
	user, _ := h.getOrCreateUser(callback.From)
	keyboard := h.createModelSelectionKeyboard(h.Models, user.LanguageCode, page)
	msg := tgbotapi.NewEditMessageReplyMarkup(callback.Message.Chat.ID, callback.Message.MessageID, keyboard)
	h.Bot.Send(msg)
}

// Sisa fungsi-fungsi dari langkah sebelumnya (TIDAK BERUBAH)
func (h *Handler) handleStart(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	if strings.HasPrefix(message.CommandArguments(), "ref_") && user.ReferrerID == 0 {
		referrerID, err := strconv.ParseInt(strings.TrimPrefix(message.CommandArguments(), "ref_"), 10, 64)
		if err == nil && referrerID != user.TelegramID {
			user.ReferrerID = referrerID
			h.DB.UpdateUser(user)
			log.Printf("INFO: User %d was referred by %d", user.TelegramID, referrerID)
		}
	}
	welcomeArgs := map[string]string{"name": message.From.FirstName}
	text := h.Localizer.Getf(user.LanguageCode, "welcome", welcomeArgs)
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	h.Bot.Send(msg)
}

func (h *Handler) handleHelp(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	text := h.Localizer.Get(user.LanguageCode, "help")
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	h.Bot.Send(msg)
}

func (h *Handler) handleProfile(message *tgbotapi.Message) {
	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode
	var resetTimeStr string
	if user.IsPremium {
		resetTimeStr = "N/A (Premium)"
	} else {
		nextReset := user.LastReset.Add(24 * time.Hour)
		duration := time.Until(nextReset)
		if duration <= 0 {
			resetTimeStr = h.Localizer.Get(lang, "time_now")
		} else {
			resetTimeStr = h.formatDuration(duration, lang)
		}
	}
	args := map[string]string{
		"id":         strconv.FormatInt(user.TelegramID, 10),
		"credits":    strconv.Itoa(user.Credits),
		"reset_time": resetTimeStr,
	}
	text := h.Localizer.Getf(lang, "profile", args)
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	h.Bot.Send(msg)
}

func (h *Handler) handleReferral(message *tgbotapi.Message) {
	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode
	referralLink := fmt.Sprintf("https://t.me/%s?start=ref_%d", h.Bot.Self.UserName, user.TelegramID)
	text := fmt.Sprintf("%s\n\n%s\n`%s`",
		h.Localizer.Get(lang, "referral_message"),
		h.Localizer.Get(lang, "referral_link_text"),
		referralLink,
	)
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	h.Bot.Send(msg)
}

func (h *Handler) handleLang(message *tgbotapi.Message) {
	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode
	text := h.Localizer.Get(lang, "lang_select")
	keyboard := h.createLanguageSelectionKeyboard()
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ReplyMarkup = keyboard
	h.Bot.Send(msg)
}

func (h *Handler) handleLangSelection(callback *tgbotapi.CallbackQuery, langCode string) {
	user, err := h.getOrCreateUser(callback.From)
	if err != nil {
		return
	}
	user.LanguageCode = langCode
	err = h.DB.UpdateUser(user)
	if err != nil {
		log.Printf("ERROR: Failed to update language for user %d: %v", user.TelegramID, err)
		return
	}
	confirmationText := h.Localizer.Get(langCode, "lang_updated")
	msg := tgbotapi.NewMessage(callback.Message.Chat.ID, confirmationText)
	h.Bot.Send(msg)
	editMsg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, callback.Message.Text)
	h.Bot.Send(editMsg)
}

func (h *Handler) formatDuration(d time.Duration, lang string) string {
	h_unit := h.Localizer.Get(lang, "time_hours")
	m_unit := h.Localizer.Get(lang, "time_minutes")
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%d %s, %d %s", hours, h_unit, minutes, m_unit)
}

func (h *Handler) getOrCreateUser(tgUser *tgbotapi.User) (*database.User, error) {
	user, err := h.DB.GetUserByTelegramID(tgUser.ID)
	if err != nil {
		log.Printf("ERROR: Database error on getOrCreateUser for user %d: %v", tgUser.ID, err)
		return nil, err
	}
	if user == nil {
		newUser := database.User{
			TelegramID: tgUser.ID,
			Username:   tgUser.UserName,
			Credits:      5,          // <-- Tambahkan ini
			LastReset:    time.Now(), // <-- Tambahkan ini
			LanguageCode: "en", 
		}
		user, err = h.DB.CreateUser(newUser)
		if err != nil {
			log.Printf("ERROR: Failed to create user %d in database: %v", tgUser.ID, err)
			return nil, err
		}
	}
	return user, nil
}

func (h *Handler) handleImageCommand(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode
	text := h.Localizer.Get(lang, "choose_model")
	keyboard := h.createModelSelectionKeyboard(h.Models, lang, 0)
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ReplyMarkup = keyboard
	h.Bot.Send(msg)
}