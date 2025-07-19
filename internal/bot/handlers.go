package bot

import (
	"context"
	"fmt"
	"log"
	"io/ioutil" // <-- TAMBAHKAN
	"net/http"  // <-- TAMBAHKAN
	"path/filepath" // <-- TAMBAHKAN
	"strconv"
	"telegram-ai-bot/internal/payments"
	"net/url"
	"sync"
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
	Providers       []config.Provider
	Models          []config.Model
	PromptTemplates []config.PromptTemplate
	Replicate       *services.ReplicateClient
	userStates      map[int64]string
	userStatesMutex sync.Mutex
	Config          *config.Config
	lastGeneratedURLs     map[int64][]string // <-- BARU: Untuk menyimpan URL RAW
	lastGeneratedURLsMutex sync.Mutex 
	PaymentHandler *payments.PaymentHandler
	GroupHandler          *GroupHandler
}

func NewHandler(api *tgbotapi.BotAPI, db *database.Client, localizer *localization.Localizer, providers []config.Provider, models []config.Model, templates []config.PromptTemplate, replicate *services.ReplicateClient, cfg *config.Config, paymentHandler *payments.PaymentHandler) *Handler {
	h := &Handler{
		Bot:               api,
		DB:                db,
		Localizer:         localizer,
		Providers:         providers,
		Models:            models,
		PromptTemplates:   templates,
		Replicate:         replicate,
		userStates:        make(map[int64]string),
		Config:            cfg,
		lastGeneratedURLs: make(map[int64][]string),
		PaymentHandler:    paymentHandler,
	}
	h.GroupHandler = NewGroupHandler(h)
	return h
}

func (h *Handler) newReplyMessage(originalMessage *tgbotapi.Message, text string) tgbotapi.MessageConfig {
	msg := tgbotapi.NewMessage(originalMessage.Chat.ID, text)
	if originalMessage.Chat.IsGroup() || originalMessage.Chat.IsSuperGroup() {
		msg.ReplyToMessageID = originalMessage.MessageID
	}
	return msg
}

func (h *Handler) newReplyPhoto(originalMessage *tgbotapi.Message, photo tgbotapi.RequestFileData) tgbotapi.PhotoConfig {
	msg := tgbotapi.NewPhoto(originalMessage.Chat.ID, photo)
	if originalMessage.Chat.IsGroup() || originalMessage.Chat.IsSuperGroup() {
		msg.ReplyToMessageID = originalMessage.MessageID
	}
	return msg
}

func (h *Handler) newReplyDocument(originalMessage *tgbotapi.Message, file tgbotapi.RequestFileData) tgbotapi.DocumentConfig {
	doc := tgbotapi.NewDocument(originalMessage.Chat.ID, file)
	if originalMessage.Chat.IsGroup() || originalMessage.Chat.IsSuperGroup() {
		doc.ReplyToMessageID = originalMessage.MessageID
	}
	return doc
}

func (h *Handler) newReplyMediaGroup(originalMessage *tgbotapi.Message, media []interface{}) tgbotapi.MediaGroupConfig {
	msg := tgbotapi.NewMediaGroup(originalMessage.Chat.ID, media)
	if originalMessage.Chat.IsGroup() || originalMessage.Chat.IsSuperGroup() {
		msg.ReplyToMessageID = originalMessage.MessageID
	}
	return msg
}

func (h *Handler) isAdmin(userID int64) bool {
	for _, adminID := range h.Config.AdminTelegramIDs {
		if userID == adminID {
			return true
		}
	}
	return false
}

func (h *Handler) isUserSubscribed(userID int64) (bool, error) {
	if h.Config.ForceSubscribeChannelID == 0 {
		return true, nil
	}

	// ### PERBAIKAN DI SINI ###
	// Ternyata, ChatConfigWithUser harus dibungkus di dalam GetChatMemberConfig.
	getChatMemberConfig := tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
			ChatID: h.Config.ForceSubscribeChannelID,
			UserID: userID,
		},
	}
	// ### SELESAI PERBAIKAN ###

	member, err := h.Bot.GetChatMember(getChatMemberConfig)
	if err != nil {
		log.Printf("ERROR: Failed to get chat member for user %d in channel %d: %v", userID, h.Config.ForceSubscribeChannelID, err)
		return true, err
	}

	status := member.Status
	if status == "member" || status == "administrator" || status == "creator" {
		return true, nil
	}

	return false, nil
}

