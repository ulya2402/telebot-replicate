package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/ioutil" // <-- TAMBAHKAN
	"log"
	"net/http" // <-- TAMBAHKAN
	"net/url"
	"path/filepath" // <-- TAMBAHKAN
	"strconv"
	"strings"
	"sync"
	"telegram-ai-bot/internal/config"
	"telegram-ai-bot/internal/database"
	"telegram-ai-bot/internal/localization"
	"telegram-ai-bot/internal/payments"
	"telegram-ai-bot/internal/services"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type PendingGeneration struct {
	ModelID   string
	Prompt    string
	ImageURL  string
	ImageURLs []string // <-- BARU
	StyleID   string   // <-- BARU
	MessageID int      // <-- BARU
}

type Handler struct {
	Bot                    *tgbotapi.BotAPI
	DB                     *database.Client
	Localizer              *localization.Localizer
	Providers              []config.Provider
	Models                 []config.Model
	PromptTemplates        []config.PromptTemplate
	Styles                 []config.StyleTemplate
	Replicate              *services.ReplicateClient
	userStates             map[int64]string
	userStatesMutex        sync.Mutex
	Config                 *config.Config
	lastGeneratedURLs      map[int64][]string // <-- BARU: Untuk menyimpan URL RAW
	lastGeneratedURLsMutex sync.Mutex
	PaymentHandler         *payments.PaymentHandler
	GroupHandler           *GroupHandler
	pendingGenerations     map[int64]*PendingGeneration
}

