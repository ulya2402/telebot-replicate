package bot

import (
	"context"
	"fmt"
	"html"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"telegram-ai-bot/internal/database"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var (
	chatHistory      = make(map[int64][]string)
	chatHistoryMutex sync.Mutex
)
const maxHistoryItems = 12

// ---------------------------------------------------------
// 1. TAHAP PEMILIHAN MODEL
// ---------------------------------------------------------

func (h *Handler) handleChatModelSelectionMenu(message *tgbotapi.Message) {
	h.userStatesMutex.Lock()
	h.userStates[message.From.ID] = "awaiting_chat_model_selection"
	h.userStatesMutex.Unlock()

	text := "🧠 <b>Select AI Model</b>\n\nChoose which AI brain you want to talk to:"
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"
	keyboard := h.createChatModelKeyboard()
	msg.ReplyMarkup = &keyboard
	h.Bot.Send(msg)
}

// ---------------------------------------------------------
// 2. MEMULAI SESI CHAT
// ---------------------------------------------------------

func (h *Handler) handleChatModeStart(message *tgbotapi.Message, modelID string) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode

	h.Bot.Request(tgbotapi.NewDeleteMessage(message.Chat.ID, message.MessageID))

	// LOGGING
	log.Printf("DEBUG: Starting Chat Mode for User %d with Model: %s", user.TelegramID, modelID)

	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "chat_mode:" + modelID
	h.userStatesMutex.Unlock()

	h.clearChatHistory(user.TelegramID)

	modelName := extractModelName(modelID)
	text := fmt.Sprintf("🟢 <b>Chat Mode Active!</b>\n\n🧠 Brain: <code>%s</code>\nCredits: 1 per reply.\n\nSend text or photo to start.", modelName)
	
	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ParseMode = "HTML"
	keyboard := h.createChatModeKeyboard(lang)
	msg.ReplyMarkup = keyboard

	h.Bot.Send(msg)
}

// ---------------------------------------------------------
// 3. LOGIKA UTAMA CHAT (HYBRID)
// ---------------------------------------------------------

func (h *Handler) handleChatMessage(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode
	userID := user.TelegramID

	cost := 1
	
	// LOGGING: Cek pesan masuk
	log.Printf("DEBUG: Chat Message Received from %d: %s", userID, message.Text)

	resetCmd := h.Localizer.Get(lang, "chat_mode_reset_btn")
	if message.Text == resetCmd || message.Text == "/reset" {
		h.handleChatModeReset(message)
		return
	}

	stopCmd := h.Localizer.Get(lang, "chat_mode_stop_btn")
	if message.Text == stopCmd || message.Text == "/exit" || message.Text == "/start" {
		h.handleChatModeStop(message)
		return
	}

	// Ambil State & Model
	h.userStatesMutex.Lock()
	state := h.userStates[userID]
	h.userStatesMutex.Unlock()

	// FIX LOGIC: Parsing Model ID
	var selectedModel string
	if strings.HasPrefix(state, "chat_mode:") {
		selectedModel = strings.TrimPrefix(state, "chat_mode:")
	} else {
		// Jika state cuma "chat_mode" (legacy state), paksa default
		selectedModel = "google/gemini-2.5-flash"
	}
	
	// FIX LOGIC: Jika kosong, paksa default lagi
	if selectedModel == "" {
		selectedModel = "google/gemini-2.5-flash"
	}

	log.Printf("DEBUG: Selected Model is: %s", selectedModel)

	h.Bot.Send(tgbotapi.NewChatAction(message.Chat.ID, tgbotapi.ChatTyping))

	// Cek Kredit (Tanpa potong dulu)
	if !h.deductUserCredit(user, 0) {
		log.Printf("DEBUG: Insufficient Credits for user %d", userID)
		args := map[string]string{"required": "1", "balance": fmt.Sprintf("%d", user.PaidCredits+user.FreeCredits)}
		failMsg := h.newReplyMessage(message, h.Localizer.Getf(lang, "insufficient_credits", args))
		h.Bot.Send(failMsg)
		return
	}

	var finalModelID string
	var prompt string
	var imageURL string
	var currentInputText string

	if message.Photo != nil && len(message.Photo) > 0 {
		log.Printf("DEBUG: Input is PHOTO")
		finalModelID = "google/gemini-2.5-flash"
		prompt = message.Caption
		if prompt == "" { prompt = "Describe this image in detail." }
		currentInputText = "[Image] " + prompt
		bestPhoto := message.Photo[len(message.Photo)-1]
		url, err := h.getFileURL(bestPhoto.FileID)
		if err != nil {
			h.Bot.Send(h.newReplyMessage(message, "❌ Failed to process image."))
			return
		}
		imageURL = url
	} else {
		log.Printf("DEBUG: Input is TEXT")
		finalModelID = selectedModel
		currentInputText = message.Text
		if currentInputText == "" { return }

		history := h.getChatHistory(userID)
		var sb strings.Builder
		sb.WriteString("System: You are a helpful AI assistant. Format output in Markdown.\n\n")
		for _, entry := range history {
			sb.WriteString(entry + "\n")
		}
		sb.WriteString("User: " + currentInputText)
		prompt = sb.String()
	}

	// Eksekusi Background
	go func() {
		log.Printf("DEBUG: Launching Goroutine for Replicate...")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		var resultText string
		var err error

		if imageURL != "" {
			resultText, err = h.Replicate.CreateVisionCompletion(ctx, finalModelID, prompt, imageURL, 1024)
		} else {
			// LOGGING SEBELUM CALL REPLICATE
			log.Printf("DEBUG: Calling CreateTextCompletion with Model: %s", finalModelID)
			
			// Pastikan replicate.go menerima 6 argumen!
			resultText, err = h.Replicate.CreateTextCompletion(ctx, finalModelID, prompt, "", 0.7, 1024)
		}

		if err != nil {
			log.Printf("ERROR calling Replicate API: %v", err) // <--- INI PENTING DILIHAT
			h.Bot.Send(h.newReplyMessage(message, "❌ AI failed to respond. Try again."))
			return
		}

		log.Printf("DEBUG: Replicate Success! Response len: %d", len(resultText))

		if imageURL == "" {
			h.appendChatHistory(userID, "User: "+currentInputText)
			h.appendChatHistory(userID, "Model: "+resultText)
		}

		if h.deductUserCredit(user, cost) {
			formattedText := h.formatChatMarkdownToHTML(resultText)
			//header := fmt.Sprintf("🤖 <i>%s</i>\n\n", extractModelName(finalModelID))
			costInfo := fmt.Sprintf(h.Localizer.Get(lang, "chat_mode_reply_cost"), cost)
			finalMsg := formattedText + costInfo

			reply := h.newReplyMessage(message, finalMsg)
			reply.ParseMode = "HTML"
			
			//stopBtn := tgbotapi.NewInlineKeyboardMarkup(
				//tgbotapi.NewInlineKeyboardRow(
					//tgbotapi.NewInlineKeyboardButtonData("❌ Stop Chat", "stop_chat_mode"),
				//),
			//)
			//reply.ReplyMarkup = stopBtn

			sent, errSend := h.Bot.Send(reply)
			if errSend != nil {
				log.Printf("ERROR Sending Telegram Msg: %v", errSend)
				// Fallback jika HTML error (misal ada tag aneh dari AI)
				reply.ParseMode = ""
				h.Bot.Send(reply)
			} else {
				log.Printf("DEBUG: Message Sent ID: %d", sent.MessageID)
			}
		}
	}()
}