func (h *Handler) HandleUpdate(update tgbotapi.Update) {
	switch {
	case update.PreCheckoutQuery != nil:
		h.PaymentHandler.HandlePreCheckoutQuery(update.PreCheckoutQuery)
	case update.Message != nil && update.Message.SuccessfulPayment != nil:
		h.PaymentHandler.HandleSuccessfulPayment(update.Message)
	case update.Message != nil:
		// Jika pesan datang dari grup, serahkan ke GroupHandler
		if update.Message.Chat.IsGroup() || update.Message.Chat.IsSuperGroup() {
			h.GroupHandler.HandleGroupMessage(update.Message)
			return // Hentikan proses lebih lanjut di sini
		}

		// Jika dari chat pribadi, lanjutkan seperti biasa
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
	command := message.Command()

	// Cek apakah perintah ini khusus admin
	isAdminCommand := command == "stats" || command == "addcredits" || command == "broadcast"
	if isAdminCommand && !h.isAdmin(message.From.ID) {
		msg := h.newReplyMessage(message, h.Localizer.Get("en", "permission_denied"))
		h.Bot.Send(msg)
		return
	}

	if command == "referral" {
		subscribed, _ := h.isUserSubscribed(message.From.ID)
		if !subscribed {
			user, _ := h.getOrCreateUser(message.From)
			lang := user.LanguageCode

			chat, err := h.Bot.GetChat(tgbotapi.ChatInfoConfig{ChatConfig: tgbotapi.ChatConfig{ChatID: h.Config.ForceSubscribeChannelID}})
			if err != nil {
				log.Printf("ERROR: Could not get chat info for channel %d: %v", h.Config.ForceSubscribeChannelID, err)
				return
			}
			channelLink := fmt.Sprintf("https://t.me/%s", chat.UserName)

			text := h.Localizer.Get(lang, "force_subscribe_message")
			keyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonURL(h.Localizer.Get(lang, "force_subscribe_button"), channelLink),
				),
			)

			msg := h.newReplyMessage(message, text)
			msg.ReplyMarkup = &keyboard
			msg.ParseMode = "HTML"
			h.Bot.Send(msg)
			return // Hentikan proses jika belum subscribe
		}
	}

	// Jalankan perintah
	switch command {
	case "start":
		h.handleStart(message)
	case "group":
		h.handleGroupCommand(message)
	case "help":
		h.handleHelp(message)
	case "faq":
        h.handleFaq(message)
	case "img", "gen":
		h.handleImageCommand(message)
	case "profile", "status":
		h.handleProfile(message)
	case "referral":
		h.handleReferral(message)
	case "lang":
		h.handleLang(message)
	case "cancel":
		h.handleCancel(message)
	case "stats":
		h.handleStats(message)
	case "addcredits":
		h.handleAddCredits(message)
	case "broadcast":
		h.handleBroadcast(message)
	case "settings":
		h.handleSettings(message)
	case "topup":
		h.PaymentHandler.ShowTopUpOptions(message.Chat.ID)
	default:
		msg := h.newReplyMessage(message, "Unknown command")
		h.Bot.Send(msg)
	}
}




func (h *Handler) handleStats(message *tgbotapi.Message) {
	stats, err := h.DB.GetStatistics()
	if err != nil {
		log.Printf("ERROR: Failed to get statistics: %v", err)
		return
	}
	
	lang := "en" // Statistik biasanya dalam bahasa Inggris
	args := map[string]string{
		"total_users":     strconv.Itoa(stats.TotalUsers),
		"new_users_today": strconv.Itoa(stats.NewUsersToday),
		"premium_users":   strconv.Itoa(stats.PremiumUsers),
	}
	
	text := h.Localizer.Getf(lang, "stats_message", args)
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "Markdown"
	h.Bot.Send(msg)
}

func (h *Handler) handleAddCredits(message *tgbotapi.Message) {
	lang := "en"
	parts := strings.Fields(message.CommandArguments())
	if len(parts) != 2 {
		msg := h.newReplyMessage(message, h.Localizer.Get(lang, "addcredits_usage"))
		h.Bot.Send(msg)
		return
	}
	
	targetID, err1 := strconv.ParseInt(parts[0], 10, 64)
	amount, err2 := strconv.Atoi(parts[1])
	
	if err1 != nil || err2 != nil {
		msg := h.newReplyMessage(message, h.Localizer.Get(lang, "addcredits_usage"))
		h.Bot.Send(msg)
		return
	}
	
	targetUser, err := h.DB.GetUserByTelegramID(targetID)
	if err != nil || targetUser == nil {
		args := map[string]string{"user_id": parts[0]}
		msg := h.newReplyMessage(message, h.Localizer.Getf(lang, "addcredits_user_not_found", args))
		h.Bot.Send(msg)
		return
	}
	
	targetUser.PaidCredits += amount
	h.DB.UpdateUser(targetUser)
	
	args := map[string]string{
		"amount":  strconv.Itoa(amount),
		"user_id": parts[0],
	}
	msg := h.newReplyMessage(message, h.Localizer.Getf(lang, "addcredits_success", args))
	h.Bot.Send(msg)
}

func (h *Handler) handleBroadcast(message *tgbotapi.Message) {
	lang := "en"
	broadcastText := message.CommandArguments()
	if broadcastText == "" {
		msg := h.newReplyMessage(message, h.Localizer.Get(lang, "broadcast_usage"))
		h.Bot.Send(msg)
		return
	}

	allUsers, err := h.DB.GetAllUsers()
	if err != nil {
		log.Printf("ERROR: Failed to get all users for broadcast: %v", err)
		return
	}

	args := map[string]string{"user_count": strconv.Itoa(len(allUsers))}
	startMsg := h.newReplyMessage(message, h.Localizer.Getf(lang, "broadcast_started", args))
	h.Bot.Send(startMsg)

	// Jalankan broadcast di goroutine agar tidak memblokir bot
	go func(adminChatID int64) {
		sentCount := 0
		for _, user := range allUsers {
			msg := tgbotapi.NewMessage(user.TelegramID, broadcastText)
			msg.ParseMode = "HTML" 
			_, err := h.Bot.Send(msg)
			if err == nil {
				sentCount++
			}
			// Rate limiting: tunggu sebentar antar pesan untuk menghindari blokir Telegram
			time.Sleep(100 * time.Millisecond)
		}
		
		finishArgs := map[string]string{
			"sent_count":   strconv.Itoa(sentCount),
			"total_count": strconv.Itoa(len(allUsers)),
		}
		finishMsg := tgbotapi.NewMessage(adminChatID, h.Localizer.Getf(lang, "broadcast_finished", finishArgs))
		h.Bot.Send(finishMsg)
	}(message.Chat.ID)
}