func NewHandler(api *tgbotapi.BotAPI, db *database.Client, localizer *localization.Localizer, providers []config.Provider, models []config.Model, templates []config.PromptTemplate, styles []config.StyleTemplate, replicate *services.ReplicateClient, cfg *config.Config, paymentHandler *payments.PaymentHandler) *Handler {
	h := &Handler{
		Bot:                api,
		DB:                 db,
		Localizer:          localizer,
		Providers:          providers,
		Models:             models,
		PromptTemplates:    templates,
		Styles:             styles,
		Replicate:          replicate,
		userStates:         make(map[int64]string),
		Config:             cfg,
		lastGeneratedURLs:  make(map[int64][]string),
		PaymentHandler:     paymentHandler,
		pendingGenerations: make(map[int64]*PendingGeneration),
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
	log.Println("DEBUG: HandleUpdate function started")
	switch {
	case update.PreCheckoutQuery != nil:
		log.Println("DEBUG: Routing update to HandlePreCheckoutQuery")
		h.PaymentHandler.HandlePreCheckoutQuery(update.PreCheckoutQuery)
	case update.Message != nil && update.Message.SuccessfulPayment != nil:
		log.Println("DEBUG: Routing update to HandleSuccessfulPayment")
		h.PaymentHandler.HandleSuccessfulPayment(update.Message)
	case update.Message != nil:
		log.Println("DEBUG: Routing update to message handlers (command or regular message)")
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
		log.Println("DEBUG: Routing update to handleCallbackQuery")
		h.handleCallbackQuery(update.CallbackQuery)
	case update.MyChatMember != nil:
		log.Println("DEBUG: Routing update to handleMyChatMemberUpdate")
		h.handleMyChatMemberUpdate(update.MyChatMember)
	default:
		log.Println("DEBUG: Update received but not handled by any case")
	}
}

func (h *Handler) handleCommand(message *tgbotapi.Message) {
	log.Printf("DIAGNOSTIC: handleCommand triggered. Raw Text: [%s]", message.Text)
	command := message.Command()
	log.Printf("DIAGNOSTIC: Command parsed by library: [%s]", command)
	isAdminCommand := command == "stats" || command == "addcredits" || command == "broadcast" || command == "broadcastgroup"
	if isAdminCommand && !h.isAdmin(message.From.ID) {
		msg := h.newReplyMessage(message, h.Localizer.Get("en", "permission_denied"))
		h.Bot.Send(msg)
		return
	}

	if command == "referral" {
		subscribed, _ := h.isUserSubscribed(message.From.ID)
		if !subscribed {
			user, err := h.getOrCreateUser(message.From)
			if err != nil {
				return
			}
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
	case "banana":
		h.handleBananaCommand(message)
	case "group":
		h.handleGroupCommand(message)
	case "help":
		h.handleHelp(message)
	case "faq":
		h.handleFaq(message)
	case "img", "gen":
		h.handleImageCommand(message)
	case "vids":
		h.handleVideoCommand(message)
	case "exchange":
		h.handleExchangeCommand(message)
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
	case "broadcastgroup":
		h.handleBroadcastGroup(message)
	case "broadcast":
		h.handleBroadcast(message)
	///case "settings":
	///h.handleSettings(message)
	case "topup":
		h.PaymentHandler.ShowTopUpOptions(message.Chat.ID)
	case "removebg":
		h.handleRemoveBg(message)
	case "upscaler":
		h.handleUpscaler(message)
	case "prompt":
		h.handlePromptCommand(message)
	default:
		log.Printf("DIAGNOSTIC: Command [%s] did not match any case. Sending 'Unknown command'.", command)

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

// AWAL PERUBAHAN
func (h *Handler) handleBananaCommand(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode

	// Set state pengguna untuk menandakan kita sedang menunggu gambar
	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "awaiting_banana_images"
	h.userStatesMutex.Unlock()

	// Siapkan tempat untuk menyimpan URL gambar
	h.pendingGenerations[user.TelegramID] = &PendingGeneration{
		ModelID:   "nano-banana", // Langsung set model ID
		ImageURLs: []string{},
	}

	// Kirim pesan instruksi dengan keyboard reply
	text := "🍌 Mode Input Gambar Nano Banana 🍌\n\nSilakan kirim foto Anda satu per satu (maksimal 4). Tekan 'Done' jika sudah selesai."
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ReplyMarkup = h.createMultiImageReplyKeyboard(lang)

	// Kirim pesan dan periksa jika ada error
	sentMsg, err := h.Bot.Send(msg)
	if err != nil {
		log.Printf("ERROR: Failed to send banana command reply: %v", err)
		return
	}

	// Simpan ID pesan agar bisa di-update
	h.pendingGenerations[user.TelegramID].MessageID = sentMsg.MessageID
}

// AKHIR PERUBAHAN

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
	// --- PERUBAHAN DI SINI (1/4): Variabel untuk menyimpan ID foto ---
	var broadcastText, photoFileID string

	broadcastText = message.CommandArguments()

	// --- PERUBAHAN DI SINI (2/4): Logika untuk mendeteksi foto ---
	if message.Photo != nil && len(message.Photo) > 0 {
		photoFileID = message.Photo[len(message.Photo)-1].FileID
	} else if message.ReplyToMessage != nil && message.ReplyToMessage.Photo != nil && len(message.ReplyToMessage.Photo) > 0 {
		photoFileID = message.ReplyToMessage.Photo[len(message.ReplyToMessage.Photo)-1].FileID
	}

	// --- PERUBAHAN DI SINI (3/4): Validasi pesan atau foto harus ada ---
	if broadcastText == "" && photoFileID == "" {
		// Menggunakan teks dari `broadcast_usage` yang sudah ada
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

	go func(adminChatID int64) {
		sentCount := 0
		for _, user := range allUsers {
			var err error
			// --- PERUBAHAN DI SINI (4/4): Logika pengiriman pesan dengan gambar ---
			if photoFileID != "" {
				photoMsg := tgbotapi.NewPhoto(user.TelegramID, tgbotapi.FileID(photoFileID))
				photoMsg.Caption = broadcastText
				photoMsg.ParseMode = "HTML"
				_, err = h.Bot.Send(photoMsg)
			} else {
				textMsg := tgbotapi.NewMessage(user.TelegramID, broadcastText)
				textMsg.ParseMode = "HTML"
				_, err = h.Bot.Send(textMsg)
			}

			if err == nil {
				sentCount++
			}
			time.Sleep(100 * time.Millisecond)
		}

		finishArgs := map[string]string{
			"sent_count":  strconv.Itoa(sentCount),
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
	log.Printf("DEBUG: Callback query received with data: [%s]", callback.Data)

	parts := strings.Split(callback.Data, ":")
	action := parts[0]
	data := ""
	if len(parts) > 1 {
		// Gabungkan sisa parts jika ada, untuk kasus seperti set_ar:9:16
		data = strings.Join(parts[1:], ":")
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
			user, err := h.getOrCreateUser(callback.From)
			if err != nil {
				return
			}
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

	// --- PEMBARUAN 3: Identifikasi User & State untuk Dashboard ---
	user, _ := h.getOrCreateUser(callback.From)
	h.userStatesMutex.Lock()
	state, hasState := h.userStates[user.TelegramID]
	h.userStatesMutex.Unlock()
	// --- AKHIR PEMBARUAN 3 (Bagian Setup) ---

	switch action {
	// --- PEMBARUAN 3: KASUS BARU UNTUK DASHBOARD ---
	
	// 1. Menu Navigasi Dashboard (Ganti Keyboard)
case "dash_ar_menu":
	keyboard := h.createDashboardAspectRatioKeyboard(user.LanguageCode)
	h.Bot.Send(tgbotapi.NewEditMessageReplyMarkup(callback.Message.Chat.ID, callback.Message.MessageID, keyboard))
	return

case "dash_num_menu":
	keyboard := h.createDashboardNumOutputsKeyboard(user.LanguageCode)
	h.Bot.Send(tgbotapi.NewEditMessageReplyMarkup(callback.Message.Chat.ID, callback.Message.MessageID, keyboard))
	return

case "dash_back_main":
	// KONDISI 1: Kembali dari Sub-menu (AR/Quantity) -> State masih awaiting_prompt_and_settings
	if hasState && strings.HasPrefix(state, "awaiting_prompt_and_settings:") {
		modelID := strings.TrimPrefix(state, "awaiting_prompt_and_settings:")
		var selectedModel *config.Model
		for _, m := range h.Models {
			if m.ID == modelID {
				selectedModel = &m
				break
			}
		}
		if selectedModel != nil {
			h.updateGenerationDashboard(callback.Message.Chat.ID, callback.Message.MessageID, user, selectedModel)
		}
		return
	}

	// KONDISI 2 (FIX BUG): Kembali dari Input Manual (Seed, dll) -> State edit_setting
	if hasState && strings.HasPrefix(state, "edit_setting:") {
		// Format state: edit_setting:modelID:paramName
		parts := strings.Split(strings.TrimPrefix(state, "edit_setting:"), ":")
		if len(parts) > 0 {
			modelID := parts[0]

			// Kembalikan state user ke mode dashboard (tunggu prompt)
			h.userStatesMutex.Lock()
			h.userStates[user.TelegramID] = "awaiting_prompt_and_settings:" + modelID
			h.userStatesMutex.Unlock()

			// Refresh tampilan dashboard
			for _, m := range h.Models {
				if m.ID == modelID {
					h.updateGenerationDashboard(callback.Message.Chat.ID, callback.Message.MessageID, user, &m)
					break
				}
			}
		}
		return
	}
	return

case "dash_set_ar":
	user.AspectRatio = data
	h.DB.UpdateUser(user)
	if hasState && strings.HasPrefix(state, "awaiting_prompt_and_settings:") {
		modelID := strings.TrimPrefix(state, "awaiting_prompt_and_settings:")
		for _, m := range h.Models {
			if m.ID == modelID {
				h.updateGenerationDashboard(callback.Message.Chat.ID, callback.Message.MessageID, user, &m)
				break
			}
		}
	}
	return

case "dash_set_num":
	num, _ := strconv.Atoi(data)
	user.NumOutputs = num
	h.DB.UpdateUser(user)
	if hasState && strings.HasPrefix(state, "awaiting_prompt_and_settings:") {
		modelID := strings.TrimPrefix(state, "awaiting_prompt_and_settings:")
		for _, m := range h.Models {
			if m.ID == modelID {
				h.updateGenerationDashboard(callback.Message.Chat.ID, callback.Message.MessageID, user, &m)
				break
			}
		}
	}
	return

case "dash_img_add":
	if hasState && strings.HasPrefix(state, "awaiting_prompt_and_settings:") {
		modelID := strings.TrimPrefix(state, "awaiting_prompt_and_settings:")
		h.userStatesMutex.Lock()
		h.userStates[user.TelegramID] = "awaiting_dashboard_image:" + modelID
		h.userStatesMutex.Unlock()

		keyboard := h.createImageUploadKeyboard(user.LanguageCode)
		text := "📤 <b>Upload Mode</b>\n\nPlease send your images one by one.\nClick <b>Done</b> when finished."
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)
	}
	return

case "dash_img_done", "dash_img_back":
	if hasState && strings.HasPrefix(state, "awaiting_dashboard_image:") {
		modelID := strings.TrimPrefix(state, "awaiting_dashboard_image:")
		h.userStatesMutex.Lock()
		h.userStates[user.TelegramID] = "awaiting_prompt_and_settings:" + modelID
		h.userStatesMutex.Unlock()

		for _, m := range h.Models {
			if m.ID == modelID {
				h.updateGenerationDashboard(callback.Message.Chat.ID, callback.Message.MessageID, user, &m)
				break
			}
		}
	}
	return

case "dash_img_clear":
	if pending, ok := h.pendingGenerations[user.TelegramID]; ok {
		pending.ImageURLs = []string{}
	}
	if hasState && strings.HasPrefix(state, "awaiting_prompt_and_settings:") {
		modelID := strings.TrimPrefix(state, "awaiting_prompt_and_settings:")
		for _, m := range h.Models {
			if m.ID == modelID {
				h.updateGenerationDashboard(callback.Message.Chat.ID, callback.Message.MessageID, user, &m)
				break
			}
		}
	}
	return

// --- AKHIR LOGIKA DASHBOARD ---

	case "style_select":
		h.handleStyleSelection(callback, data)
		return

	case "back_to_providers":
		h.userStatesMutex.Lock()
		state, ok := h.userStates[callback.From.ID]
		h.userStatesMutex.Unlock()

		if ok {
			var modelType string
			if strings.Contains(state, "image") {
				modelType = "image"
			} else if strings.Contains(state, "video") {
				modelType = "video"
			}

			if modelType != "" {
				h.showProviderMenu(callback.Message.Chat.ID, callback.From.ID, modelType, callback.Message.MessageID)
				return
			}
		}

		deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
		h.Bot.Request(deleteMsg)
		dummyMessage := &tgbotapi.Message{
			From: callback.From,
			Chat: callback.Message.Chat,
		}
		h.handleStart(dummyMessage)

	case "provider_select":
		h.handleProviderSelection(callback, data)

	case "model_page":
		parts := strings.Split(data, ";")
		if len(parts) < 2 {
			log.Printf("ERROR: Invalid data format for model_page: %s", data)
			return
		}
		providerID := parts[0]
		page, _ := strconv.Atoi(parts[1])
		h.navigateModels(callback, providerID, page)

	case "model_select":
		h.handleModelSelection(callback, data)

	case "adv_setting_open":
		h.handleOpenAdvancedSettings(callback, data)

	case "adv_setting_select":
		if len(parts) > 2 {
			h.handleSelectAdvancedSetting(callback, parts[1], parts[2])
		}

	case "adv_setting_back":
		// --- PEMBARUAN 3: Override Tombol Back di Advanced Settings ---
		// Jika sedang dalam mode dashboard, kembali ke dashboard, bukan ke pemilihan model
		if hasState && strings.HasPrefix(state, "awaiting_prompt_and_settings:") {
			modelID := strings.TrimPrefix(state, "awaiting_prompt_and_settings:")
			for _, m := range h.Models {
				if m.ID == modelID {
					h.updateGenerationDashboard(callback.Message.Chat.ID, callback.Message.MessageID, user, &m)
					break
				}
			}
			return
		}
		// --- Akhir Override ---

		// Logika lama (Fallback) jika tidak dalam mode dashboard
		if !hasState || !strings.HasPrefix(state, "prompt_for:") {
			h.handleModelSelection(callback, data)
			return
		}

		stateParts := strings.Split(state, ":")
		if len(stateParts) < 3 {
			h.handleModelSelection(callback, data)
			return
		}

		modelID := stateParts[1]
		styleID := stateParts[2]
		h.showPromptEntryScreen(callback, modelID, styleID, true)

	case "adv_set_option":
		if len(parts) >= 4 {
			modelID := parts[1]
			paramName := parts[2]
			optionValue := strings.Join(parts[3:], ":")
			h.handleSetOption(callback, modelID, paramName, optionValue)
		}

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

	case "cancel_flow":
		h.handleCancelCallback(callback)

	case "settings_aspect_ratio":
		lang := h.getUserLang(callback.From.ID)
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, h.Localizer.Get(lang, "select_aspect_ratio"))
		keyboard := h.createAspectRatioKeyboard(lang)
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)

	case "settings_num_images":
		lang := h.getUserLang(callback.From.ID)
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, h.Localizer.Get(lang, "select_num_images"))
		keyboard := h.createNumOutputsKeyboard(lang)
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)

	case "set_ar":
		aspectRatioValue := strings.TrimPrefix(callback.Data, "set_ar:")
		user.AspectRatio = aspectRatioValue
		h.DB.UpdateUser(user)
		h.updateSettingsMessage(callback.Message.Chat.ID, callback.Message.MessageID, user)

	case "set_num":
		num, _ := strconv.Atoi(data)
		user.NumOutputs = num
		h.DB.UpdateUser(user)
		h.updateSettingsMessage(callback.Message.Chat.ID, callback.Message.MessageID, user)
	case "settings_back_to_main":
		h.updateSettingsMessage(callback.Message.Chat.ID, callback.Message.MessageID, user)

	case "main_menu_generate":
		h.handleImageCommand(dummyMessage)
	case "main_menu_generate_video":
		h.handleVideoCommand(dummyMessage)
	case "main_menu_exchange":
		h.handleExchangeCommand(dummyMessage)
	case "main_menu_removebg":
		h.handleRemoveBg(dummyMessage)
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
	case "main_menu_topup":
		h.PaymentHandler.ShowTopUpOptions(callback.Message.Chat.ID)
	case "download_raw":
		h.handleRawDownload(callback)
	case "main_menu_back":
		h.handleStart(dummyMessage)

	case "topup_stars":
		h.PaymentHandler.ShowStarsPackages(callback.Message.Chat.ID, callback.Message.MessageID)
	case "topup_manual":
		h.PaymentHandler.ShowManualPaymentOptions(callback.Message.Chat.ID, callback.Message.MessageID)
	case "topup_transfer_bank":
		h.PaymentHandler.ShowManualPaymentInfo(callback.Message.Chat.ID, callback.Message.MessageID)
	case "topup_back_to_main":
		h.PaymentHandler.ShowTopUpOptions(callback.Message.Chat.ID, callback.Message.MessageID)
	case "topup_back_to_manual":
		h.PaymentHandler.ShowManualPaymentOptions(callback.Message.Chat.ID, callback.Message.MessageID)
	//case "topup_bmac":
	//	h.PaymentHandler.ShowBMACPackages(callback.Message.Chat.ID, callback.Message.MessageID)

	case "faq_show":
		h.handleFaqShow(callback, data)
	case "faq_back":
		h.handleFaqBack(callback)

	case "style_confirm":
		h.handleStyleCallback(callback, data)
		return

	case "multi_image_done":
		// Logika legacy untuk Nano Banana (Masih disimpan agar aman)
		user, _ := h.getOrCreateUser(callback.From)
		lang := user.LanguageCode

		h.userStatesMutex.Lock()
		state, ok := h.userStates[user.TelegramID]
		h.userStatesMutex.Unlock()

		if !ok || !strings.HasPrefix(state, "awaiting_multi_image:") {
			return
		}

		parts := strings.Split(state, ":")
		if len(parts) < 3 {
			return
		}
		modelID := parts[1]

		h.userStatesMutex.Lock()
		h.userStates[user.TelegramID] = "prompt_for:" + modelID
		h.userStatesMutex.Unlock()

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

		args := map[string]string{
			"model_name":        selectedModel.Name,
			"model_description": selectedModel.Description,
		}
		text := h.Localizer.Getf(lang, "enter_prompt", args)

		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
		msg.ParseMode = "HTML"
		cancelKeyboard := h.createCancelFlowKeyboard(lang)
		msg.ReplyMarkup = &cancelKeyboard
		h.Bot.Send(msg)
		return

	case "main_menu_upscaler":
		h.handleUpscaler(dummyMessage)
	case "main_menu_prompt":
		// Saat tombol menu utama diklik, tampilkan SUB-MENU Prompt Assistant
		h.handlePromptMenu(dummyMessage) 

	case "prompt_mode":
		// Menangani pilihan user (Text atau Image)
		if len(parts) > 1 {
			h.handlePromptModeSelection(callback, parts[1])
		}
	
	case "prompt_method":
		if len(parts) > 1 {
			h.handlePromptMethodCallback(callback, parts[1])
		}
	case "main_menu_chat":
		// Langkah 1: Tampilkan Menu Pilih Model (Bukan langsung start)
		h.handleChatModelSelectionMenu(dummyMessage)

	case "select_chat_model":
		// Langkah 2: User sudah pilih model -> Mulai Chat
		if len(parts) > 1 {
			modelID := parts[1]
			h.handleChatModeStart(dummyMessage, modelID)
		}
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

func (h *Handler) handleRemoveBg(message *tgbotapi.Message) {
	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode

	var removeBgModel *config.Model
	for _, m := range h.Models {
		if m.ID == "remove-background" {
			removeBgModel = &m
			break
		}
	}

	if removeBgModel == nil {
		log.Println("ERROR: 'remove-background' model not found in models.json")
		return
	}

	totalAvailableCredits := user.PaidCredits + user.FreeCredits
	if totalAvailableCredits < removeBgModel.Cost {
		args := map[string]string{
			"required": strconv.Itoa(removeBgModel.Cost),
			"balance":  strconv.Itoa(totalAvailableCredits),
		}
		text := h.Localizer.Getf(lang, "insufficient_credits", args)
		msg := h.newReplyMessage(message, text)
		h.Bot.Send(msg)
		return
	}

	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "awaiting_image_for_removebg"
	h.userStatesMutex.Unlock()

	args := map[string]string{
		"cost": strconv.Itoa(removeBgModel.Cost),
	}
	text := h.Localizer.Getf(lang, "removebg_prompt", args)

	msg := h.newReplyMessage(message, text)
	keyboard := h.createCancelFlowKeyboard(lang)
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

	h.userStatesMutex.Lock()
	state, ok := h.userStates[user.TelegramID]
	h.userStatesMutex.Unlock()

	var modelType string
	if ok {
		if strings.Contains(state, "image") {
			modelType = "image"
		} else if strings.Contains(state, "video") {
			modelType = "video"
		}
	}

	// Jika state tidak valid atau tidak ditemukan, hentikan proses untuk menghindari bug
	if modelType == "" {
		log.Printf("WARN: Invalid or missing state for user %d in handleProviderSelection", user.TelegramID)
		return
	}

	// Filter model berdasarkan provider yang dipilih
	var providerModels []config.Model
	for _, m := range h.Models {
		if strings.HasPrefix(m.ReplicateID, providerID+"/") && m.Type == modelType {
			providerModels = append(providerModels, m)
		}
	}

	text := fmt.Sprintf("<b>%s</b>\n\n%s\n\nSilakan pilih model:", selectedProvider.Name, selectedProvider.Description)

	keyboard := h.createModelSelectionKeyboard(providerModels, lang, providerID, 0)

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
	h.userStatesMutex.Lock() // <-- DITAMBAHKAN: Mengunci sebelum mengakses
	defer h.userStatesMutex.Unlock()
	if _, ok := h.userStates[message.From.ID]; ok {
		delete(h.userStates, message.From.ID)

		user, _ := h.getOrCreateUser(message.From)
		lang := user.LanguageCode
		msg := h.newReplyMessage(message, h.Localizer.Get(lang, "flow_cancelled"))
		h.Bot.Send(msg)
	}
}

func (h *Handler) handleCancelCallback(callback *tgbotapi.CallbackQuery) {
	h.userStatesMutex.Lock()
	state, ok := h.userStates[callback.From.ID]
	
	// Hapus data pending generation (gambar yang diupload tapi batal dipakai)
	delete(h.pendingGenerations, callback.From.ID)
	h.userStatesMutex.Unlock()

	if ok && strings.HasPrefix(state, "awaiting_prompt_") {
		
		// Bersihkan state sepenuhnya
		h.userStatesMutex.Lock()
		delete(h.userStates, callback.From.ID)
		h.userStatesMutex.Unlock()

		// Hapus pesan menu Prompt Assistant
		deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
		h.Bot.Request(deleteMsg)

		// Kembali ke Main Menu (/start)
		dummyMessage := &tgbotapi.Message{
			From: callback.From,
			Chat: callback.Message.Chat,
		}
		h.handleStart(dummyMessage)
		return
	}

	// 1. Deteksi tipe model dari state sebelumnya (Image atau Video)
	modelType := "image" // Default
	if ok && strings.Contains(state, "video") {
		modelType = "video"
	}

	// 2. FIX BUG FATAL: Reset state ke awal, JANGAN dihapus kosong.
	// Agar saat user memilih provider lagi, bot tahu ini untuk image atau video.
	h.userStatesMutex.Lock()
	if modelType == "video" {
		h.userStates[callback.From.ID] = "awaiting_video_provider"
	} else {
		h.userStates[callback.From.ID] = "awaiting_image_provider"
	}
	h.userStatesMutex.Unlock()

	// 3. Hapus pesan dashboard lama agar bersih
	deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
	h.Bot.Request(deleteMsg)

	// 4. Tampilkan menu pemilihan provider
	h.showProviderMenu(callback.Message.Chat.ID, callback.From.ID, modelType)
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
	state, ok := h.userStates[callback.From.ID]
	if !ok || !strings.HasPrefix(state, "prompt_for:") {
		h.userStatesMutex.Unlock()
		return
	}
	modelID := strings.TrimPrefix(state, "prompt_for:")
	h.userStatesMutex.Unlock()

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

	// PERBAIKAN: Panggil triggerImageGeneration dengan argumen yang benar.
	// Karena template tidak memiliki advanced settings, kita berikan nil.
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
		log.Printf("ERROR: Model %s not found", modelID)
		return
	}

	// Cek saldo
	totalAvailableCredits := user.PaidCredits + user.FreeCredits
	cost := selectedModel.Cost
	if selectedModel.Type == "video" {
		if user.Diamonds < selectedModel.DiamondCost {
			// Kirim pesan saldo kurang (kode sama seperti sebelumnya, disingkat)
			args := map[string]string{"required": strconv.Itoa(selectedModel.DiamondCost), "balance": strconv.Itoa(user.Diamonds)}
			text := h.Localizer.Getf(lang, "insufficient_diamonds", args)
			h.Bot.Send(tgbotapi.NewMessage(callback.Message.Chat.ID, text))
			return
		}
	} else {
		if totalAvailableCredits < cost {
			// Kirim pesan saldo kurang
			args := map[string]string{"required": strconv.Itoa(cost), "balance": strconv.Itoa(totalAvailableCredits)}
			text := h.Localizer.Getf(lang, "insufficient_credits", args)
			h.Bot.Send(tgbotapi.NewMessage(callback.Message.Chat.ID, text))
			return
		}
	}

	// Inisialisasi State dan Data Pending
	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = fmt.Sprintf("awaiting_prompt_and_settings:%s", modelID)
	h.userStatesMutex.Unlock()

	// Reset pending generation untuk user ini
	h.pendingGenerations[user.TelegramID] = &PendingGeneration{
		ModelID:   modelID,
		ImageURLs: []string{},
	}

	// Hapus pesan menu sebelumnya
	h.Bot.Request(tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID))

	// Tampilkan Dashboard
	h.showGenerationDashboard(callback.Message.Chat.ID, user, selectedModel)
}

// --- PEMBARUAN 1: Tampilkan detail parameter langsung di teks Dashboard ---

func (h *Handler) showGenerationDashboard(chatID int64, user *database.User, model *config.Model) {
	lang := user.LanguageCode

	imageCount := 0
	if pending, ok := h.pendingGenerations[user.TelegramID]; ok {
		imageCount = len(pending.ImageURLs)
	}

	var customSettings map[string]interface{}
	if user.CustomSettings != "" {
		json.Unmarshal([]byte(user.CustomSettings), &customSettings)
	} else {
		customSettings = make(map[string]interface{})
	}

	var textBuilder strings.Builder
	// Hanya tampilkan Nama Model
	textBuilder.WriteString(fmt.Sprintf("<b>🤖 Model: %s</b>\n\n", model.Name))
	
	// HAPUS DESKRIPSI DI SINI (Sesuai request)
	// if model.Description != "" { ... }

	textBuilder.WriteString("<b>⚙️ Settings:</b>\n")
	if model.ConfigurableAspectRatio {
		textBuilder.WriteString(fmt.Sprintf("• Aspect Ratio: <code>%s</code>\n", user.AspectRatio))
	}
	if model.ConfigurableNumOutputs {
		textBuilder.WriteString(fmt.Sprintf("• Quantity: <code>%d</code>\n", user.NumOutputs))
	}
	if model.AcceptsImageInput {
		textBuilder.WriteString(fmt.Sprintf("• Input Images: <code>%d</code>\n", imageCount))
	}

	if len(model.Parameters) > 0 {
		for _, param := range model.Parameters {
			// Skip tampilan parameter duplikat di teks juga agar rapi
			if param.Name == "aspect_ratio" && model.ConfigurableAspectRatio { continue }
			if param.Name == "num_outputs" && model.ConfigurableNumOutputs { continue }

			val, ok := customSettings[param.Name]
			if !ok || val == nil {
				if param.Default != nil {
					val = param.Default
				} else {
					val = "-"
				}
			}
			displayVal := fmt.Sprintf("%v", val)
			if v, ok := val.(float64); ok && v == float64(int(v)) {
				displayVal = fmt.Sprintf("%d", int(v))
			}
			
			textBuilder.WriteString(fmt.Sprintf("• %s: <code>%s</code>\n", param.Label, displayVal))
		}
	}

	textBuilder.WriteString("\n✏️ <b>Ready! Please type your prompt now to generate.</b>")

	msg := tgbotapi.NewMessage(chatID, textBuilder.String())
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = h.createGenerationDashboardKeyboard(lang, *model, user, imageCount)
	
	h.Bot.Send(msg)
}

// Lakukan hal yang sama untuk updateGenerationDashboard (Hapus deskripsi)
func (h *Handler) updateGenerationDashboard(chatID int64, messageID int, user *database.User, model *config.Model) {
	lang := user.LanguageCode
	
	imageCount := 0
	if pending, ok := h.pendingGenerations[user.TelegramID]; ok {
		imageCount = len(pending.ImageURLs)
	}

	var customSettings map[string]interface{}
	if user.CustomSettings != "" {
		json.Unmarshal([]byte(user.CustomSettings), &customSettings)
	} else {
		customSettings = make(map[string]interface{})
	}

	var textBuilder strings.Builder
	textBuilder.WriteString(fmt.Sprintf("<b>🤖 Model: %s</b>\n\n", model.Name))
	
	textBuilder.WriteString("<b>⚙️ Settings:</b>\n")
	if model.ConfigurableAspectRatio {
		textBuilder.WriteString(fmt.Sprintf("• Aspect Ratio: <code>%s</code>\n", user.AspectRatio))
	}
	if model.ConfigurableNumOutputs {
		textBuilder.WriteString(fmt.Sprintf("• Quantity: <code>%d</code>\n", user.NumOutputs))
	}
	if model.AcceptsImageInput {
		textBuilder.WriteString(fmt.Sprintf("• Input Images: <code>%d</code>\n", imageCount))
	}

	if len(model.Parameters) > 0 {
		for _, param := range model.Parameters {
			if param.Name == "aspect_ratio" && model.ConfigurableAspectRatio { continue }
			if param.Name == "num_outputs" && model.ConfigurableNumOutputs { continue }

			val, ok := customSettings[param.Name]
			if !ok || val == nil {
				if param.Default != nil {
					val = param.Default
				} else {
					val = "-"
				}
			}
			displayVal := fmt.Sprintf("%v", val)
			if v, ok := val.(float64); ok && v == float64(int(v)) {
				displayVal = fmt.Sprintf("%d", int(v))
			}
			textBuilder.WriteString(fmt.Sprintf("• %s: <code>%s</code>\n", param.Label, displayVal))
		}
	}

	textBuilder.WriteString("\n✏️ <b>Ready! Please type your prompt now to generate.</b>")

	keyboard := h.createGenerationDashboardKeyboard(lang, *model, user, imageCount)
	
	editMsg := tgbotapi.NewEditMessageText(chatID, messageID, textBuilder.String())
	editMsg.ParseMode = "HTML"
	editMsg.ReplyMarkup = &keyboard
	h.Bot.Send(editMsg)
}

func (h *Handler) triggerVideoGeneration(user *database.User, originalMessage *tgbotapi.Message, modelID, prompt, imageURL string) {
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
		log.Printf("ERROR: Video model with ID '%s' not found.", modelID)
		return
	}

	if user.Diamonds < selectedModel.DiamondCost {
		return
	}

	waitMsg := h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "video_generating"))
	sentMsg, _ := h.Bot.Send(waitMsg)
	defer h.Bot.Send(tgbotapi.NewDeleteMessage(originalMessage.Chat.ID, sentMsg.MessageID))

	action := tgbotapi.NewChatAction(originalMessage.Chat.ID, tgbotapi.ChatUploadVideo)
	h.Bot.Send(action)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var customParams map[string]interface{}
	if user.CustomSettings != "" {
		json.Unmarshal([]byte(user.CustomSettings), &customParams)
	}

	videoUrls, err := h.Replicate.CreatePrediction(ctx, selectedModel.ReplicateID, prompt, imageURL, selectedModel.ImageParameterName, "", 1, customParams)

	if err != nil || len(videoUrls) == 0 {
		failMsg := h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "video_generation_failed"))
		h.Bot.Send(failMsg)
		return
	}

	user.Diamonds -= selectedModel.DiamondCost
	h.DB.UpdateUser(user)

	safePrompt := html.EscapeString(prompt)
	if len(safePrompt) > 900 {
		safePrompt = safePrompt[:900] + "..."
	}
	caption := fmt.Sprintf("<b>Prompt:</b> <pre>%s</pre>\n<b>Model:</b> <code>%s</code>\n<b>Cost:</b> %d 💎", safePrompt, selectedModel.Name, selectedModel.DiamondCost)

	resp, httpErr := http.Get(videoUrls[0])
	if httpErr != nil {
		log.Printf("ERROR: Failed to download video file: %v", httpErr)
		h.Bot.Send(h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "video_generation_failed")))
		return
	}
	defer resp.Body.Close()

	bytes, readErr := ioutil.ReadAll(resp.Body)
	if readErr != nil {
		log.Printf("ERROR: Failed to read video file bytes: %v", readErr)
		h.Bot.Send(h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "video_generation_failed")))
		return
	}

	videoFile := tgbotapi.FileBytes{
		Name:  "generated-video.mp4",
		Bytes: bytes,
	}

	videoMsg := h.newReplyVideo(originalMessage, videoFile)
	videoMsg.Caption = caption
	videoMsg.ParseMode = "HTML"
	h.Bot.Send(videoMsg)
}