// ---------------------------------------------------------
// HELPER & UTILS
// ---------------------------------------------------------

func (h *Handler) handleChatModeStop(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	userID := user.TelegramID

	h.userStatesMutex.Lock()
	delete(h.userStates, userID)
	h.userStatesMutex.Unlock()

	h.clearChatHistory(userID)

	msg := h.newReplyMessage(message, "🔴 <b>Chat Mode Ended.</b>")
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
	h.Bot.Send(msg)
	h.handleStart(message)
}

func (h *Handler) deductUserCredit(user *database.User, cost int) bool {
	if cost == 0 { return true }
	u, _ := h.DB.GetUserByTelegramID(user.TelegramID)
	
	// Pastikan saldo cukup
	total := u.PaidCredits + u.FreeCredits
	if total < cost { return false }
	
	if u.FreeCredits >= cost {
		u.FreeCredits -= cost
	} else {
		rem := cost - u.FreeCredits
		u.FreeCredits = 0
		u.PaidCredits -= rem
	}
	h.DB.UpdateUser(u)
	return true
}

func extractModelName(replicateID string) string {
	parts := strings.Split(replicateID, "/")
	if len(parts) > 1 {
		return parts[1]
	}
	return replicateID
}

func (h *Handler) formatChatMarkdownToHTML(text string) string {
	clean := html.EscapeString(text)
	reCodeBlock := regexp.MustCompile("```(?:\\w*\\n)?([\\s\\S]*?)```")
	clean = reCodeBlock.ReplaceAllString(clean, "<pre>$1</pre>")
	reInlineCode := regexp.MustCompile("`([^`]+)`")
	clean = reInlineCode.ReplaceAllString(clean, "<code>$1</code>")
	reBold := regexp.MustCompile(`\*\*([^*]+)\*\*`)
	clean = reBold.ReplaceAllString(clean, "<b>$1</b>")
	reItalic := regexp.MustCompile(`\*([^*]+)\*`)
	clean = reItalic.ReplaceAllString(clean, "<i>$1</i>")
	return clean
}

func (h *Handler) appendChatHistory(userID int64, text string) {
	chatHistoryMutex.Lock()
	defer chatHistoryMutex.Unlock()
	history, _ := chatHistory[userID]
	history = append(history, text)
	if len(history) > maxHistoryItems {
		history = history[len(history)-maxHistoryItems:]
	}
	chatHistory[userID] = history
}

func (h *Handler) getChatHistory(userID int64) []string {
	chatHistoryMutex.Lock()
	defer chatHistoryMutex.Unlock()
	if history, ok := chatHistory[userID]; ok {
		res := make([]string, len(history))
		copy(res, history)
		return res
	}
	return []string{}
}

func (h *Handler) clearChatHistory(userID int64) {
	chatHistoryMutex.Lock()
	defer chatHistoryMutex.Unlock()
	delete(chatHistory, userID)
}

// [BARU] Fungsi Menghapus Ingatan
func (h *Handler) handleChatModeReset(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode
	userID := user.TelegramID

	// Hapus history user ini dari memori
	h.clearChatHistory(userID)

	// Kirim pesan konfirmasi
	text := h.Localizer.Get(lang, "chat_mode_reset_done")
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"
	h.Bot.Send(msg)
}