func (h *Handler) handleGroupCommand(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode

	// Ambil teks instruksi dari file bahasa
	text := h.Localizer.Get(lang, "group_command_text")

	// Buat pesan balasan
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"

	// Buat dan lampirkan keyboard dengan tombol "Add to Group"
	keyboard := h.createAddToGroupKeyboard(lang, h.Bot.Self.UserName)
	msg.ReplyMarkup = &keyboard

	// Kirim pesan
	h.Bot.Send(msg)
}

func (h *Handler) handleSettings(message *tgbotapi.Message) {
	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode

	args := map[string]string{
		"aspect_ratio": user.AspectRatio,
		"num_images":   strconv.Itoa(user.NumOutputs),
	}
	text := h.Localizer.Getf(lang, "settings_menu", args)

	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "Markdown"
	// PERBAIKAN: Tambahkan '&' untuk mendapatkan pointer
	keyboard := h.createSettingsKeyboard(lang, user)
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

func (h *Handler) updateSettingsMessage(chatID int64, messageID int, user *database.User) {
	lang := user.LanguageCode
	args := map[string]string{
		"aspect_ratio": user.AspectRatio,
		"num_images":   strconv.Itoa(user.NumOutputs),
	}
	text := h.Localizer.Getf(lang, "settings_menu", args)

	// PERBAIKAN: Tambahkan '&' untuk mendapatkan pointer
	keyboard := h.createSettingsKeyboard(lang, user)
	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}


func (h *Handler) handleCallbackQuery(callback *tgbotapi.CallbackQuery) {
	parts := strings.Split(callback.Data, ":")
	action := parts[0]
	data := ""
	if len(parts) > 1 {
		data = parts[1]
	}

	h.Bot.Request(tgbotapi.NewCallback(callback.ID, ""))

	dummyMessage := &tgbotapi.Message{
		From: callback.From,
		Chat: &tgbotapi.Chat{ID: callback.Message.Chat.ID},
	}
if callback.Message.Chat.IsGroup() || callback.Message.Chat.IsSuperGroup() {
        dummyMessage.MessageID = callback.Message.MessageID
}

	if strings.HasPrefix(action, "buy_stars") {
		packageID := strings.Split(callback.Data, ":")[1]
		h.PaymentHandler.HandleStarsInvoice(callback.Message.Chat.ID, packageID)
		return
	}

	if action == "main_menu_referral" {
		subscribed, _ := h.isUserSubscribed(callback.From.ID)
		if !subscribed {
			user, _ := h.getOrCreateUser(callback.From)
			lang := user.LanguageCode

			chat, err := h.Bot.GetChat(tgbotapi.ChatInfoConfig{ChatConfig: tgbotapi.ChatConfig{ChatID: h.Config.ForceSubscribeChannelID}})
			if err != nil {
				log.Printf("ERROR: Could not get chat info for channel %d: %v", h.Config.ForceSubscribeChannelID, err)
				return
			}
			channelLink := fmt.Sprintf("https://t.me/%s", chat.UserName)

			text := h.Localizer.Get(lang, "force_subscribe_message")
			keyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonURL(h.Localizer.Get(lang, "force_subscribe_button"), channelLink),
				),
			)
			// Kirim sebagai pesan baru karena kita tidak bisa mengedit pesan foto dengan teks saja
			msg := tgbotapi.NewMessage(callback.Message.Chat.ID, text)
			msg.ReplyMarkup = &keyboard
			msg.ParseMode = "HTML"
			h.Bot.Send(msg)
			return // Hentikan proses jika belum subscribe
		}
	}


	switch action {
	case "back_to_providers": // <-- AKSI BARU
		h.showProviderSelection(callback)
	case "provider_select": // <-- AKSI BARU
		h.handleProviderSelection(callback, data)
	case "model_page":
		// Perlu sedikit penyesuaian untuk pagination per provider
		parts := strings.Split(data, ";") // Format baru: "provider_id;page"
		providerID := parts[0]
		page, _ := strconv.Atoi(parts[1])
		h.navigateModels(callback, providerID, page)
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
	case "settings_aspect_ratio":
		lang := h.getUserLang(callback.From.ID)
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, h.Localizer.Get(lang, "select_aspect_ratio"))
		// PERBAIKAN: Tambahkan '&' untuk mendapatkan pointer
		keyboard := h.createAspectRatioKeyboard(lang)
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)

	case "settings_num_images":
		lang := h.getUserLang(callback.From.ID)
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, h.Localizer.Get(lang, "select_num_images"))
		// PERBAIKAN: Tambahkan '&' untuk mendapatkan pointer
		keyboard := h.createNumOutputsKeyboard(lang)
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)

	case "set_ar":
		// PERBAIKAN: Ambil semua bagian setelah "set_ar:"
		// Ini akan memastikan "9:16" terbaca utuh
		aspectRatioValue := strings.TrimPrefix(callback.Data, "set_ar:")
		
		user, _ := h.getOrCreateUser(callback.From)
		user.AspectRatio = aspectRatioValue // Gunakan nilai yang sudah benar
		h.DB.UpdateUser(user)
		h.updateSettingsMessage(callback.Message.Chat.ID, callback.Message.MessageID, user)

	case "set_num":
		user, _ := h.getOrCreateUser(callback.From)
		num, _ := strconv.Atoi(data)
		user.NumOutputs = num
		h.DB.UpdateUser(user)
		h.updateSettingsMessage(callback.Message.Chat.ID, callback.Message.MessageID, user)
	case "settings_back_to_main":
        user, _ := h.getOrCreateUser(callback.From)
        h.updateSettingsMessage(callback.Message.Chat.ID, callback.Message.MessageID, user)
	
	case "main_menu_generate":
		// Panggil logika /img, tapi sebagai callback
		h.handleImageCommand(dummyMessage)
	
	case "main_menu_settings":
		h.handleSettings(dummyMessage)
	
	case "main_menu_faq":
		h.handleFaq(dummyMessage)

	case "main_menu_language":
		h.handleLang(dummyMessage)
		

	case "main_menu_help":
		h.handleHelp(dummyMessage)


	case "main_menu_referral":
		h.handleReferral(dummyMessage)
	
		case "main_menu_topup": // <-- CASE BARU
		h.PaymentHandler.ShowTopUpOptions(callback.Message.Chat.ID)

	case "download_raw": // <-- BARU
		h.handleRawDownload(callback) // <-- BARU

	case "main_menu_back":
		// Kembali ke menu utama dari halaman lain
		h.handleStart(dummyMessage)
	
	case "topup_stars": // <-- BARU
		h.PaymentHandler.ShowStarsPackages(callback.Message.Chat.ID)
	case "topup_manual": // <-- BARU
		h.PaymentHandler.ShowManualPaymentInfo(callback.Message.Chat.ID)

	case "faq_show":
        h.handleFaqShow(callback, data)
    case "faq_back":
        h.handleFaqBack(callback)

	}
}

