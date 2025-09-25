package bot

import (
	"fmt"
	"strconv"
	"strings"
	"encoding/json"
	"telegram-ai-bot/internal/config"

	"telegram-ai-bot/internal/database"
	

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const itemsPerPage = 6

func (h *Handler) createProviderSelectionKeyboard(providers []config.Provider, lang string) tgbotapi.InlineKeyboardMarkup {
	var keyboard [][]tgbotapi.InlineKeyboardButton

	var row []tgbotapi.InlineKeyboardButton
	for i, provider := range providers {
		callbackData := fmt.Sprintf("provider_select:%s", provider.ID)
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(provider.Name, callbackData))

		if (i+1)%2 == 0 || i == len(providers)-1 {
			keyboard = append(keyboard, row)
			row = []tgbotapi.InlineKeyboardButton{}
		}
	}

	return tgbotapi.NewInlineKeyboardMarkup(keyboard...)
}

func (h *Handler) createModelSelectionKeyboard(models []config.Model, lang string, providerID string, page int) tgbotapi.InlineKeyboardMarkup {
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
		var buttonText string
		if model.Type == "video" {
			buttonText = fmt.Sprintf("%s (%d 💎)", model.Name, model.DiamondCost)
		} else {
			buttonText = fmt.Sprintf("%s (%d 💵)", model.Name, model.Cost)
		}
		
		callbackData := fmt.Sprintf("model_select:%s", model.ID)
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(buttonText, callbackData))

		if (i+1)%2 == 0 || i == len(paginatedModels)-1 {
			keyboard = append(keyboard, row)
			row = []tgbotapi.InlineKeyboardButton{}
		}
	}

	var navRow []tgbotapi.InlineKeyboardButton
	// Callback data sekarang menyertakan providerID
	if page > 0 {
		prevText := h.Localizer.Get(lang, "prev_button")
		callbackData := fmt.Sprintf("model_page:%s;%d", providerID, page-1)
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(prevText, callbackData))
	}
	if end < len(models) {
		nextText := h.Localizer.Get(lang, "next_button")
		callbackData := fmt.Sprintf("model_page:%s;%d", providerID, page+1)
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(nextText, callbackData))
	}

	if len(navRow) > 0 {
		keyboard = append(keyboard, navRow)
	}

	backButton := tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "back_button"), "back_to_providers")
	keyboard = append(keyboard, tgbotapi.NewInlineKeyboardRow(backButton))

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
		tgbotapi.NewInlineKeyboardRow(
			// --- BARIS YANG DITAMBAHKAN ---
			tgbotapi.NewInlineKeyboardButtonData("Русский 🇷🇺", "lang_select:ru"),
			tgbotapi.NewInlineKeyboardButtonData("Español 🇪🇸", "lang_select:es"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Deutsch 🇩🇪", "lang_select:de"),
			tgbotapi.NewInlineKeyboardButtonData("हिन्दी 🇮🇳", "lang_select:hi"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("中文 🇨🇳", "lang_select:zh"),
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
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_generate_video"), "main_menu_generate_video"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_removebg"), "main_menu_removebg"),

		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_upscaler"), "main_menu_upscaler"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_settings"), "main_menu_settings"),
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_language"), "main_menu_language"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_help"), "main_menu_help"),
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_referral"), "main_menu_referral"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_topup"), "main_menu_topup"),
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_exchange"), "main_menu_exchange"),
		),
		tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_faq"), "main_menu_faq"),
        ),
	)
}

func (h *Handler) createProfileKeyboard(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_topup"), "main_menu_topup"),
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "button_exchange"), "main_menu_exchange"),
		),
		tgbotapi.NewInlineKeyboardRow(
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

func (h *Handler) createAddToGroupKeyboard(lang string, botUsername string) tgbotapi.InlineKeyboardMarkup {
	buttonText := h.Localizer.Get(lang, "button_add_to_group")
	// URL khusus dari Telegram untuk memicu aksi tambah ke grup
	url := fmt.Sprintf("https://t.me/%s?startgroup=true", botUsername)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(buttonText, url),
		),
	)
	return keyboard
}

func (h *Handler) createFaqKeyboard(lang string) tgbotapi.InlineKeyboardMarkup {
    keyboard := tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "faq_q1_button"), "faq_show:q1"),
        ),
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "faq_q2_button"), "faq_show:q2"),
        ),
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "faq_q3_button"), "faq_show:q3"),
        ),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "faq_q4_button"), "faq_show:q4"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "faq_q5_button"), "faq_show:q5"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "faq_q6_button"), "faq_show:q6"),
		),
		tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "faq_q7_button"), "faq_show:q7"),
        ),
		tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "faq_q8_button"), "faq_show:q8"),
        ),
		tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "faq_q9_button"), "faq_show:q9"),
        ),
    )
    return keyboard
}

