package localization

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"path/filepath"
	"strings"
)

type Localizer struct {
	translations map[string]map[string]string
}

func New(localesDir string) *Localizer {
	l := &Localizer{
		translations: make(map[string]map[string]string),
	}

	files, err := ioutil.ReadDir(localesDir)
	if err != nil {
		log.Fatalf("FATAL: Failed to read locales directory: %v", err)
	}

	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".json") {
			langCode := strings.TrimSuffix(file.Name(), ".json")
			filePath := filepath.Join(localesDir, file.Name())

			fileContent, err := ioutil.ReadFile(filePath)
			if err != nil {
				log.Printf("WARN: Could not read locale file %s: %v", filePath, err)
				continue
			}

			var trans map[string]string
			if err := json.Unmarshal(fileContent, &trans); err != nil {
				log.Printf("WARN: Could not parse locale file %s: %v", filePath, err)
				continue
			}

			l.translations[langCode] = trans
			log.Printf("INFO: Loaded language '%s'", langCode)
		}
	}
	return l
}

func (l *Localizer) Get(lang, key string) string {
	if translations, ok := l.translations[lang]; ok {
		if value, ok := translations[key]; ok {
			return value
		}
	}
	
	if translations, ok := l.translations["en"]; ok {
		if value, ok := translations[key]; ok {
			return value
		}
	}

	return key
}

func (l *Localizer) Getf(lang, key string, args map[string]string) string {
	format := l.Get(lang, key)
	for k, v := range args {
		placeholder := "{" + k + "}"
		format = strings.ReplaceAll(format, placeholder, v)
	}
	return format
}