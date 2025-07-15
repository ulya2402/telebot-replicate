package bot

import (
	"fmt"
	"strconv"
	"telegram-ai-bot/internal/config"

	"telegram-ai-bot/internal/database"
	

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
		buttonText := fmt.Sprintf("%s (%d 💵)", model.Name, model.Cost)
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

func (h *Handler) createSettingsKeyboard(lang string, user *database.User) tgbotapi.InlineKeyboardMarkup {
	textAR := h.Localizer.Get(lang, "change_aspect_ratio")
	textNum := h.Localizer.Get(lang, "change_num_images")

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(textAR, "settings_aspect_ratio"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(textNum, "settings_num_images"),
		),
	)
	return keyboard
}

func (h *Handler) createAspectRatioKeyboard(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("1:1 (Square)", "set_ar:1:1"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("16:9 (Landscape)", "set_ar:16:9"),
			tgbotapi.NewInlineKeyboardButtonData("9:16 (Portrait)", "set_ar:9:16"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("4:3 (Classic)", "set_ar:4:3"),
			tgbotapi.NewInlineKeyboardButtonData("3:4 (Classic)", "set_ar:3:4"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("3:2 (Photo)", "set_ar:3:2"),
			tgbotapi.NewInlineKeyboardButtonData("2:3 (Photo)", "set_ar:2:3"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("5:4 (Vintage)", "set_ar:5:4"),
			tgbotapi.NewInlineKeyboardButtonData("4:5 (Vintage)", "set_ar:4:5"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("21:9 (Widescreen)", "set_ar:21:9"),
			tgbotapi.NewInlineKeyboardButtonData("9:21 (Widescreen)", "set_ar:9:21"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "back_button"), "settings_back_to_main"),
		),
	)
}

func (h *Handler) createNumOutputsKeyboard(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("1", "set_num:1"),
			tgbotapi.NewInlineKeyboardButtonData("2", "set_num:2"),
			tgbotapi.NewInlineKeyboardButtonData("3", "set_num:3"),
			tgbotapi.NewInlineKeyboardButtonData("4", "set_num:4"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "back_button"), "settings_back_to_main"),
		),
	)
}

func (h *Handler) createMainMenuKeyboard(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_generate"), "main_menu_generate"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_settings"), "main_menu_settings"),
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_language"), "main_menu_language"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_help"), "main_menu_help"),
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_referral"), "main_menu_referral"),
		),
	)
}

// Fungsi baru untuk tombol Back ke menu utama
func (h *Handler) createBackToMenuKeyboard(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "back_button"), "main_menu_back"),
		),
	)
}