func (h *Handler) showProviderSelection(callback *tgbotapi.CallbackQuery) {
	user, _ := h.getOrCreateUser(callback.From)
	lang := user.LanguageCode

	args := map[string]string{
		"aspect_ratio": user.AspectRatio,
		"num_images":   strconv.Itoa(user.NumOutputs),
	}
	text := h.Localizer.Getf(lang, "choose_model", args)

	keyboard := h.createProviderSelectionKeyboard(h.Providers, lang)
	msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

func (h *Handler) handleProviderSelection(callback *tgbotapi.CallbackQuery, providerID string) {
	user, _ := h.getOrCreateUser(callback.From)
	lang := user.LanguageCode

	var selectedProvider *config.Provider
	for _, p := range h.Providers {
		if p.ID == providerID {
			selectedProvider = &p
			break
		}
	}
	if selectedProvider == nil {
		return
	}

	// Filter model berdasarkan provider yang dipilih
	var providerModels []config.Model
	for _, m := range h.Models {
		if strings.HasPrefix(m.ReplicateID, providerID+"/") {
			providerModels = append(providerModels, m)
		}
	}

	// Buat teks dengan deskripsi provider
	text := fmt.Sprintf("<b>%s</b>\n\n%s\n\nSilakan pilih model:", selectedProvider.Name, selectedProvider.Description)

	// Buat keyboard model untuk provider ini
	keyboard := h.createModelSelectionKeyboard(providerModels, lang, providerID, 0)

	// Edit pesan sebelumnya untuk menampilkan model
	msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

func (h *Handler) getUserLang(userID int64) string {
	user, err := h.DB.GetUserByTelegramID(userID)
	if err != nil || user == nil {
		return "en" // default
	}
	return user.LanguageCode
}

func (h *Handler) handleCancel(message *tgbotapi.Message) {
	h.userStatesMutex.Lock()         // <-- DITAMBAHKAN: Mengunci sebelum mengakses
	defer h.userStatesMutex.Unlock()
	if _, ok := h.userStates[message.From.ID]; ok {
		delete(h.userStates, message.From.ID)
		
		user, _ := h.getOrCreateUser(message.From)
		lang := user.LanguageCode
		msg := h.newReplyMessage(message, h.Localizer.Get(lang, "flow_cancelled"))		
		h.Bot.Send(msg)
	}
}

// FUNGSI BARU UNTUK TOMBOL BATAL
func (h *Handler) handleCancelCallback(callback *tgbotapi.CallbackQuery) {
	h.userStatesMutex.Lock()   
	delete(h.userStates, callback.From.ID)
	defer h.userStatesMutex.Unlock() 

	deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
    h.Bot.Request(deleteMsg)

	dummyMessage := &tgbotapi.Message{
        From: callback.From,
        Chat: callback.Message.Chat,
    }
    h.handleImageCommand(dummyMessage)
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
	h.userStatesMutex.Lock()
	modelID, ok := h.userStates[callback.From.ID]
	h.userStatesMutex.Unlock()
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
	messageForReply := callback.Message
	
	// Pemanggilan yang sudah diperbaiki: tanpa imageURL
	h.triggerImageGeneration(user, messageForReply, modelID, selectedTemplate.Prompt)

	deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
	h.Bot.Send(deleteMsg)
}


func (h *Handler) getFileURL(fileID string) (string, error) {
	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	file, err := h.Bot.GetFile(fileConfig)
	if err != nil {
		return "", err
	}
	return file.Link(h.Bot.Token), nil
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

	// --- MULAI PERUBAHAN ---
	// Cek total kredit dari kedua dompet
	totalAvailableCredits := user.PaidCredits + user.FreeCredits
	if totalAvailableCredits < selectedModel.Cost {
		args := map[string]string{
			"required": strconv.Itoa(selectedModel.Cost),
			"balance":  strconv.Itoa(totalAvailableCredits),
		}
		// --- SELESAI PERUBAHAN ---
		text := h.Localizer.Getf(lang, "insufficient_credits", args)
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_topup_contextual"), "main_menu_topup"),
			),
		)
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, text)
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)
		return
	}

	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = modelID
	h.userStatesMutex.Unlock()

	var warningText string
    var mainText string

    // 1. Siapkan peringatan untuk model yang hanya bisa 1 gambar.
    if user.NumOutputs > 1 && !selectedModel.ConfigurableNumOutputs {
        warningText = h.Localizer.Get(lang, "single_output_warning")
    }

	args := map[string]string{
		"model_name":        selectedModel.Name,
		"model_description": selectedModel.Description,
	}

    // 2. Siapkan teks utama.
    if selectedModel.AcceptsImageInput {
        mainText =  h.Localizer.Getf(lang, "enter_prompt_with_image_option", args)
    } else {
        mainText = h.Localizer.Getf(lang, "enter_prompt_without_image_option", args)
    }

    // 3. Gabungkan peringatan dan teks utama.
    fullText := warningText + mainText

    // 4. Tambahkan peringatan biaya jika diperlukan.
    if user.NumOutputs > 1 && selectedModel.ConfigurableNumOutputs {
        totalCost := selectedModel.Cost * user.NumOutputs
        warningArgs := map[string]string{
            "num_images": strconv.Itoa(user.NumOutputs),
            "total_cost": strconv.Itoa(totalCost),
        }
        costWarning := h.Localizer.Getf(lang, "multiple_images_warning", warningArgs)
        fullText += costWarning // Tambahkan ke `fullText` yang sudah ada.
    }

    msg := tgbotapi.NewMessage(callback.Message.Chat.ID, fullText)
    msg.ParseMode = "HTML"

	if selectedModel.ShowTemplates {
		templateButton := tgbotapi.NewInlineKeyboardButtonData("✨ Choose from Template", "show_templates:0")
		cancelButton := tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "cancel_button"), "cancel_flow")
		keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(templateButton, cancelButton))
		msg.ReplyMarkup = &keyboard
	} else {
		// Jika tidak, cukup beri opsi batal
		cancelButton := tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "cancel_button"), "cancel_flow")
		keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(cancelButton))
		msg.ReplyMarkup = &keyboard
	}
	// --- SELESAI PERUBAHAN ---


	h.Bot.Send(msg)

	deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
	h.Bot.Send(deleteMsg)
}

