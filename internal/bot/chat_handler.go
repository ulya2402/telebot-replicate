package bot

import (
	"context"
	"fmt"
	"html"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"telegram-ai-bot/internal/database"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// --- MEMORY PENYIMPANAN RIWAYAT CHAT ---
// Kita gunakan map sederhana di level paket untuk menyimpan history sementara.
// Key: UserID, Value: Slice of strings (urutan percakapan)
var (
	chatHistory      = make(map[int64][]string)
	chatHistoryMutex sync.Mutex
)

// Konfigurasi History
const maxHistoryItems = 12 // Simpan 6 percakapan terakhir (User + Bot)

// 1. Memulai Chat Mode
func (h *Handler) handleChatModeStart(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode

	// Set State ke "chat_mode"
	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "chat_mode"
	h.userStatesMutex.Unlock()

	// Reset History User Ini (Mulai sesi baru bersih)
	h.clearChatHistory(user.TelegramID)

	text := h.Localizer.Get(lang, "chat_mode_welcome")
	
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"
	
	// Tampilkan Keyboard Bawah (Reply Keyboard)
	keyboard := h.createChatModeKeyboard(lang)
	msg.ReplyMarkup = keyboard

	h.Bot.Send(msg)
}

// 2. Menangani Pesan Masuk saat di Chat Mode
func (h *Handler) handleChatMessage(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode
	userID := user.TelegramID
	
	// Cek Command Stop (Text atau Command)
	stopCmd := h.Localizer.Get(lang, "chat_mode_stop_btn")
	if message.Text == stopCmd || message.Text == "/exit" || message.Text == "/start" {
		h.handleChatModeStop(message)
		return
	}

	// Cek Saldo (Biaya: 1 Kredit)
	cost := 1
	totalCredits := user.PaidCredits + user.FreeCredits
	if totalCredits < cost {
		args := map[string]string{
			"required": strconv.Itoa(cost),
			"balance":  strconv.Itoa(totalCredits),
		}
		failMsg := h.newReplyMessage(message, h.Localizer.Getf(lang, "insufficient_credits", args))
		h.Bot.Send(failMsg)
		return
	}

	// Kirim "Typing..." action
	action := tgbotapi.NewChatAction(message.Chat.ID, tgbotapi.ChatTyping)
	h.Bot.Send(action)

	// --- PERSIAPAN INPUT GEMINI ---
	replicateModelPath := "google/gemini-2.5-flash"
	var prompt string
	var imageURL string
	var currentInputText string

	if message.Photo != nil && len(message.Photo) > 0 {
		// Jika kirim gambar (Vision) -> History sementara diabaikan/reset untuk fokus ke gambar
		prompt = message.Caption
		if prompt == "" {
			prompt = "Describe this image in detail and answer any user questions about it."
		}
		currentInputText = "[User sent an image] " + prompt

		bestPhoto := message.Photo[len(message.Photo)-1]
		url, err := h.getFileURL(bestPhoto.FileID)
		if err != nil {
			h.Bot.Send(h.newReplyMessage(message, "❌ Failed to process image."))
			return
		}
		imageURL = url
	} else {
		// Jika teks biasa
		currentInputText = message.Text
		if currentInputText == "" { return }
		
		// RAKIT CONTEXT DARI HISTORY
		history := h.getChatHistory(userID)
		
		// System Prompt agar bot punya kepribadian
		systemPrompt := "System: You are a helpful, smart, and friendly AI assistant inside a Telegram Bot. " +
			"Use Emoji. Keep answers concise but informative. Format output in Markdown."
		
		// Gabungkan: System + History + Input Baru
		var sb strings.Builder
		sb.WriteString(systemPrompt + "\n\n")
		for _, entry := range history {
			sb.WriteString(entry + "\n")
		}
		sb.WriteString("User: " + currentInputText)
		
		prompt = sb.String()
	}

	// Proses di Background
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		var resultText string
		var err error

		// Panggil Gemini (Vision atau Text)
		if imageURL != "" {
			resultText, err = h.Replicate.CreateVisionCompletion(ctx, replicateModelPath, prompt, imageURL, 1024)
		} else {
			// Text Completion
			// Kita kirim prompt lengkap (history) sebagai prompt utama
			// SysPrompt kosong karena sudah digabung di prompt utama
			resultText, err = h.Replicate.CreateTextCompletion(ctx, replicateModelPath, prompt, "", 0.7, 1024, 0)
		}

		if err != nil {
			log.Printf("ERROR Gemini Chat: %v", err)
			h.Bot.Send(h.newReplyMessage(message, "❌ AI is busy/overloaded. Try again."))
			return
		}

		// SIMPAN KE HISTORY (Hanya jika teks, bukan gambar)
		if imageURL == "" {
			h.appendChatHistory(userID, "User: "+currentInputText)
			h.appendChatHistory(userID, "Model: "+resultText)
		}

		// Potong Saldo
		if h.deductUserCredit(user, cost) {
			// --- FORMATTING HTML (Markdown Fix) ---
			formattedText := h.formatChatMarkdownToHTML(resultText)

			// Info biaya
			costInfo := fmt.Sprintf(h.Localizer.Get(lang, "chat_mode_reply_cost"), cost)
			finalMsg := formattedText + costInfo

			reply := h.newReplyMessage(message, finalMsg)
			reply.ParseMode = "HTML" // Gunakan HTML agar stabil
			
			// --- TAMBAHAN: TOMBOL STOP DI SETIAP PESAN ---
			// Jika keyboard bawah tidak muncul, user bisa klik ini
			stopBtn := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("❌ Stop Chat", "stop_chat_mode"),
				),
			)
			reply.ReplyMarkup = stopBtn

			h.Bot.Send(reply)
		}
	}()
}

