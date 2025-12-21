package bot

import (
	"context"
	"fmt"
	"html"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"telegram-ai-bot/internal/database" // <-- PENTING: Import database ditambahkan

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ---------------------------------------------------------
// 1. MENU UTAMA ASISTEN (DASHBOARD)
// ---------------------------------------------------------

// [PERBAIKAN] Fungsi Wrapper agar command /prompt di handlers.go tidak error
func (h *Handler) handlePromptCommand(message *tgbotapi.Message) {
	// Arahkan langsung ke menu utama prompt assistant
	h.handlePromptMenu(message)
}

// Dipanggil saat klik "📝 Prompt Assistant" di Main Menu
func (h *Handler) handlePromptMenu(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode

	// --- PERBAIKAN: SET STATE AGAR TOMBOL CANCEL TAHU KITA DI SINI ---
	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "awaiting_prompt_menu"
	h.userStatesMutex.Unlock()
	// -----------------------------------------------------------------

	text := h.Localizer.Get(lang, "prompt_menu_title")

	msg := h.newReplyMessage(message, text)
	msg.ParseMode = "HTML"
	keyboard := h.createPromptMainMenuKeyboard(lang)
	msg.ReplyMarkup = &keyboard

	h.Bot.Send(msg)
}

// Menangani pilihan tombol: "Text Idea" vs "Image to Prompt"
func (h *Handler) handlePromptModeSelection(callback *tgbotapi.CallbackQuery, mode string) {
	user, _ := h.getOrCreateUser(callback.From)
	lang := user.LanguageCode

	// Hapus pesan menu sebelumnya agar bersih
	h.Bot.Request(tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID))

	h.userStatesMutex.Lock()
	defer h.userStatesMutex.Unlock()

	if mode == "text" {
		// --- MODE 1: TEXT GENERATOR (Logika Lama) ---
		h.userStates[user.TelegramID] = "awaiting_prompt_idea"
		
		textTitle := h.Localizer.Get(lang, "prompt_gen_title")
		textInstruction := h.Localizer.Get(lang, "prompt_gen_instruction")
		fullText := fmt.Sprintf("%s\n\n%s", textTitle, textInstruction)

		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, fullText)
		msg.ParseMode = "HTML"
		keyboard := h.createCancelFlowKeyboard(lang)
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)

	} else if mode == "image" {
		// --- MODE 2: IMAGE TO PROMPT (Logika Baru) ---
		h.userStates[user.TelegramID] = "awaiting_prompt_image"

		text := h.Localizer.Get(lang, "prompt_image_instruction")
		
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, text)
		msg.ParseMode = "HTML"
		keyboard := h.createCancelFlowKeyboard(lang)
		msg.ReplyMarkup = &keyboard
		h.Bot.Send(msg)
	}
}

// ---------------------------------------------------------
// 2. LOGIKA TEXT-TO-PROMPT (FITUR LAMA)
// ---------------------------------------------------------

func (h *Handler) handlePromptIdeaInput(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode
	ideaText := message.Text

	h.userStatesMutex.Lock()
	h.userStates[user.TelegramID] = "awaiting_prompt_method"
	h.pendingGenerations[user.TelegramID] = &PendingGeneration{
		Prompt: ideaText,
	}
	h.userStatesMutex.Unlock()

	safeIdea := html.EscapeString(ideaText)
	baseText := h.Localizer.Getf(lang, "prompt_idea_received", map[string]string{})
	finalText := fmt.Sprintf(strings.Replace(baseText, "%s", "%s", 1), safeIdea)

	msg := h.newReplyMessage(message, finalText)
	msg.ParseMode = "HTML"
	keyboard := h.createPromptMethodKeyboard(lang)
	msg.ReplyMarkup = &keyboard

	h.Bot.Send(msg)
}