func (h *Handler) handleMessage(message *tgbotapi.Message) {
	h.userStatesMutex.Lock()
	modelID, ok := h.userStates[message.From.ID]
	h.userStatesMutex.Unlock()
	if !ok {
		return
	}

	user, _ := h.getOrCreateUser(message.From)
	var prompt, imageURL string

	// PERUBAHAN: Cek apakah pesan berisi foto
	if message.Photo != nil && len(message.Photo) > 0 {
		// Ambil foto dengan resolusi tertinggi
		bestPhoto := message.Photo[len(message.Photo)-1]
		url, err := h.getFileURL(bestPhoto.FileID)
		if err != nil {
			log.Printf("ERROR: Failed to get file URL for photo: %v", err)
			return
		}
		imageURL = url
		prompt = message.Caption // Gunakan caption sebagai prompt
	} else {
		prompt = message.Text // Gunakan teks biasa sebagai prompt
	}

	if prompt == "" {
		// Jangan proses jika tidak ada prompt
		return
	}

	h.triggerImageGeneration(user, message, modelID, prompt, imageURL)
}


func (h *Handler) triggerImageGeneration(user *database.User, originalMessage *tgbotapi.Message, modelID, prompt string, imageURL ...string) {
	h.userStatesMutex.Lock()
	delete(h.userStates, user.TelegramID)
	h.userStatesMutex.Unlock()

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

	var aspectRatio string
	var numOutputs int

	if selectedModel.ConfigurableAspectRatio {
		aspectRatio = user.AspectRatio
	}
	if selectedModel.ConfigurableNumOutputs {
		numOutputs = user.NumOutputs
	} else {
		numOutputs = 1
	}

	totalCost := selectedModel.Cost * numOutputs
	// --- MULAI PERUBAHAN LOGIKA KREDIT ---
	totalAvailableCredits := user.PaidCredits + user.FreeCredits
	if totalAvailableCredits < totalCost {
		insufficientArgs := map[string]string{
			"required": strconv.Itoa(totalCost),
			"balance":  strconv.Itoa(totalAvailableCredits),
		}
		msg := h.newReplyMessage(originalMessage, h.Localizer.Getf(lang, "insufficient_credits", insufficientArgs))
		h.Bot.Send(msg)
		return
	}
	// --- SELESAI PERUBAHAN LOGIKA KREDIT ---

	waitMsg := h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generating"))
	sentMsg, _ := h.Bot.Send(waitMsg)
	defer h.Bot.Send(tgbotapi.NewDeleteMessage(originalMessage.Chat.ID, sentMsg.MessageID))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var finalImageURL string
	if len(imageURL) > 0 {
		finalImageURL = imageURL[0]
	}
	imageUrls, err := h.Replicate.CreatePrediction(ctx, selectedModel.ReplicateID, prompt, finalImageURL, aspectRatio, numOutputs)

	if err != nil || len(imageUrls) == 0 {
        // --- BARIS YANG DIPERBARUI ---
		failMsg := h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generation_failed"))
        // --- SELESAI ---
		h.Bot.Send(failMsg)
		return
	}

	h.lastGeneratedURLsMutex.Lock()
	h.lastGeneratedURLs[user.TelegramID] = imageUrls
	h.lastGeneratedURLsMutex.Unlock()

	costLeft := totalCost
	if user.FreeCredits > 0 {
		if user.FreeCredits >= costLeft {
			user.FreeCredits -= costLeft
			costLeft = 0
		} else {
			costLeft -= user.FreeCredits
			user.FreeCredits = 0
		}
	}
	if costLeft > 0 {
		user.PaidCredits -= costLeft
	}

	// 2. Tambah jumlah gambar yang sudah dibuat
	user.GeneratedImageCount++

	// 3. Simpan perubahan data pengguna saat ini ke database SEGERA
	h.DB.UpdateUser(user)

	// 4. SEKARANG, baru kita cek apakah syarat referral terpenuhi
	if user.GeneratedImageCount == 2 && user.ReferrerID != 0 {
		// Dapatkan data terbaru dari si pengundang
		referrer, err := h.DB.GetUserByTelegramID(user.ReferrerID)
		if err == nil && referrer != nil {
			referrer.PaidCredits += 5
			errUpdate := h.DB.UpdateUser(referrer) // Simpan bonus ke pengundang
			if errUpdate == nil {
				log.Printf("INFO: Awarded 5 referral credits to user %d", referrer.TelegramID)

				// Kirim notifikasi ke pengundang
				referrerLang := referrer.LanguageCode
				notificationText := h.Localizer.Get(referrerLang, "referral_bonus_notification")
				msg := tgbotapi.NewMessage(referrer.TelegramID, notificationText)
				msg.ParseMode = "Markdown"
				h.Bot.Send(msg)
			}
		}
	}
	// --- SELESAI LOGIKA BARU ---	

	safePrompt := html.EscapeString(prompt)
	if len(safePrompt) > 900 { // Batas aman untuk prompt
		safePrompt = safePrompt[:900] + "..."
	}
	caption := fmt.Sprintf("<b>Prompt:</b> <pre>%s</pre>\n<b>Model:</b> <code>%s</code>\n<b>Cost:</b> %d 💵", safePrompt, selectedModel.Name, totalCost)
	// --- Selesai ---
	

	if len(imageUrls) == 1 {
		msg := h.newReplyPhoto(originalMessage, tgbotapi.FileURL(imageUrls[0]))
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
		msg := h.newReplyMediaGroup(originalMessage, media)
		h.Bot.Send(msg)
	}
	rawPromptText := h.Localizer.Get(lang, "raw_file_prompt")
	rawButtonText := h.Localizer.Get(lang, "raw_download_button")
	
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(rawButtonText, "download_raw"),
		),
	)
	
	rawMsg := tgbotapi.NewMessage(originalMessage.Chat.ID, rawPromptText)
	rawMsg.ReplyMarkup = &keyboard
	h.Bot.Send(rawMsg)
}