func (h *Handler) createFaqAnswerKeyboard(lang string) tgbotapi.InlineKeyboardMarkup {
    return tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "back_button"), "faq_back"),
        ),
    )
}

func (h *Handler) createAdvancedSettingsKeyboard(lang string, model *config.Model, user *database.User) (tgbotapi.InlineKeyboardMarkup, string) {
	var settingsText strings.Builder
	settingsText.WriteString("<b>Advanced Settings</b>\n\n")

	var customSettings map[string]interface{}
	if user.CustomSettings != "" {
		json.Unmarshal([]byte(user.CustomSettings), &customSettings)
	} else {
		customSettings = make(map[string]interface{})
	}

	var keyboardRows [][]tgbotapi.InlineKeyboardButton
	var currentRow []tgbotapi.InlineKeyboardButton

	for _, param := range model.Parameters {
		currentValue, ok := customSettings[param.Name]
		if !ok || currentValue == nil {
			if param.Default != nil {
				currentValue = param.Default
			} else {
				currentValue = "Not Set"
			}
		}

		// Format nilai agar lebih rapi
		var displayValue string
		switch v := currentValue.(type) {
		case float64:
			// Cek apakah angka tersebut sebenarnya integer
			if v == float64(int(v)) {
				displayValue = fmt.Sprintf("%d", int(v))
			} else {
				displayValue = fmt.Sprintf("%.1f", v)
			}
		default:
			displayValue = fmt.Sprintf("%v", currentValue)
		}


		settingsText.WriteString(fmt.Sprintf("▸ %s: <code>%s</code>\n", param.Label, displayValue))

		button := tgbotapi.NewInlineKeyboardButtonData("Change "+param.Label, fmt.Sprintf("adv_setting_select:%s:%s", model.ID, param.Name))
		currentRow = append(currentRow, button)

		if len(currentRow) == 2 {
			keyboardRows = append(keyboardRows, currentRow)
			currentRow = []tgbotapi.InlineKeyboardButton{}
		}
	}

	if len(currentRow) > 0 {
		keyboardRows = append(keyboardRows, currentRow)
	}

	backButton := tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "back_button"), fmt.Sprintf("adv_setting_back:%s", model.ID))
	keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(backButton))

	return tgbotapi.NewInlineKeyboardMarkup(keyboardRows...), settingsText.String()
}


func (h *Handler) createCancelFlowKeyboard(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "cancel_button"), "cancel_flow"),
		),
	)
}

func (h *Handler) createStyleConfirmationKeyboard(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🚀 Mulai Generate Sekarang", "style_confirm:generate_now"),
			tgbotapi.NewInlineKeyboardButtonData("🎨 Pilih Gaya", "style_confirm:show_styles"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "cancel_button"), "cancel_flow"),
		),
	)
}

func (h *Handler) createStyleSelectionKeyboard(styles []config.StyleTemplate, lang string) tgbotapi.InlineKeyboardMarkup {
	var keyboardRows [][]tgbotapi.InlineKeyboardButton
	var currentRow []tgbotapi.InlineKeyboardButton

	// Tombol "Lewati" di baris pertama
	skipButtonText := h.Localizer.Get(lang, "style_skip_button")
	skipButton := tgbotapi.NewInlineKeyboardButtonData(skipButtonText, "style_select:style_none")
	keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(skipButton))
	// Tombol-tombol gaya lainnya
	for _, style := range styles {
		if style.ID == "style_none" { continue } 
		
		callbackData := fmt.Sprintf("style_select:%s", style.ID)
		button := tgbotapi.NewInlineKeyboardButtonData(style.Name, callbackData)
		currentRow = append(currentRow, button)

		if len(currentRow) == 2 {
			keyboardRows = append(keyboardRows, currentRow)
			currentRow = []tgbotapi.InlineKeyboardButton{}
		}
	}
	if len(currentRow) > 0 {
		keyboardRows = append(keyboardRows, currentRow)
	}

	// PERBAIKAN: Gunakan `lang` yang sudah menjadi parameter fungsi
	cancelButton := tgbotapi.NewInlineKeyboardButtonData(h.Localizer.Get(lang, "cancel_button"), "cancel_flow")
	keyboardRows = append(keyboardRows, tgbotapi.NewInlineKeyboardRow(cancelButton))
	
	return tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)

}

// AWAL PERUBAHAN

// Fungsi untuk membuat keyboard reply (di bawah layar) untuk alur multi-gambar
func (h *Handler) createMultiImageReplyKeyboard(lang string) tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(h.Localizer.Get(lang, "multi_image_button_done")),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(h.Localizer.Get(lang, "cancel_button")),
		),
	)
}

func (h *Handler) createRemoveReplyKeyboard() tgbotapi.ReplyKeyboardRemove {
	return tgbotapi.NewRemoveKeyboard(true)
}