// Tambahkan fungsi helper baru ini di mana saja di dalam handlers.go
func (h *Handler) newReplyVideo(originalMessage *tgbotapi.Message, video tgbotapi.RequestFileData) tgbotapi.VideoConfig {
	msg := tgbotapi.NewVideo(originalMessage.Chat.ID, video)
	if originalMessage.Chat.IsGroup() || originalMessage.Chat.IsSuperGroup() {
		msg.ReplyToMessageID = originalMessage.MessageID
	}
	return msg
}

func (h *Handler) handleMessage(message *tgbotapi.Message) {
	h.userStatesMutex.Lock()
	state, ok := h.userStates[message.From.ID]
	h.userStatesMutex.Unlock()

	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		return
	}

	// Jika tidak ada state aktif, abaikan pesan teks biasa (kecuali command start)
	if !ok {
		if message.IsCommand() && message.Command() == "start" {
			h.handleStart(message)
		}
		return
	}

	lang := user.LanguageCode

	// 1. LOGIKA MODE TUNGGU (DASHBOARD): MENERIMA PROMPT TEKS
	if strings.HasPrefix(state, "awaiting_prompt_and_settings:") {
		modelID := strings.TrimPrefix(state, "awaiting_prompt_and_settings:")
		
		// Jika user mengirim teks, itu adalah PROMPT -> Jalankan Generasi
		if message.Text != "" {
			prompt := message.Text
			
			// Ambil gambar yang sudah di-pending (jika ada)
			var pendingImages []string
			if pending, exists := h.pendingGenerations[user.TelegramID]; exists {
				pendingImages = pending.ImageURLs
			}

			// Panggil fungsi trigger generasi utama
			// Catatan: Fungsi ini akan membaca setting (AR, NumOutputs, dll) dari database user
			h.triggerImageGeneration(user, message, modelID, prompt, pendingImages)
			
			// Bersihkan state & data pending setelah proses dimulai
			h.userStatesMutex.Lock()
			delete(h.userStates, user.TelegramID)
			h.userStatesMutex.Unlock()
			delete(h.pendingGenerations, user.TelegramID)
			return
		}
		// Jika user mengirim foto di mode ini tanpa klik tombol "Add Image" dulu, abaikan.
		return
	}

	if ok && strings.HasPrefix(state, "chat_mode") {
		h.handleChatMessage(message)
		return
	}

	// 2. LOGIKA MODE UPLOAD GAMBAR (DARI DASHBOARD)
	if strings.HasPrefix(state, "awaiting_dashboard_image:") {
		// Jika ada foto dikirim
		if message.Photo != nil && len(message.Photo) > 0 {
			// Pastikan objek pending ada
			pending, exists := h.pendingGenerations[user.TelegramID]
			if !exists {
				// Safety check: Buat baru jika hilang
				parts := strings.Split(state, ":")
				modelID := parts[1]
				pending = &PendingGeneration{ModelID: modelID, ImageURLs: []string{}}
				h.pendingGenerations[user.TelegramID] = pending
			}

			// Ambil kualitas foto terbaik (terakhir di array)
			bestPhoto := message.Photo[len(message.Photo)-1]
			imageURL, err := h.getFileURL(bestPhoto.FileID)
			if err != nil {
				log.Printf("ERROR: Failed to get file URL: %v", err)
				return
			}
			pending.ImageURLs = append(pending.ImageURLs, imageURL)

			// Beri notifikasi kecil (reply) bahwa gambar diterima
			count := len(pending.ImageURLs)
			msg := h.newReplyMessage(message, fmt.Sprintf("✅ Image %d received.", count))
			h.Bot.Send(msg)
			return
		}
		// Abaikan teks biasa di mode upload (karena tombol Done ada di inline keyboard)
		return
	}

	if state == "awaiting_prompt_idea" {
		if message.Text == "" { return }
		h.handlePromptIdeaInput(message)
		return
	}

	// 2. [BARU] Jika user sedang di mode IMAGE (Image-to-Prompt)
	if state == "awaiting_prompt_image" {
		// Pastikan user mengirim FOTO
		if message.Photo != nil && len(message.Photo) > 0 {
			h.handlePromptImageInput(message)
		} else {
			// Jika kirim teks doang, ingatkan butuh foto
			h.Bot.Send(tgbotapi.NewMessage(message.Chat.ID, "❌ Please send an image/photo."))
		}
		return
	}

	// 3. LOGIKA INPUT PENGATURAN MANUAL (SEED, QUALITY, DLL)
	if strings.HasPrefix(state, "edit_setting:") {
		parts := strings.Split(strings.TrimPrefix(state, "edit_setting:"), ":")
		if len(parts) < 2 {
			return
		}
		modelID, paramName := parts[0], parts[1]

		// Cari model & tipe parameter
		var selectedModel *config.Model
		var targetParamType string
		found := false
		for _, m := range h.Models {
			if m.ID == modelID {
				selectedModel = &m
				for _, p := range m.Parameters {
					if p.Name == paramName {
						targetParamType = p.Type
						found = true
						break
					}
				}
				break
			}
		}

		if !found || selectedModel == nil {
			return
		}

		// Update Custom Settings user
		var customSettings map[string]interface{}
		if user.CustomSettings != "" {
			json.Unmarshal([]byte(user.CustomSettings), &customSettings)
		} else {
			customSettings = make(map[string]interface{})
		}

		inputValue := message.Text
		var parsedValue interface{}
		var errParse error

		// Validasi & Konversi Input
		switch targetParamType {
		case "integer":
			val, err := strconv.ParseInt(inputValue, 10, 64)
			parsedValue = int(val)
			errParse = err
		case "number":
			parsedValue, errParse = strconv.ParseFloat(inputValue, 64)
		default: // string
			parsedValue = inputValue
		}

		if errParse != nil {
			errorText := fmt.Sprintf("❌ Invalid input. Please enter a valid %s value.", targetParamType)
			msg := h.newReplyMessage(message, errorText)
			h.Bot.Send(msg)
			return
		}

		// Simpan setting ke DB
		customSettings[paramName] = parsedValue
		settingsJSON, _ := json.Marshal(customSettings)
		user.CustomSettings = string(settingsJSON)
		h.DB.UpdateUser(user)

		// Reset state kembali ke Dashboard
		h.userStatesMutex.Lock()
		h.userStates[user.TelegramID] = "awaiting_prompt_and_settings:" + modelID
		h.userStatesMutex.Unlock()

		// Hapus pesan input user agar chat bersih
		h.Bot.Request(tgbotapi.NewDeleteMessage(message.Chat.ID, message.MessageID))
		
		// Tampilkan Dashboard baru dengan nilai setting yang sudah terupdate
		// (Kirim pesan baru agar dashboard ada di paling bawah)
		h.showGenerationDashboard(message.Chat.ID, user, selectedModel)
		return
	}

	// 4. LOGIKA LAIN (Exchange, RemoveBG, Video Prompt - Tetap dipertahankan)

	if state == "awaiting_prompt_idea" {
		if message.Text == "" { return }
		h.handlePromptIdeaInput(message)
		return
	}
	
	if state == "awaiting_exchange_amount" {
		diamondsToBuy, err := strconv.Atoi(message.Text)
		if err != nil || diamondsToBuy <= 0 {
			msg := h.newReplyMessage(message, h.Localizer.Get(lang, "exchange_invalid_amount"))
			h.Bot.Send(msg)
			return
		}

		creditsNeeded := diamondsToBuy * 20
		totalCredits := user.PaidCredits + user.FreeCredits

		if totalCredits < creditsNeeded {
			args := map[string]string{
				"diamonds_to_buy": strconv.Itoa(diamondsToBuy),
				"credits_needed":  strconv.Itoa(creditsNeeded),
				"credits_balance": strconv.Itoa(totalCredits),
			}
			msg := h.newReplyMessage(message, h.Localizer.Getf(lang, "exchange_not_enough_credits", args))
			h.Bot.Send(msg)
			return
		}

		creditsToDeduct := creditsNeeded
		if user.FreeCredits >= creditsToDeduct {
			user.FreeCredits -= creditsToDeduct
		} else {
			creditsToDeduct -= user.FreeCredits
			user.FreeCredits = 0
			user.PaidCredits -= creditsToDeduct
		}

		user.Diamonds += diamondsToBuy
		h.DB.UpdateUser(user)

		h.userStatesMutex.Lock()
		delete(h.userStates, user.TelegramID)
		h.userStatesMutex.Unlock()

		args := map[string]string{
			"credits_spent":        strconv.Itoa(creditsNeeded),
			"diamonds_gained":      strconv.Itoa(diamondsToBuy),
			"new_diamonds_balance": strconv.Itoa(user.Diamonds),
		}
		msg := h.newReplyMessage(message, h.Localizer.Getf(lang, "exchange_success", args))
		h.Bot.Send(msg)
		return
	}

	if strings.HasPrefix(state, "prompt_for_video:") {
		modelID := strings.TrimPrefix(state, "prompt_for_video:")
		var prompt, imageURL string

		if message.Photo != nil && len(message.Photo) > 0 {
			bestPhoto := message.Photo[len(message.Photo)-1]
			url, err := h.getFileURL(bestPhoto.FileID)
			if err != nil {
				return
			}
			imageURL = url
			prompt = message.Caption
		} else {
			prompt = message.Text
		}

		if prompt == "" { return }

		h.triggerVideoGeneration(user, message, modelID, prompt, imageURL)
		return
	}

	if state == "awaiting_image_for_removebg" {
		if message.Photo == nil || len(message.Photo) == 0 { return }
		bestPhoto := message.Photo[len(message.Photo)-1]
		imageURL, err := h.getFileURL(bestPhoto.FileID)
		if err != nil { return }
		h.triggerImageGeneration(user, message, "remove-background", "", imageURL)
		return
	}

	if state == "awaiting_image_for_upscaler" {
		if message.Photo == nil || len(message.Photo) == 0 { return }
		bestPhoto := message.Photo[len(message.Photo)-1]
		imageURL, err := h.getFileURL(bestPhoto.FileID)
		if err != nil { return }
		h.triggerImageGeneration(user, message, "recraft-upscaler", "", imageURL)
		return
	}
}

