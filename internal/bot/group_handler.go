package bot

import (
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// GroupHandler menangani logika untuk pesan grup
type GroupHandler struct {
	mainHandler *Handler
}

// NewGroupHandler membuat instance baru dari GroupHandler
func NewGroupHandler(handler *Handler) *GroupHandler {
	return &GroupHandler{mainHandler: handler}
}

// HandleGroupMessage adalah titik masuk untuk memproses pesan dari grup
func (gh *GroupHandler) HandleGroupMessage(message *tgbotapi.Message) {
	// Tentukan apakah pesan ini ditujukan untuk bot.
	// Bot akan merespons jika pesan me-mention @username_bot atau membalas pesan bot.
	isReply := message.ReplyToMessage != nil && message.ReplyToMessage.From.ID == gh.mainHandler.Bot.Self.ID
	isMention := false
	mentionText := "@" + gh.mainHandler.Bot.Self.UserName

	// Cek mention di teks atau caption
	var rawText string
	if message.Text != "" {
		rawText = message.Text
	} else if message.Caption != "" {
		rawText = message.Caption
	}

	if strings.Contains(rawText, mentionText) {
		isMention = true
	}

	// Jika bukan untuk bot, abaikan
	if !isReply && !isMention {
		return
	}

	// Cek apakah pengguna sedang dalam alur perintah (misal: menunggu input prompt)
	gh.mainHandler.userStatesMutex.Lock()
	_, hasState := gh.mainHandler.userStates[message.From.ID]
	gh.mainHandler.userStatesMutex.Unlock()

	if hasState {
		// Jika ya, teruskan ke message handler biasa untuk diproses sebagai input
		gh.mainHandler.handleMessage(message)
		return
	}

	// Jika pesan adalah perintah (misal: /img, /profile), proses perintahnya
	if message.IsCommand() {
		gh.mainHandler.handleCommand(message)
	}
}