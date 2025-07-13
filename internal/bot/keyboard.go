package bot

import (
	"fmt"
	"strconv"
	"telegram-ai-bot/internal/config"
	

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const itemsPerPage = 6

func (h *Handler) createModelSelectionKeyboard(models []config.Model, lang string, page int) tgbotapi.InlineKeyboardMarkup {
	var keyboard [][]tgbotapi.InlineKeyboardButton

	start := page * itemsPerPage
	end := start + itemsPerPage
	if end > len(models) {
		end = len(models)
	}

	if start >= len(models) {
		return tgbotapi.NewInlineKeyboardMarkup()
	}

	paginatedModels := models[start:end]

	var row []tgbotapi.InlineKeyboardButton
	for i, model := range paginatedModels {
		buttonText := fmt.Sprintf("%s (%d 💎)", model.Name, model.Cost)
		callbackData := fmt.Sprintf("model_select:%s", model.ID)
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(buttonText, callbackData))

		if (i+1)%2 == 0 || i == len(paginatedModels)-1 {
			keyboard = append(keyboard, row)
			row = []tgbotapi.InlineKeyboardButton{}
		}
	}

	var navRow []tgbotapi.InlineKeyboardButton
	if page > 0 {
		prevText := h.Localizer.Get(lang, "prev_button")
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(prevText, "model_page:"+strconv.Itoa(page-1)))
	}
	if end < len(models) {
		nextText := h.Localizer.Get(lang, "next_button")
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(nextText, "model_page:"+strconv.Itoa(page+1)))
	}

	if len(navRow) > 0 {
		keyboard = append(keyboard, navRow)
	}

	return tgbotapi.NewInlineKeyboardMarkup(keyboard...)
}

func (h *Handler) createTemplateSelectionKeyboard(templates []config.PromptTemplate, lang string, page int) tgbotapi.InlineKeyboardMarkup {
	var keyboard [][]tgbotapi.InlineKeyboardButton

	start := page * itemsPerPage
	end := start + itemsPerPage
	if end > len(templates) {
		end = len(templates)
	}

	if start >= len(templates) {
		return tgbotapi.NewInlineKeyboardMarkup()
	}

	paginatedTemplates := templates[start:end]

	for _, template := range paginatedTemplates {
		button := tgbotapi.NewInlineKeyboardButtonData(template.Title, "template_select:"+template.ID)
		keyboard = append(keyboard, tgbotapi.NewInlineKeyboardRow(button))
	}

	var navRow []tgbotapi.InlineKeyboardButton
	if page > 0 {
		prevText := h.Localizer.Get(lang, "prev_button")
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(prevText, "template_page:"+strconv.Itoa(page-1)))
	}
	if end < len(templates) {
		nextText := h.Localizer.Get(lang, "next_button")
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(nextText, "template_page:"+strconv.Itoa(page+1)))
	}

	if len(navRow) > 0 {
		keyboard = append(keyboard, navRow)
	}

	return tgbotapi.NewInlineKeyboardMarkup(keyboard...)
}

func (h *Handler) createLanguageSelectionKeyboard() tgbotapi.InlineKeyboardMarkup {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("English 🇬🇧", "lang_select:en"),
			tgbotapi.NewInlineKeyboardButtonData("Bahasa Indonesia 🇮🇩", "lang_select:id"),
		),
	)
	return keyboard
}