func (h *Handler) handlePromptMethodCallback(callback *tgbotapi.CallbackQuery, method string) {
	user, _ := h.getOrCreateUser(callback.From)
	lang := user.LanguageCode

	pending, ok := h.pendingGenerations[user.TelegramID]
	if !ok || pending.Prompt == "" {
		h.Bot.Send(tgbotapi.NewMessage(callback.Message.Chat.ID, "❌ Session expired."))
		return
	}
	userIdea := pending.Prompt

	if !h.checkAndDeductCredits(user, callback.Message.Chat.ID, lang, 2) {
		return
	}

	h.Bot.Request(tgbotapi.NewDeleteMessage(callback.Message.Chat.ID, callback.Message.MessageID))
	action := tgbotapi.NewChatAction(callback.Message.Chat.ID, tgbotapi.ChatTyping)
	h.Bot.Send(action)

	go func() {
		// Panggil Logic Text Generator
		success := h.processTextToPrompt(user.TelegramID, callback.Message.Chat.ID, userIdea, method, lang)
		h.finalizePromptProcess(user, success, 2)
	}()
}

// ---------------------------------------------------------
// 3. LOGIKA IMAGE-TO-PROMPT (FITUR BARU)
// ---------------------------------------------------------

func (h *Handler) handlePromptImageInput(message *tgbotapi.Message) {
	user, _ := h.getOrCreateUser(message.From)
	lang := user.LanguageCode

	// 1. Ambil File ID Foto Terbesar
	photo := message.Photo[len(message.Photo)-1]
	imageURL, err := h.getFileURL(photo.FileID)
	if err != nil {
		h.Bot.Send(tgbotapi.NewMessage(message.Chat.ID, "❌ Failed to get image URL."))
		return
	}

	// 2. Cek Kredit (Biaya: 2)
	if !h.checkAndDeductCredits(user, message.Chat.ID, lang, 2) {
		return
	}

	// 3. Kirim Status
	statusText := h.Localizer.Get(lang, "prompt_image_received")
	msg := h.newReplyMessage(message, statusText)
	msg.ParseMode = "HTML"
	h.Bot.Send(msg)

	action := tgbotapi.NewChatAction(message.Chat.ID, tgbotapi.ChatTyping)
	h.Bot.Send(action)

	// 4. Proses Background
	go func() {
		success := h.processImageToPrompt(message.Chat.ID, imageURL, lang)
		h.finalizePromptProcess(user, success, 2)
	}()
}

func (h *Handler) processImageToPrompt(chatID int64, imageURL, lang string) bool {
	replicateModelPath := "google/gemini-2.5-flash"
	
	prompt := "Describe this image as a highly detailed text-to-image prompt (English). " +
		"Focus on subject, artistic style, lighting, camera angle, and colors. " +
		"CRITICAL: Output ONLY the prompt inside a Markdown code block (```). Do not add conversational text."

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Panggil fungsi VISION baru di replicate.go
	// Menggunakan maxOutputTokens: 2048
	resultText, err := h.Replicate.CreateVisionCompletion(ctx, replicateModelPath, prompt, imageURL, 2048)
	
	if err != nil {
		log.Printf("ERROR Gemini Vision: %v", err)
		failText := h.Localizer.Get(lang, "generation_failed")
		h.Bot.Send(tgbotapi.NewMessage(chatID, "❌ "+failText))
		return false
	}

	// Format Output
	htmlResult := h.formatMarkdownToHTML(resultText)
	
	title := "🖼️ <b>Image Decoded (Reverse Prompt)</b>"
	costInfo := fmt.Sprintf(h.Localizer.Get(lang, "prompt_gen_cost_info"), 2)

	finalResponse := fmt.Sprintf("%s\n%s\n\n%s", title, costInfo, htmlResult)

	msg := tgbotapi.NewMessage(chatID, finalResponse)
	msg.ParseMode = "HTML"
	h.Bot.Send(msg)

	return true
}

// ---------------------------------------------------------
// 4. FUNGSI PEMBANTU (HELPER)
// ---------------------------------------------------------