func (h *Handler) navigateModels(callback *tgbotapi.CallbackQuery, providerID string, page int) {
	user, _ := h.getOrCreateUser(callback.From)

	var providerModels []config.Model
	for _, m := range h.Models {
		if strings.HasPrefix(m.ReplicateID, providerID+"/") {
			providerModels = append(providerModels, m)
		}
	}

	keyboard := h.createModelSelectionKeyboard(providerModels, user.LanguageCode, providerID, page)
	msg := tgbotapi.NewEditMessageReplyMarkup(callback.Message.Chat.ID, callback.Message.MessageID, keyboard)
	h.Bot.Send(msg)
}

// Sisa fungsi-fungsi dari langkah sebelumnya (TIDAK BERUBAH)
func (h *Handler) handleStart(message *tgbotapi.Message) {
	// Cek dulu apakah pengguna sudah ada di database
	user, err := h.DB.GetUserByTelegramID(message.From.ID)
	if err != nil {
		log.Printf("ERROR: Failed to get user on start: %v", err)
		return
	}

	// --- LOGIKA BARU UNTUK PENGGUNA BARU ---
	if user == nil {
		// Jika pengguna tidak ada, ini adalah pengguna baru.
		var referrerID int64
		
		// Cek dan proses ID referral SEBELUM membuat pengguna
		if strings.HasPrefix(message.CommandArguments(), "ref_") {
			parsedID, err := strconv.ParseInt(strings.TrimPrefix(message.CommandArguments(), "ref_"), 10, 64)
			// Pastikan pengguna tidak mereferensikan dirinya sendiri
			if err == nil && parsedID != message.From.ID {
				referrerID = parsedID
			}
		}

		// Siapkan data pengguna baru, termasuk ID referral jika ada
		newUser := database.User{
			TelegramID:   message.From.ID,
			Username:     message.From.UserName,
			PaidCredits:          0, // Pengguna baru mulai dengan 0 kredit berbayar
			FreeCredits:          5,
			LastFreeCreditsReset:    time.Now(),
			LanguageCode: "en",
			AspectRatio:  "1:1",
			NumOutputs:   1,
			ReferrerID:   referrerID, // ID referral langsung dimasukkan di sini
		}

		// Buat pengguna baru di database
		user, err = h.DB.CreateUser(&newUser)
		if err != nil {
			log.Printf("ERROR: Failed to create user on start: %v", err)
			return
		}
		
		if referrerID != 0 {
			log.Printf("INFO: User %d created with referral from %d", user.TelegramID, referrerID)
		}
	}

	// --- Sisa fungsi (mengirim pesan sambutan) tidak berubah ---
	lang := user.LanguageCode
	args := map[string]string{
		"name":         message.From.FirstName,
		"aspect_ratio": user.AspectRatio,
		"num_images":   strconv.Itoa(user.NumOutputs),
	}
	caption := h.Localizer.Getf(lang, "welcome", args)
	photoMsg := h.newReplyPhoto(message, tgbotapi.FileURL(h.Config.WelcomeImageURL))
	photoMsg.Caption = caption
	photoMsg.ParseMode = "HTML"
	keyboard := h.createMainMenuKeyboard(lang)
	photoMsg.ReplyMarkup = &keyboard
	h.Bot.Send(photoMsg)
}