// 3. Menghentikan Chat Mode
func (h *Handler) handleChatModeStop(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode
	userID := user.TelegramID

	// Hapus State
	h.userStatesMutex.Lock()
	delete(h.userStates, userID)
	h.userStatesMutex.Unlock()

	// Bersihkan History
	h.clearChatHistory(userID)

	text := h.Localizer.Get(lang, "chat_mode_stopped")
	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"
	
	// Hapus Keyboard Bawah
	msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
	
	h.Bot.Send(msg)

	// Tampilkan Menu Utama lagi
	h.handleStart(message)
}

// --- HELPER FUNCTIONS ---

func (h *Handler) deductUserCredit(user *database.User, cost int) bool {
	u, _ := h.DB.GetUserByTelegramID(user.TelegramID)
	costLeft := cost
	if u.FreeCredits > 0 {
		if u.FreeCredits >= costLeft {
			u.FreeCredits -= costLeft
			costLeft = 0
		} else {
			costLeft -= u.FreeCredits
			u.FreeCredits = 0
		}
	}
	if costLeft > 0 {
		if u.PaidCredits < costLeft { return false }
		u.PaidCredits -= costLeft
	}
	h.DB.UpdateUser(u)
	return true
}

// Helper: Format Markdown Gemini (Bold **) ke HTML Telegram (<b>)
func (h *Handler) formatChatMarkdownToHTML(text string) string {
	// 1. Sanitize input agar tidak merusak HTML (escape < > &)
	clean := html.EscapeString(text)

	// 2. Convert Code Blocks (```) -> <pre>
	reCodeBlock := regexp.MustCompile("```(?:\\w*\\n)?([\\s\\S]*?)```")
	clean = reCodeBlock.ReplaceAllString(clean, "<pre>$1</pre>")

	// 3. Convert Inline Code (`) -> <code>
	reInlineCode := regexp.MustCompile("`([^`]+)`")
	clean = reInlineCode.ReplaceAllString(clean, "<code>$1</code>")

	// 4. Convert Bold (**) -> <b>
	reBold := regexp.MustCompile(`\*\*([^*]+)\*\*`)
	clean = reBold.ReplaceAllString(clean, "<b>$1</b>")

	// 5. Convert Italic (*) -> <i> (Optional, kadang Gemini pakai *)
	// Hati-hati regex ini bisa agresif, kita batasi ke yang jelas pasangannya
	reItalic := regexp.MustCompile(`\*([^*]+)\*`)
	clean = reItalic.ReplaceAllString(clean, "<i>$1</i>")
	
	// 6. Convert Bullet Points (* di awal baris) -> Bullet standar
	clean = strings.ReplaceAll(clean, "\n* ", "\n• ")

	return clean
}

// --- HISTORY MANAGER ---

func (h *Handler) appendChatHistory(userID int64, text string) {
	chatHistoryMutex.Lock()
	defer chatHistoryMutex.Unlock()

	history, exists := chatHistory[userID]
	if !exists {
		history = []string{}
	}

	history = append(history, text)
	
	// Jaga agar tidak terlalu panjang (FIFO)
	if len(history) > maxHistoryItems {
		history = history[len(history)-maxHistoryItems:]
	}
	
	chatHistory[userID] = history
}

func (h *Handler) getChatHistory(userID int64) []string {
	chatHistoryMutex.Lock()
	defer chatHistoryMutex.Unlock()
	
	if history, ok := chatHistory[userID]; ok {
		// Return copy agar aman
		result := make([]string, len(history))
		copy(result, history)
		return result
	}
	return []string{}
}

func (h *Handler) clearChatHistory(userID int64) {
	chatHistoryMutex.Lock()
	defer chatHistoryMutex.Unlock()
	delete(chatHistory, userID)
}