// Helper: Proses Text-to-Prompt (Gemini Text)
func (h *Handler) processTextToPrompt(userID, chatID int64, idea, method, lang string) bool {
	replicateModelPath := "google/gemini-2.5-flash"

	baseInstruction := "You are an expert AI Prompt Engineer. Convert user idea into TWO professional prompts. " +
		"CRITICAL: Enclose each prompt in a Markdown code block (```)."
	
	var specificInstruction string
	switch method {
	case "zero_shot": specificInstruction = "Style: Direct, concise."
	case "role": specificInstruction = "Style: National Geographic / Concept Artist."
	case "permutation": specificInstruction = "Style: Mix conflicting styles."
	case "step": specificInstruction = "Style: Structured (Subject, Light, Camera)."
	case "json": specificInstruction = "Output Format: JSON."
	}

	systemInstruction := fmt.Sprintf("%s %s\n\nOutput:\n**Var 1:**\n```...```\n\n**Var 2:**\n```...```", baseInstruction, specificInstruction)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Parameter: temperature=0.8, maxOutputTokens=2048, thinkingBudget=0
	resultText, err := h.Replicate.CreateTextCompletion(ctx, replicateModelPath, idea, systemInstruction, 0.8, 2048)
	if err != nil {
		failText := h.Localizer.Get(lang, "generation_failed")
		h.Bot.Send(tgbotapi.NewMessage(chatID, "❌ "+failText))
		return false
	}

	htmlResult := h.formatMarkdownToHTML(resultText)
	titleBase := h.Localizer.Getf(lang, "prompt_gen_result_title", map[string]string{})
	titleFormatted := fmt.Sprintf(strings.Replace(titleBase, "%s", "%s", 1), strings.ToUpper(method))
	costInfo := fmt.Sprintf(h.Localizer.Get(lang, "prompt_gen_cost_info"), 2)
	safeIdea := html.EscapeString(idea)

	finalResponse := fmt.Sprintf("%s\nIdea: <i>%s</i>\n%s\n\n%s", titleFormatted, safeIdea, costInfo, htmlResult)
	msg := tgbotapi.NewMessage(chatID, finalResponse)
	msg.ParseMode = "HTML"
	h.Bot.Send(msg)

	return true
}

// Helper: Cek Saldo & Potong (Sementara di memory, commit jika sukses)
func (h *Handler) checkAndDeductCredits(user *database.User, chatID int64, lang string, cost int) bool {
	totalCredits := user.PaidCredits + user.FreeCredits
	if totalCredits < cost {
		args := map[string]string{
			"required": strconv.Itoa(cost),
			"balance":  strconv.Itoa(totalCredits),
		}
		failMsg := tgbotapi.NewMessage(chatID, h.Localizer.Getf(lang, "insufficient_credits", args))
		h.Bot.Send(failMsg)
		return false
	}
	return true
}

// Helper: Commit pengurangan kredit & bersihkan state
func (h *Handler) finalizePromptProcess(user *database.User, success bool, cost int) {
	if success {
		// Refresh user dari DB
		u, _ := h.DB.GetUserByTelegramID(user.TelegramID)
		if u != nil {
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
				u.PaidCredits -= costLeft
			}
			h.DB.UpdateUser(u)
		}
	}

	h.userStatesMutex.Lock()
	delete(h.userStates, user.TelegramID)
	delete(h.pendingGenerations, user.TelegramID)
	h.userStatesMutex.Unlock()
}

// Helper: Convert Markdown to HTML Telegram
func (h *Handler) formatMarkdownToHTML(text string) string {
	clean := html.EscapeString(text)
	
	// Convert Code Blocks
	reCodeBlock := regexp.MustCompile("```(?:\\w*\\n)?([\\s\\S]*?)```")
	clean = reCodeBlock.ReplaceAllString(clean, "<pre>$1</pre>")

	// Convert Bold
	reBold := regexp.MustCompile(`\*\*(.*?)\*\*`)
	clean = reBold.ReplaceAllString(clean, "<b>$1</b>")

	return clean
}