func (h *Handler) handleHelp(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode
	text := h.Localizer.Get(lang, "help")
	msg := h.newReplyMessage(message, text)
	keyboard := h.createAddToGroupKeyboard(lang, h.Bot.Self.UserName) 
	msg.ReplyMarkup = &keyboard
	msg.ParseMode = "html"
	h.Bot.Send(msg)
}

func (h *Handler) handleProfile(message *tgbotapi.Message) {
	// Panggil getOrCreateUser yang sudah memiliki logika reset
	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		// Error sudah di-log di dalam getOrCreateUser, cukup hentikan proses
		return
	}
	lang := user.LanguageCode

	// Logika reset sudah dipindahkan, sekarang bagian ini hanya untuk tampilan
	var resetTimeStr string
	if user.IsPremium {
		resetTimeStr = "N/A (Premium)"
	} else {
		nowUTC := time.Now().UTC()
		nextResetUTC := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
		duration := time.Until(nextResetUTC)
		resetTimeStr = h.formatDuration(duration, lang)
	}

	totalCredits := user.PaidCredits + user.FreeCredits
	args := map[string]string{
		"id":            strconv.FormatInt(user.TelegramID, 10),
		"total_credits": strconv.Itoa(totalCredits),
		"paid_credits":  "`" + strconv.Itoa(user.PaidCredits) + "`", // Tambahkan ` agar font berbeda
		"free_credits":  "`" + strconv.Itoa(user.FreeCredits) + "`", // Tambahkan ` agar font berbeda
		"reset_time":    resetTimeStr,
	}

	text := h.Localizer.Getf(lang, "profile", args)
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "Markdown"
	keyboard := h.createProfileKeyboard(lang)
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

func (h *Handler) handleReferral(message *tgbotapi.Message) {
	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode
	referralLink := fmt.Sprintf("https://t.me/%s?start=ref_%d", h.Bot.Self.UserName, user.TelegramID)
	text := fmt.Sprintf("%s\n\n%s\n%s",
		h.Localizer.Get(lang, "referral_message"),
		h.Localizer.Get(lang, "referral_link_text"),
		referralLink,
	)
	shareText := url.QueryEscape(fmt.Sprintf("Come join this cool AI image bot! Use my link to get a bonus:😉\n %s", referralLink))
	shareURL := fmt.Sprintf("https://t.me/share/url?url=%s", shareText)

	// Buat keyboard dengan tombol "Share"
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("🚀 Share with Friends", shareURL),
		),
	)
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = &keyboard
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
	msg := h.newReplyMessage(message, text)
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

	// PERBAIKAN: Menggunakan `callback.Message.Chat.ID` yang sudah pasti ada
	msg := tgbotapi.NewMessage(callback.Message.Chat.ID, confirmationText)
	h.Bot.Send(msg)

	// Hapus keyboard dari pesan lama
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
			TelegramID:           tgUser.ID,
			Username:             tgUser.UserName,
			PaidCredits:          0,
			FreeCredits:          5,
			LastFreeCreditsReset: time.Now(),
			LanguageCode:         "en",
			AspectRatio:          "1:1",
			NumOutputs:           1,
		}
		user, err = h.DB.CreateUser(&newUser)
		if err != nil {
			log.Printf("ERROR: Failed to create user %d in database: %v", tgUser.ID, err)
			return nil, err
		}
	} else {
		// --- MULAI LOGIKA BARU UNTUK PENGGUNA LAMA ---
		now := time.Now()
		lastReset := user.LastFreeCreditsReset
		// Cek jika hari kalender sudah berbeda (dalam zona waktu UTC)
		if now.YearDay() != lastReset.YearDay() || now.Year() != lastReset.Year() {
			log.Printf("INFO: Resetting free credits for existing user %d", user.TelegramID)
			user.FreeCredits = 5
			user.LastFreeCreditsReset = now
			// Penting: simpan perubahan ini ke DB segera
			err := h.DB.UpdateUser(user)
			if err != nil {
				// Log error tapi tetap kembalikan user object yang sudah diupdate di memory
				log.Printf("ERROR: Failed to update user credits during reset for user %d: %v", user.TelegramID, err)
			}
		}
		// --- SELESAI LOGIKA BARU ---
	}
	return user, nil
}

