package bot

import (
	"context"
	"encoding/json"
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

type PendingGeneration struct {
	ModelID  string
	Prompt   string
	ImageURL string
}

type Handler struct {
	Bot             *tgbotapi.BotAPI
	DB              *database.Client
	Localizer       *localization.Localizer
	Providers       []config.Provider
	Models          []config.Model
	PromptTemplates []config.PromptTemplate
	Styles                []config.StyleTemplate  
	Replicate       *services.ReplicateClient
	userStates      map[int64]string
	userStatesMutex sync.Mutex
	Config          *config.Config
	lastGeneratedURLs     map[int64][]string // <-- BARU: Untuk menyimpan URL RAW
	lastGeneratedURLsMutex sync.Mutex 
	PaymentHandler *payments.PaymentHandler
	GroupHandler          *GroupHandler
	pendingGenerations    map[int64]*PendingGeneration 
}

func NewHandler(api *tgbotapi.BotAPI, db *database.Client, localizer *localization.Localizer, providers []config.Provider, models []config.Model, templates []config.PromptTemplate, styles []config.StyleTemplate, replicate *services.ReplicateClient, cfg *config.Config, paymentHandler *payments.PaymentHandler) *Handler {
	h := &Handler{
		Bot:               api,
		DB:                db,
		Localizer:         localizer,
		Providers:         providers,
		Models:            models,
		PromptTemplates:   templates,
		Styles:              styles, 
		Replicate:         replicate,
		userStates:        make(map[int64]string),
		Config:            cfg,
		lastGeneratedURLs: make(map[int64][]string),
		PaymentHandler:    paymentHandler,
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
	case update.MyChatMember != nil:
        h.handleMyChatMemberUpdate(update.MyChatMember)
	}
}

func (h *Handler) handleCommand(message *tgbotapi.Message) {
	command := message.Command()

	// Cek apakah perintah ini khusus admin
	isAdminCommand := command == "stats" || command == "addcredits" || command == "broadcast" || command == "broadcastgroup"
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
	case "broadcastgroup":
        h.handleBroadcastGroup(message)
	case "broadcast":
		h.handleBroadcast(message)
	case "settings":
		h.handleSettings(message)
	case "topup":
		h.PaymentHandler.ShowTopUpOptions(message.Chat.ID)
	case "removebg":
		h.handleRemoveBg(message)
	case "upscaler":
		h.handleUpscaler(message)
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
	case "adv_setting_open":
		h.handleOpenAdvancedSettings(callback, data) // data = modelID
	case "adv_setting_select":
		// 'parts' sudah didefinisikan di atas dan berisi seluruh data.
		// parts[0] = "adv_setting_select"
		// parts[1] = modelID
		// parts[2] = paramName
		if len(parts) > 2 {
			h.handleSelectAdvancedSetting(callback, parts[1], parts[2])
		}
	case "adv_setting_back":
		// Ambil state pengguna saat ini untuk mengetahui model & gaya yang dipilih
		h.userStatesMutex.Lock()
		state, ok := h.userStates[callback.From.ID]
		h.userStatesMutex.Unlock()

		if !ok || !strings.HasPrefix(state, "prompt_for:") {
			// Jika state rusak, kembali ke pemilihan model sebagai fallback
			h.handleModelSelection(callback, data) // data adalah modelID
			return
		}

		parts := strings.Split(state, ":")
		if len(parts) < 3 {
			// Jika state rusak, kembali ke pemilihan model sebagai fallback
			h.handleModelSelection(callback, data) // data adalah modelID
			return
		}

		modelID := parts[1]
		styleID := parts[2]
		
		// Panggil helper untuk kembali ke layar prompt.
		// `isEdit` diatur ke true karena kita mengedit pesan "Advanced Settings".
		h.showPromptEntryScreen(callback, modelID, styleID, true)
	case "adv_set_option":
		// Format: adv_set_option:modelID:paramName:value
		// Value bisa mengandung ':', contohnya '16:9'
		if len(parts) >= 4 {
			modelID := parts[1]
			paramName := parts[2]
			// Gabungkan kembali semua bagian value yang mungkin terpisah oleh ':'
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
	
	case "main_menu_topup": // <-- CASE BARU
		h.PaymentHandler.ShowTopUpOptions(callback.Message.Chat.ID, callback.Message.MessageID)

	case "download_raw": // <-- BARU
		h.handleRawDownload(callback) // <-- BARU

	case "main_menu_back":
		// Kembali ke menu utama dari halaman lain
		h.handleStart(dummyMessage)
	
	case "topup_stars":
		h.PaymentHandler.ShowStarsPackages(callback.Message.Chat.ID, callback.Message.MessageID)
	
	case "topup_manual":
		h.PaymentHandler.ShowManualPaymentOptions(callback.Message.Chat.ID, callback.Message.MessageID)
	case "topup_transfer_bank":
		h.PaymentHandler.ShowManualPaymentInfo(callback.Message.Chat.ID, callback.Message.MessageID)

	case "topup_back_to_main": // <-- CASE BARU
		h.PaymentHandler.ShowTopUpOptions(callback.Message.Chat.ID, callback.Message.MessageID)
	case "topup_back_to_manual": // <-- CASE BARU
		h.PaymentHandler.ShowManualPaymentOptions(callback.Message.Chat.ID, callback.Message.MessageID)

	case "faq_show":
        h.handleFaqShow(callback, data)
    case "faq_back":
        h.handleFaqBack(callback)
	case "style_confirm":
		h.handleStyleCallback(callback, data)
		return // Return agar tidak dilanjutkan ke switch di bawah
	case "style_select":
		h.handleStyleSelection(callback, data)
		return // Return agar tidak dilanjutkan ke switch di bawah
	case "topup_bmac":
		h.PaymentHandler.ShowBMACPackages(callback.Message.Chat.ID, callback.Message.MessageID)
	case "main_menu_upscaler":
		h.handleUpscaler(dummyMessage)
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
	state, ok := h.userStates[callback.From.ID]
	// Pastikan state-nya benar sebelum memproses
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
		return
	}

	totalAvailableCredits := user.PaidCredits + user.FreeCredits
	if totalAvailableCredits < selectedModel.Cost {
		args := map[string]string{
			"required": strconv.Itoa(selectedModel.Cost),
			"balance":  strconv.Itoa(totalAvailableCredits),
		}
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
	h.userStates[user.TelegramID] = fmt.Sprintf("awaiting_style_for:%s", modelID)
	h.userStatesMutex.Unlock()

	text := h.Localizer.Get(lang, "style_flow_title")

	// Panggil keyboard untuk memilih gaya, lalu kirim pesan. Selesai.
	keyboard := h.createStyleSelectionKeyboard(h.Styles, lang)
	
	msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID, text)
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

func (h *Handler) handleMessage(message *tgbotapi.Message) {
	h.userStatesMutex.Lock()
	state, ok := h.userStates[message.From.ID]
	h.userStatesMutex.Unlock()

	if !ok {
		return
	}

	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode

	if state == "awaiting_image_for_removebg" {
		if message.Photo == nil || len(message.Photo) == 0 {
			return
		}

		bestPhoto := message.Photo[len(message.Photo)-1]
		imageURL, err := h.getFileURL(bestPhoto.FileID)
		if err != nil {
			log.Printf("ERROR: Failed to get file URL for removebg: %v", err)
			return
		}
		h.triggerImageGeneration(user, message, "remove-background", "", imageURL)
		return
	}

	if state == "awaiting_image_for_upscaler" {
		if message.Photo == nil || len(message.Photo) == 0 {
			return
		}

		bestPhoto := message.Photo[len(message.Photo)-1]
		imageURL, err := h.getFileURL(bestPhoto.FileID)
		if err != nil {
			log.Printf("ERROR: Failed to get file URL for upscaler: %v", err)
			return
		}

		h.triggerImageGeneration(user, message, "recraft-upscaler", "", imageURL)
		return
	}


	if strings.HasPrefix(state, "prompt_for:") {
		// Format state baru: "prompt_for:model_id:style_id"
		parts := strings.Split(state, ":")
		if len(parts) < 3 { return } // Validasi format state
		
		modelID := parts[1]
		styleID := parts[2]

		var prompt, imageURL string
		if message.Photo != nil && len(message.Photo) > 0 {
			bestPhoto := message.Photo[len(message.Photo)-1]
			url, err := h.getFileURL(bestPhoto.FileID)
			if err != nil {
				log.Printf("ERROR: Failed to get file URL for photo: %v", err)
				return
			}
			imageURL = url
			prompt = message.Caption
		} else {
			prompt = message.Text
		}

		if prompt == "" {
			return
		}
		
		// Temukan suffix prompt dari style yang dipilih
		var promptSuffix string
		for _, style := range h.Styles {
			if style.ID == styleID {
				promptSuffix = style.PromptSuffix
				break
			}
		}
		
		finalPrompt := prompt + promptSuffix

		// Panggil triggerImageGeneration dengan prompt yang sudah dimodifikasi
		h.triggerImageGeneration(user, message, modelID, finalPrompt, imageURL)

	} else if strings.HasPrefix(state, "edit_setting:") {
		parts := strings.Split(strings.TrimPrefix(state, "edit_setting:"), ":")
		modelID, paramName := parts[0], parts[1]
		
		var selectedModel *config.Model
		var selectedParam *config.Parameter
		for _, m := range h.Models {
			if m.ID == modelID {
				selectedModel = &m
				for _, p := range m.Parameters {
					if p.Name == paramName {
						selectedParam = &p
						break
					}
				}
				break
			}
		}

		if selectedModel == nil || selectedParam == nil {
			return
		}
		
		var customSettings map[string]interface{}
		if user.CustomSettings != "" {
			json.Unmarshal([]byte(user.CustomSettings), &customSettings)
		} else {
			customSettings = make(map[string]interface{})
		}

		inputValue := message.Text
		var parsedValue interface{}
		var err error

		switch selectedParam.Type {
		case "integer":
			parsedValue, err = strconv.ParseInt(inputValue, 10, 64)
			if err == nil {
				val := parsedValue.(int64)
				if (selectedParam.Min != 0 || selectedParam.Max != 0) && (float64(val) < selectedParam.Min || float64(val) > selectedParam.Max) {
					err = fmt.Errorf("value out of range")
				}
			}
		case "number":
			parsedValue, err = strconv.ParseFloat(inputValue, 64)
			if err == nil {
				val := parsedValue.(float64)
				if (selectedParam.Min != 0 || selectedParam.Max != 0) && (val < selectedParam.Min || val > selectedParam.Max) {
					err = fmt.Errorf("value out of range")
				}
			}
		case "string":
			parsedValue = inputValue
		}

		if err != nil {
			errorText := fmt.Sprintf("Invalid input. Please provide a valid value for %s.", selectedParam.Label)
			msg := h.newReplyMessage(message, errorText)
			h.Bot.Send(msg)
			return
		}
		
		customSettings[paramName] = parsedValue

		settingsJSON, _ := json.Marshal(customSettings)
		user.CustomSettings = string(settingsJSON)
		h.DB.UpdateUser(user)

		h.userStatesMutex.Lock()
		delete(h.userStates, user.TelegramID)
		h.userStatesMutex.Unlock()

		keyboard, text := h.createAdvancedSettingsKeyboard(lang, selectedModel, user)
		msg := h.newReplyMessage(message, text)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)
		
		// Hapus pesan "Please enter a new value..."
		deleteMsg := tgbotapi.NewDeleteMessage(message.Chat.ID, message.MessageID - 1)
		h.Bot.Request(deleteMsg)
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
	if selectedModel == nil { return }
	
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
	var customParams map[string]interface{}

	if len(imageURLAndParams) > 0 {
		if url, ok := imageURLAndParams[0].(string); ok {
			finalImageURL = url
		}
	}
	if len(imageURLAndParams) > 1 {
		if params, ok := imageURLAndParams[1].(map[string]interface{}); ok {
			customParams = params
		}
	}

	if customParams == nil {
		if user.CustomSettings != "" {
			json.Unmarshal([]byte(user.CustomSettings), &customParams)
		} else {
			customParams = make(map[string]interface{})
		}
	}

	var aspectRatio string
	if selectedModel.ConfigurableAspectRatio {
		aspectRatio = user.AspectRatio
	}

	// --- AWAL PERUBAHAN LOGIKA BIAYA ---
	var numOutputs int
	// Periksa apakah model mendukung multiple outputs DAN pengguna tidak mengatur nilai custom
	if selectedModel.ConfigurableNumOutputs {
		// Jika ada pengaturan custom, gunakan itu
		if val, ok := customParams["num_outputs"]; ok {
			if num, ok := val.(float64); ok {
				numOutputs = int(num)
			} else if num, ok := val.(int64); ok {
				numOutputs = int(num)
			}
		} else {
			// Jika tidak, gunakan pengaturan dari profil pengguna
			numOutputs = user.NumOutputs
		}
	} else {
		// Jika model tidak mendukung, paksa jumlah output menjadi 1
		numOutputs = 1
	}

	// Pastikan numOutputs tidak pernah 0
	if numOutputs == 0 {
		numOutputs = 1
	}
	
	// Untuk model spesial, selalu atur numOutputs ke 1
	if modelID == "remove-background" || modelID == "recraft-upscaler" {
		numOutputs = 1
		delete(customParams, "num_outputs")
	}

	// Kalkulasi biaya menggunakan numOutputs yang sudah divalidasi
	totalCost := selectedModel.Cost * numOutputs
	// --- AKHIR PERUBAHAN LOGIKA BIAYA ---

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

	waitMsg := h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generating"))
	sentMsg, _ := h.Bot.Send(waitMsg)
	defer h.Bot.Send(tgbotapi.NewDeleteMessage(originalMessage.Chat.ID, sentMsg.MessageID))

	action := tgbotapi.NewChatAction(originalMessage.Chat.ID, tgbotapi.ChatUploadPhoto)
	h.Bot.Send(action)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Panggil Replicate dengan numOutputs yang sudah divalidasi
	imageUrls, err := h.Replicate.CreatePrediction(ctx, selectedModel.ReplicateID, prompt, finalImageURL, selectedModel.ImageParameterName, aspectRatio, numOutputs, customParams)

	if err != nil || len(imageUrls) == 0 {
		failMsg := h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generation_failed"))
		h.Bot.Send(failMsg)
		return
	}

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

	if user.CustomSettings != "" {
		user.CustomSettings = ""
	}

	if user.GeneratedImageCount == 2 && user.ReferrerID != 0 {
		referrer, errRef := h.DB.GetUserByTelegramID(user.ReferrerID)
		if errRef == nil && referrer != nil {
			referrer.PaidCredits += 5
			if errUpdate := h.DB.UpdateUser(referrer); errUpdate == nil {
				log.Printf("INFO: Awarded referral bonus to user %d", referrer.TelegramID)
				notificationText := h.Localizer.Get(referrer.LanguageCode, "referral_bonus_notification")
				msg := tgbotapi.NewMessage(referrer.TelegramID, notificationText)
				msg.ParseMode = "Markdown"
				h.Bot.Send(msg)
			}
		}
	}

	if modelID == "remove-background" || modelID == "recraft-upscaler" {
		resp, httpErr := http.Get(imageUrls[0])
		if httpErr != nil {
			log.Printf("ERROR: Failed to download processed file: %v", httpErr)
			h.Bot.Send(h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generation_failed")))
			return
		}
		defer resp.Body.Close()

		bytes, readErr := ioutil.ReadAll(resp.Body)
		if readErr != nil {
			log.Printf("ERROR: Failed to read processed file bytes: %v", readErr)
			h.Bot.Send(h.newReplyMessage(originalMessage, h.Localizer.Get(lang, "generation_failed")))
			return
		}

		var fileName, successMessage string
		if modelID == "remove-background" {
			fileName = "background-removed.png"
			successMessage = h.Localizer.Get(lang, "removebg_success")
		} else {
			fileName = "upscaled-image.jpg"
			successMessage = h.Localizer.Get(lang, "upscaler_success")
		}

		fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: bytes}
		doc := h.newReplyDocument(originalMessage, fileBytes)
		doc.Caption = successMessage
		h.Bot.Send(doc)
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

		backCallback := fmt.Sprintf("adv_setting_open:%s", modelID)
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
		
		cancelKeyboard := h.createCancelFlowKeyboard(lang)
		msg.ReplyMarkup = &cancelKeyboard
		
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

	// Langsung cari parameter yang relevan tanpa menyimpan model secara terpisah
	var targetParamType string
	found := false
	for _, model := range h.Models {
		if model.ID == modelID {
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

	if !found {
		log.Printf("WARN: Parameter %s for model %s not found in handleSetOption", paramName, modelID)
		return
	}
	
	// Konversi nilai berdasarkan tipe data yang ditemukan
	var parsedValue interface{}
	switch targetParamType {
	case "integer":
		parsedValue, _ = strconv.ParseInt(optionValue, 10, 64)
	case "number":
		parsedValue, _ = strconv.ParseFloat(optionValue, 64)
	default: // string
		parsedValue = optionValue
	}

	customSettings[paramName] = parsedValue
	settingsJSON, _ := json.Marshal(customSettings)
	user.CustomSettings = string(settingsJSON)
	h.DB.UpdateUser(user)

	// Panggil kembali menu advanced settings
	h.handleOpenAdvancedSettings(callback, modelID)
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