// Fungsi baru untuk memulai alur
func (h *Handler) startStyleConfirmationFlow(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode

	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "awaiting_style_confirmation"
	h.userStatesMutex.Unlock()

	text := "<b>Prompt diterima!</b> ✅\n\nPilih gaya di bawah untuk menyempurnakan gambarmu, atau langsung mulai proses generasi."
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"
	keyboard := h.createStyleConfirmationKeyboard(lang)
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

// Fungsi baru untuk menangani semua callback dari alur gaya
func (h *Handler) handleStyleCallback(callback *tgbotapi.CallbackQuery, action string) {
	userID := callback.From.ID
	user, _ := h.getOrCreateUser(callback.From)
	lang := user.LanguageCode

	// Ambil data yang tersimpan
	pending, ok := h.pendingGenerations[userID]
	if !ok {
		// Jika tidak ada data, batalkan
		deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
		h.Bot.Request(deleteMsg)
		return
	}

	switch action {
	case "generate_now":
		delete(h.pendingGenerations, userID) // Hapus data sementara
		deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
		h.Bot.Request(deleteMsg)
		h.triggerImageGeneration(user, callback.Message, pending.ModelID, pending.Prompt, pending.ImageURL)

	case "show_styles":
		text := "Silakan pilih gaya visual yang Anda inginkan:"
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
		keyboard := h.createStyleSelectionKeyboard(h.Styles, lang)
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)

	case "back_to_confirm":
		text := "<b>Prompt diterima!</b> ✅\n\nPilih gaya di bawah untuk menyempurnakan gambarmu, atau langsung mulai proses generasi."
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
		msg.ParseMode = "HTML"
		keyboard := h.createStyleConfirmationKeyboard(lang)
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)
	}
}