func (h *Handler) handleImageCommand(message *tgbotapi.Message) {
	// Panggil getOrCreateUser yang sudah memiliki logika reset kredit
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode

	args := map[string]string{
		"aspect_ratio": user.AspectRatio,
		"num_images":   strconv.Itoa(user.NumOutputs),
	}
	text := h.Localizer.Getf(lang, "choose_model", args) // Gunakan string choose_model yang lebih generik jika ada

	keyboard := h.createProviderSelectionKeyboard(h.Providers, lang) // Panggil keyboard provider
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

// File: internal/bot/handlers.go

// FUNGSI BARU
// File: internal/bot/handlers.go

// File: internal/bot/handlers.go

func (h *Handler) handleRawDownload(callback *tgbotapi.CallbackQuery) {
	userID := callback.From.ID
	lang := h.getUserLang(userID)

	h.lastGeneratedURLsMutex.Lock()
	urls, ok := h.lastGeneratedURLs[userID]
	if ok {
		delete(h.lastGeneratedURLs, userID)
	}
	h.lastGeneratedURLsMutex.Unlock()

	if !ok || len(urls) == 0 {
		notFoundText := h.Localizer.Get(lang, "raw_files_not_found")
		editMsg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, notFoundText)
		h.Bot.Send(editMsg)
		return
	}

	for _, urlString := range urls {
		resp, err := http.Get(urlString)
		if err != nil {
			log.Printf("ERROR: Failed to download file from URL %s: %v", urlString, err)
			continue
		}
		defer resp.Body.Close()

		bytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Printf("ERROR: Failed to read file bytes from URL %s: %v", urlString, err)
			continue
		}

		// --- TRIK UTAMA: UBAH NAMA FILE ---
		// Ambil nama file asli dari URL
		originalFileName := filepath.Base(urlString)
		// Hapus ekstensi lama dan ganti dengan .png
		newFileName := strings.TrimSuffix(originalFileName, filepath.Ext(originalFileName)) + ".png"

		fileBytes := tgbotapi.FileBytes{
			Name:  newFileName, // Gunakan nama file baru
			Bytes: bytes,
		}

		doc := h.newReplyDocument(callback.Message, fileBytes)
		h.Bot.Send(doc)
		// --- SELESAI TRIK UTAMA ---
	}

	sentText := h.Localizer.Get(lang, "raw_files_sent")
	editMsg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, sentText)
	h.Bot.Send(editMsg)
}

func (h *Handler) handleFaq(message *tgbotapi.Message) {
    user, _ := h.getOrCreateUser(message.From)
    lang := user.LanguageCode
    text := h.Localizer.Get(lang, "faq_title")

    msg := h.newReplyMessage(message, text)
    msg.ParseMode = "Markdown"
    keyboard := h.createFaqKeyboard(lang)
    msg.ReplyMarkup = &keyboard
    h.Bot.Send(msg)
}

func (h *Handler) handleFaqShow(callback *tgbotapi.CallbackQuery, questionID string) {
    user, _ := h.getOrCreateUser(callback.From)
    lang := user.LanguageCode

    // 1. Ambil teks pertanyaan dari tombol yang diklik
    questionKey := fmt.Sprintf("faq_%s_button", questionID)
    questionText := h.Localizer.Get(lang, questionKey)

    // 2. Ambil teks jawabannya
    answerKey := fmt.Sprintf("faq_%s_answer", questionID)
    answerText := h.Localizer.Get(lang, answerKey)

    // 3. Gabungkan pertanyaan dan jawaban jadi satu pesan
    // Format:
    // *Pertanyaan*
    //
    // Jawaban
    combinedText := fmt.Sprintf("*%s*\n\n%s", questionText, answerText)

    // 4. Kirim pesan yang sudah digabung
    msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, combinedText)
    msg.ParseMode = "Markdown"
    keyboard := h.createFaqAnswerKeyboard(lang)
    msg.ReplyMarkup = &keyboard
    h.Bot.Send(msg)
}

func (h *Handler) handleFaqBack(callback *tgbotapi.CallbackQuery) {
    user, _ := h.getOrCreateUser(callback.From)
    lang := user.LanguageCode
    text := h.Localizer.Get(lang, "faq_title")

    msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
    msg.ParseMode = "Markdown"
    keyboard := h.createFaqKeyboard(lang)
    msg.ReplyMarkup = &keyboard
    h.Bot.Send(msg)
}