func (h *Handler) handleStyleSelection(callback *tgbotapi.CallbackQuery, styleID string) {
	userID := callback.From.ID

	h.userStatesMutex.Lock()
	state, ok := h.userStates[userID]
	if !ok || !strings.HasPrefix(state, "awaiting_style_for:") {
		h.userStatesMutex.Unlock()
		return
	}
	modelID := strings.TrimPrefix(state, "awaiting_style_for:")

	h.userStates[userID] = fmt.Sprintf("prompt_for:%s:%s", modelID, styleID)
	h.userStatesMutex.Unlock()


	user, _ := h.getOrCreateUser(callback.From)
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


	if selectedModel.AcceptsMultipleImages {
		h.userStatesMutex.Lock()
		h.userStates[user.TelegramID] = fmt.Sprintf("awaiting_multi_image:%s:%s", modelID, styleID)
		h.userStatesMutex.Unlock()

		h.pendingGenerations[user.TelegramID] = &PendingGeneration{
			ModelID:   modelID,
			StyleID:   styleID,
			ImageURLs: []string{},
		}

		// Kirim pesan baru dengan keyboard reply
		args := map[string]string{
			"model_name": selectedModel.Name,
			"count":      "0",
			"max":        "4",
		}
		text := h.Localizer.Getf(lang, "multi_image_prompt", args)
		msg := tgbotapi.NewMessage(user.TelegramID, text)
		msg.ParseMode = "HTML"
		keyboard := h.createMultiImageReplyKeyboard(lang)
		msg.ReplyMarkup = keyboard

		// Simpan message ID dari pesan baru ini untuk di-update nanti
		sentMsg, _ := h.Bot.Send(msg)
		h.pendingGenerations[user.TelegramID].MessageID = sentMsg.MessageID

		// Hapus pesan lama yang berisi tombol style
		deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
		h.Bot.Send(deleteMsg)
		return
	}
	// --- AKHIR LOGIKA RENCANA B ---

	var styleName string
	for _, style := range h.Styles {
		if style.ID == styleID {
			styleName = style.Name
			break
		}
	}

	promptRequestArgs := map[string]string{
		"style_name": styleName,
	}
	mainText := h.Localizer.Getf(lang, "style_select_prompt", promptRequestArgs)

	if selectedModel.Description != "" {
		mainText += fmt.Sprintf("\n\n<blockquote expandable>%s</blockquote>", selectedModel.Description)
	}

	var warningText string
	if user.NumOutputs > 1 && !selectedModel.ConfigurableNumOutputs {
		warningText = h.Localizer.Get(lang, "single_output_warning")
	}

	fullText := warningText + mainText

	if user.NumOutputs > 1 && selectedModel.ConfigurableNumOutputs {
		totalCost := selectedModel.Cost * user.NumOutputs
		warningArgs := map[string]string{
			"num_images": strconv.Itoa(user.NumOutputs),
			"total_cost": strconv.Itoa(totalCost),
		}
		costWarning := h.Localizer.Getf(lang, "multiple_images_warning", warningArgs)
		fullText += costWarning
	}

	msg := tgbotapi.NewMessage(callback.Message.Chat.ID, fullText)
	msg.ParseMode = "HTML"

	var keyboardRows [][]tgbotapi.InlineKeyboardButton
	if selectedModel.Parameters != nil && len(selectedModel.Parameters) > 0 {
		advButton := tgbotapi.NewInlineKeyboardButtonData("⚙️ Advanced Settings", "adv_setting_open:"+modelID)
		keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(advButton))
	}
	cancelButton := tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "cancel_button"), "cancel_flow")
	keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(cancelButton))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)
	msg.ReplyMarkup = &keyboard

	h.Bot.Send(msg)

	deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
	h.Bot.Send(deleteMsg)
}

// File: internal/bot/handlers.go

func (h *Handler) triggerImageGeneration(user *database.User, originalMessage *tgbotapi.Message, modelID, prompt string, imageURLAndParams ...interface{}) {
	// Hapus state agar user bersih
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
		log.Printf("ERROR: Model with ID '%s' not found.", modelID)
		return
	}

	var finalImageURL string
	var finalImageURLs []string
	var rawCustomParams map[string]interface{} // <--- Ganti nama jadi rawCustomParams

	// Parsing argumen
	if len(imageURLAndParams) > 0 {
		if url, ok := imageURLAndParams[0].(string); ok {
			finalImageURL = url
		} else if urls, ok := imageURLAndParams[0].([]string); ok {
			finalImageURLs = urls
		}
	}
	if len(imageURLAndParams) > 1 {
		if params, ok := imageURLAndParams[1].(map[string]interface{}); ok {
			rawCustomParams = params
		}
	}

	// Load custom settings mentah dari DB
	if rawCustomParams == nil {
		if user.CustomSettings != "" {
			json.Unmarshal([]byte(user.CustomSettings), &rawCustomParams)
		} else {
			rawCustomParams = make(map[string]interface{})
		}
	}

	// --- [AWAL LOGIKA SANITASI / WHITELIST] --- 
	// Kita buat map baru yang BERSIH. Hanya parameter yang ada di models.json 
	// yang boleh masuk ke sini. Parameter sisa (sampah) dibuang.
	cleanParams := make(map[string]interface{})

	// 1. Tentukan Aspect Ratio & Num Outputs (Ini spesial, tidak masuk cleanParams dulu)
	var aspectRatio string
	if selectedModel.ConfigurableAspectRatio {
		aspectRatio = user.AspectRatio
	}

	var numOutputs int
	if selectedModel.ConfigurableNumOutputs {
		numOutputs = user.NumOutputs
	} else {
		numOutputs = 1
	}

	// Safety Check
	if numOutputs <= 0 { numOutputs = 1 }
	if modelID == "remove-background" || modelID == "recraft-upscaler" {
		numOutputs = 1
	}

	// 2. Filter parameter lainnya berdasarkan models.json
	for _, param := range selectedModel.Parameters {
		// Skip AR dan NumOutputs karena sudah dihandle variabel terpisah di atas
		if param.Name == "aspect_ratio" || param.Name == "num_outputs" {
			continue
		}

		// Cek apakah user punya settingan untuk parameter ini di DB (rawCustomParams)?
		if val, exists := rawCustomParams[param.Name]; exists {
			// JIKA ADA: Masukkan ke cleanParams (Valid)
			cleanParams[param.Name] = val
		} else if param.Default != nil {
			// JIKA TIDAK ADA: Pakai default dari models.json
			cleanParams[param.Name] = param.Default
		}
        // Parameter sampah dari model lain otomatis tidak ter-copy ke cleanParams
	}

	// Debugging: Lihat apa yang bersih
	log.Printf("DEBUG: Cleaned params for model %s: %+v", modelID, cleanParams)
    // --- [AKHIR LOGIKA SANITASI] ---

	// --- CEK SALDO ---
	totalCost := selectedModel.Cost * numOutputs
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

	// --- EKSEKUSI ---
	waitMsg := h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generating"))
	sentMsg, _ := h.Bot.Send(waitMsg)
	defer h.Bot.Send(tgbotapi.NewDeleteMessage(originalMessage.Chat.ID, sentMsg.MessageID))

	action := tgbotapi.NewChatAction(originalMessage.Chat.ID, tgbotapi.ChatUploadPhoto)
	h.Bot.Send(action)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var imageUrls []string
	var err error

	// Panggil Service Replicate menggunakan cleanParams (yang sudah bersih) <--- PENTING
	if len(finalImageURLs) > 0 {
		imageUrls, err = h.Replicate.CreatePrediction(ctx, selectedModel.ReplicateID, prompt, "", selectedModel.ImageParameterName, aspectRatio, numOutputs, cleanParams, finalImageURLs)
	} else {
		imageUrls, err = h.Replicate.CreatePrediction(ctx, selectedModel.ReplicateID, prompt, finalImageURL, selectedModel.ImageParameterName, aspectRatio, numOutputs, cleanParams)
	}

	if err != nil || len(imageUrls) == 0 {
		// Log error detail untuk debugging di console
		log.Printf("ERROR REPLICATE: %v", err)
		failMsg := h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generation_failed"))
		h.Bot.Send(failMsg)
		return
	}

	// --- DEDUKSI KREDIT ---
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

	user.GeneratedImageCount++
	h.DB.UpdateUser(user)

	// Referral Bonus
	if user.GeneratedImageCount == 2 && user.ReferrerID != 0 {
		referrer, errRef := h.DB.GetUserByTelegramID(user.ReferrerID)
		if errRef == nil && referrer != nil {
			referrer.PaidCredits += 5
			if errUpdate := h.DB.UpdateUser(referrer); errUpdate == nil {
				notificationText := h.Localizer.Get(referrer.LanguageCode, "referral_bonus_notification")
				msg := tgbotapi.NewMessage(referrer.TelegramID, notificationText)
				msg.ParseMode = "Markdown"
				h.Bot.Send(msg)
			}
		}
	}

	// --- OUTPUT ---
	if modelID == "remove-background" || modelID == "recraft-upscaler" {
		h.handleSpecialModelOutput(originalMessage, imageUrls[0], modelID, lang)
	} else {
		safePrompt := html.EscapeString(prompt)
		if len(safePrompt) > 900 {
			safePrompt = safePrompt[:900] + "..."
		}
		caption := fmt.Sprintf("<b>Prompt:</b> <pre>%s</pre>\n<b>Model:</b> <code>%s</code>\n<b>Cost:</b> %d 💵", safePrompt, selectedModel.Name, totalCost)

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

		// Simpan URL terakhir
		h.lastGeneratedURLsMutex.Lock()
		h.lastGeneratedURLs[user.TelegramID] = imageUrls
		h.lastGeneratedURLsMutex.Unlock()

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
}

func (h *Handler) handleUpscaler(message *tgbotapi.Message) {
	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode

	var upscalerModel *config.Model
	for _, m := range h.Models {
		// --- PERUBAHAN DI SINI: Cari ID model baru ---
		if m.ID == "recraft-upscaler" {
			upscalerModel = &m
			break
		}
	}

	if upscalerModel == nil {
		log.Println("ERROR: 'recraft-upscaler' model not found in models.json")
		return
	}

	totalAvailableCredits := user.PaidCredits + user.FreeCredits
	if totalAvailableCredits < upscalerModel.Cost {
		args := map[string]string{
			"required": strconv.Itoa(upscalerModel.Cost),
			"balance":  strconv.Itoa(totalAvailableCredits),
		}
		text := h.Localizer.Getf(lang, "insufficient_credits", args)
		msg := h.newReplyMessage(message, text)
		h.Bot.Send(msg)
		return
	}

	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "awaiting_image_for_upscaler"
	h.userStatesMutex.Unlock()

	args := map[string]string{
		"cost": strconv.Itoa(upscalerModel.Cost),
	}
	text := h.Localizer.Getf(lang, "upscaler_prompt", args)

	msg := h.newReplyMessage(message, text)
	keyboard := h.createCancelFlowKeyboard(lang)
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
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
			TelegramID:           message.From.ID,
			Username:             message.From.UserName,
			PaidCredits:          0, // Pengguna baru mulai dengan 0 kredit berbayar
			FreeCredits:          5,
			LastFreeCreditsReset: time.Now(),
			LanguageCode:         "en",
			AspectRatio:          "1:1",
			NumOutputs:           1,
			ReferrerID:           referrerID, // ID referral langsung dimasukkan di sini
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
		"paid_credits":  "`" + strconv.Itoa(user.PaidCredits) + "`",
		"free_credits":  "`" + strconv.Itoa(user.FreeCredits) + "`",
		"diamonds":      "`" + strconv.Itoa(user.Diamonds) + "`",
		"total_credits": strconv.Itoa(totalCredits),
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
			Diamonds:             0,
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
	user, _ := h.getOrCreateUser(message.From)

	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "awaiting_image_provider"
	h.userStatesMutex.Unlock()

	h.showProviderMenu(message.Chat.ID, user.TelegramID, "image")

}

// File: internal/bot/handlers.go

// FUNGSI BARU
// File: internal/bot/handlers.go

// File: internal/bot/handlers.go

func (h *Handler) handleRawDownload(callback *tgbotapi.CallbackQuery) {
	userID := callback.From.ID
	lang := h.getUserLang(userID)

	h.Bot.Request(tgbotapi.NewCallback(callback.ID, ""))
	action := tgbotapi.NewChatAction(callback.Message.Chat.ID, tgbotapi.ChatUploadDocument)
	h.Bot.Send(action)

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

// handleSpecialModelOutput menangani output file untuk model RemoveBG dan Upscaler
// yang outputnya berupa file (PNG/JPG) bukan URL gambar biasa.
func (h *Handler) handleSpecialModelOutput(originalMessage *tgbotapi.Message, url, modelID, lang string) {
	// Download file dari URL
	resp, httpErr := http.Get(url)
	if httpErr != nil {
		log.Printf("ERROR: Failed to download result file: %v", httpErr)
		h.Bot.Send(h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generation_failed")))
		return
	}
	defer resp.Body.Close()

	// Baca isi file
	fileBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		log.Printf("ERROR: Failed to read result file body: %v", readErr)
		h.Bot.Send(h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generation_failed")))
		return
	}

	// Tentukan nama file dan pesan sukses
	var fileName, successMessage string
	if modelID == "remove-background" {
		fileName = "background-removed.png"
		successMessage = h.Localizer.Get(lang, "removebg_success")
	} else {
		fileName = "upscaled-image.jpg"
		successMessage = h.Localizer.Get(lang, "upscaler_success")
	}

	// Kirim sebagai Dokumen (File) agar tidak terkompresi
	docBytes := tgbotapi.FileBytes{Name: fileName, Bytes: fileBytes}
	doc := h.newReplyDocument(originalMessage, docBytes)
	doc.Caption = successMessage
	h.Bot.Send(doc)
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

// Ditambahkan: Fungsi untuk menangani saat bot join/leave grup
func (h *Handler) handleMyChatMemberUpdate(update *tgbotapi.ChatMemberUpdated) {
	// Hanya proses jika update terjadi di grup atau supergroup
	if !(update.Chat.IsGroup() || update.Chat.IsSuperGroup()) {
		return
	}

	// Cek status baru bot
	newStatus := update.NewChatMember.Status
	// Cek status lama bot
	oldStatus := update.OldChatMember.Status

	// Logika: Bot ditambahkan ke grup
	// (Status berubah dari 'left' atau 'kicked' menjadi 'member')
	if (oldStatus == "left" || oldStatus == "kicked") && newStatus == "member" {
		newGroup := &database.Group{
			GroupID:    update.Chat.ID,
			GroupTitle: update.Chat.Title,
		}
		h.DB.CreateGroup(newGroup)
	}

	// Logika: Bot dikeluarkan dari grup
	// (Status berubah dari 'member' atau 'administrator' menjadi 'left' atau 'kicked')
	if (oldStatus == "member" || oldStatus == "administrator") && (newStatus == "left" || newStatus == "kicked") {
		h.DB.DeleteGroup(update.Chat.ID)
	}
}

// Ditambahkan: Fungsi untuk broadcast ke grup
// Diperbarui: Fungsi broadcast grup kini mendukung gambar
// Diperbarui: Fungsi broadcast grup kini mendukung gambar (Perbaikan Logika Caption)
// Diperbarui: Fungsi broadcast grup mendukung gambar via caption atau reply
func (h *Handler) handleBroadcastGroup(message *tgbotapi.Message) {
	lang := "en"
	var broadcastText, photoFileID string

	// Selalu ambil teks dari argumen perintah pada pesan saat ini.
	broadcastText = message.CommandArguments()

	// Prioritas 1: Cek apakah pesan ini sendiri memiliki foto (metode caption).
	if message.Photo != nil && len(message.Photo) > 0 {
		photoFileID = message.Photo[len(message.Photo)-1].FileID
	} else if message.ReplyToMessage != nil && message.ReplyToMessage.Photo != nil && len(message.ReplyToMessage.Photo) > 0 {
		// Prioritas 2: Jika tidak, cek apakah pesan ini me-reply sebuah pesan dengan foto.
		photoFileID = message.ReplyToMessage.Photo[len(message.ReplyToMessage.Photo)-1].FileID
	}

	// Validasi: Pastikan ada sesuatu untuk dikirim (teks atau foto)
	if broadcastText == "" && photoFileID == "" {
		msg := h.newReplyMessage(message, "Usage:\n1. /broadcastgroup [message]\n2. Reply to an image with /broadcastgroup [caption]\n3. Send an image with /broadcastgroup [caption] in the caption.")
		h.Bot.Send(msg)
		return
	}

	allGroups, err := h.DB.GetAllGroups()
	if err != nil {
		log.Printf("ERROR: Failed to get all groups for broadcast: %v", err)
		return
	}
	if len(allGroups) == 0 {
		msg := h.newReplyMessage(message, "The bot is not a member of any groups.")
		h.Bot.Send(msg)
		return
	}

	args := map[string]string{"group_count": strconv.Itoa(len(allGroups))}
	startMsgText := "⏳ Starting group broadcast to {group_count} groups..."
	startMsg := h.newReplyMessage(message, h.Localizer.Getf(lang, startMsgText, args))
	h.Bot.Send(startMsg)

	go func(adminChatID int64) {
		sentCount := 0
		for _, group := range allGroups {
			var err error
			// Jika ada FileID foto, kirim sebagai foto
			if photoFileID != "" {
				photoMsg := tgbotapi.NewPhoto(group.GroupID, tgbotapi.FileID(photoFileID))
				photoMsg.Caption = broadcastText
				photoMsg.ParseMode = "HTML"
				_, err = h.Bot.Send(photoMsg)
			} else { // Jika tidak, kirim sebagai pesan teks biasa
				textMsg := tgbotapi.NewMessage(group.GroupID, broadcastText)
				textMsg.ParseMode = "HTML"
				_, err = h.Bot.Send(textMsg)
			}

			if err == nil {
				sentCount++
			} else {
				log.Printf("WARN: Failed to send broadcast to group %d (%s). Removing from DB. Error: %v", group.GroupID, group.GroupTitle, err)
				h.DB.DeleteGroup(group.GroupID)
			}
			time.Sleep(100 * time.Millisecond)
		}

		finishArgs := map[string]string{
			"sent_count":  strconv.Itoa(sentCount),
			"total_count": strconv.Itoa(len(allGroups)),
		}
		finishMsgText := "✅ Group broadcast finished. Sent to {sent_count} of {total_count} groups."
		finishMsg := tgbotapi.NewMessage(adminChatID, h.Localizer.Getf(lang, finishMsgText, finishArgs))
		h.Bot.Send(finishMsg)
	}(message.Chat.ID)
}

func (h *Handler) handleOpenAdvancedSettings(callback *tgbotapi.CallbackQuery, modelID string) {
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

	keyboard, text := h.createAdvancedSettingsKeyboard(lang, selectedModel, user)

	msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

func (h *Handler) handleSelectAdvancedSetting(callback *tgbotapi.CallbackQuery, modelID, paramName string) {
	user, err := h.getOrCreateUser(callback.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode

	var selectedParam *config.Parameter
	// Langsung cari parameter yang relevan tanpa menyimpan modelnya
	found := false
	for _, m := range h.Models {
		if m.ID == modelID {
			for _, p := range m.Parameters {
				if p.Name == paramName {
					selectedParam = &p
					found = true
					break
				}
			}
		}
		if found {
			break
		}
	}

	if selectedParam == nil {
		log.Printf("WARN: Parameter %s for model %s not found", paramName, modelID)
		return
	}

	// Jika parameter punya daftar 'options', tampilkan sebagai tombol
	if len(selectedParam.Options) > 0 {
		var keyboardRows [][]tgbotapi.InlineKeyboardButton
		var currentRow []tgbotapi.InlineKeyboardButton

		for _, option := range selectedParam.Options {
			callbackData := fmt.Sprintf("adv_set_option:%s:%s:%s", modelID, paramName, option)
			button := tgbotapi.NewInlineKeyboardButtonData(option, callbackData)
			currentRow = append(currentRow, button)

			if len(currentRow) == 2 {
				keyboardRows = append(keyboardRows, currentRow)
				currentRow = []tgbotapi.InlineKeyboardButton{}
			}
		}
		if len(currentRow) > 0 {
			keyboardRows = append(keyboardRows, currentRow)
		}

		backCallback := "dash_back_main" 
		backButton := tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "back_button"), backCallback)
		keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(backButton))

		keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)

		msgText := fmt.Sprintf("Select a value for <b>%s</b>:", selectedParam.Label)
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, msgText)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)

	} else { // Jika tidak ada 'options', minta input teks seperti biasa
		h.userStatesMutex.Lock()
		h.userStates[user.TelegramID] = fmt.Sprintf("edit_setting:%s:%s", modelID, paramName)
		h.userStatesMutex.Unlock()

		var promptText strings.Builder
		promptText.WriteString(fmt.Sprintf("Please enter a new value for <b>%s</b>.", selectedParam.Label))
		if selectedParam.Description != "" {
			promptText.WriteString(fmt.Sprintf("\n\n<i>%s</i>", selectedParam.Description))
		}

		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, promptText.String())
		msg.ParseMode = "HTML"

		backButton := tgbotapi.NewInlineKeyboardButtonData("🔙 Back", "dash_back_main")
		keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(backButton))
		msg.ReplyMarkup = &keyboard

		h.Bot.Send(msg)
	}
}

func (h *Handler) handleSetOption(callback *tgbotapi.CallbackQuery, modelID, paramName, optionValue string) {
	user, _ := h.getOrCreateUser(callback.From)

	var customSettings map[string]interface{}
	if user.CustomSettings != "" {
		json.Unmarshal([]byte(user.CustomSettings), &customSettings)
	} else {
		customSettings = make(map[string]interface{})
	}

	var targetParamType string
	var selectedModel *config.Model
	found := false
	for _, model := range h.Models {
		if model.ID == modelID {
			selectedModel = &model // Simpan referensi model untuk refresh dashboard nanti
			for _, param := range model.Parameters {
				if param.Name == paramName {
					targetParamType = param.Type
					found = true
					break
				}
			}
		}
		if found {
			break
		}
	}

	if !found || selectedModel == nil {
		log.Printf("WARN: Parameter %s for model %s not found in handleSetOption", paramName, modelID)
		return
	}

	// Konversi nilai berdasarkan tipe data
	var parsedValue interface{}
	switch targetParamType {
	case "integer":
		val, _ := strconv.ParseInt(optionValue, 10, 64)
		parsedValue = int(val)
	case "number":
		parsedValue, _ = strconv.ParseFloat(optionValue, 64)
	default:
		parsedValue = optionValue
	}

	customSettings[paramName] = parsedValue
	settingsJSON, _ := json.Marshal(customSettings)
	user.CustomSettings = string(settingsJSON)
	h.DB.UpdateUser(user)

	// PERUBAHAN: Refresh Dashboard, bukan buka menu Advanced Settings lagi
	h.updateGenerationDashboard(callback.Message.Chat.ID, callback.Message.MessageID, user, selectedModel)
}

// showPromptEntryScreen is a helper function to display the prompt entry message.
// This avoids code duplication between handleStyleSelection and the 'back' action.
func (h *Handler) showPromptEntryScreen(callback *tgbotapi.CallbackQuery, modelID string, styleID string, isEdit bool) {
	user, _ := h.getOrCreateUser(callback.From)
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

	var styleName string
	for _, style := range h.Styles {
		if style.ID == styleID {
			styleName = style.Name
			break
		}
	}

	promptRequestArgs := map[string]string{
		"style_name": styleName,
	}
	mainText := h.Localizer.Getf(lang, "style_select_prompt", promptRequestArgs)

	if selectedModel.Description != "" {
		mainText += fmt.Sprintf("\n\n<blockquote expandable>%s</blockquote>", selectedModel.Description)
	}

	var warningText string
	if user.NumOutputs > 1 && !selectedModel.ConfigurableNumOutputs {
		warningText = h.Localizer.Get(lang, "single_output_warning")
	}

	fullText := warningText + mainText

	if user.NumOutputs > 1 && selectedModel.ConfigurableNumOutputs {
		totalCost := selectedModel.Cost * user.NumOutputs
		warningArgs := map[string]string{
			"num_images": strconv.Itoa(user.NumOutputs),
			"total_cost": strconv.Itoa(totalCost),
		}
		costWarning := h.Localizer.Getf(lang, "multiple_images_warning", warningArgs)
		fullText += costWarning
	}

	var keyboardRows [][]tgbotapi.InlineKeyboardButton
	if selectedModel.Parameters != nil && len(selectedModel.Parameters) > 0 {
		advButton := tgbotapi.NewInlineKeyboardButtonData("⚙️ Advanced Settings", "adv_setting_open:"+modelID)
		keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(advButton))
	}
	cancelButton := tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "cancel_button"), "cancel_flow")
	keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(cancelButton))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)

	if isEdit {
		msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, fullText)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)
	} else {
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, fullText)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)

		deleteMsg := tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID)
		h.Bot.Send(deleteMsg)
	}
}

func (h *Handler) handleVideoCommand(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)

	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "awaiting_video_provider"
	h.userStatesMutex.Unlock()

	h.showProviderMenu(message.Chat.ID, user.TelegramID, "video")

}

func (h *Handler) handleExchangeCommand(message *tgbotapi.Message) {
	user, err := h.getOrCreateUser(message.From)
	if err != nil {
		return
	}
	lang := user.LanguageCode

	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "awaiting_exchange_amount"
	h.userStatesMutex.Unlock()

	totalCredits := user.PaidCredits + user.FreeCredits
	args := map[string]string{
		"credits":  strconv.Itoa(totalCredits),
		"diamonds": strconv.Itoa(user.Diamonds),
	}
	text := h.Localizer.Getf(lang, "exchange_prompt", args)

	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"
	keyboard := h.createCancelFlowKeyboard(lang)
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

func (h *Handler) showProviderMenu(chatID int64, userID int64, modelType string, messageID ...int) {
	user, _ := h.getOrCreateUser(&tgbotapi.User{ID: userID})
	lang := user.LanguageCode

	// 1. Saring provider (kode sama seperti sebelumnya)
	var filteredProviders []config.Provider
	providerHasModel := make(map[string]bool)

	for _, model := range h.Models {
		if model.Type == modelType {
			providerID := strings.Split(model.ReplicateID, "/")[0]
			providerHasModel[providerID] = true
		}
	}

	for _, provider := range h.Providers {
		if _, ok := providerHasModel[provider.ID]; ok {
			filteredProviders = append(filteredProviders, provider)
		}
	}

	// 2. Siapkan teks yang LEBIH BERSIH
	var text string
	if modelType == "video" {
		text = h.Localizer.Get(lang, "choose_video_model")
	} else {
		// Gunakan teks sederhana tanpa menampilkan setting saat ini
		text = "<b>🎨 Select Your AI Model</b>\n\nPlease choose a provider below:"
	}

	keyboard := h.createProviderSelectionKeyboard(filteredProviders, lang)

	// 3. Kirim pesan
	if len(messageID) > 0 {
		msg := tgbotapi.NewEditMessageText(chatID, messageID[0], text)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)
	} else {
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)
	}
}