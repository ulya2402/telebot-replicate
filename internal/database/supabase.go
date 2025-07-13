package database

import (
	"log"
	"strconv"
	"time"

	"telegram-ai-bot/internal/config"

	supa "github.com/supabase-community/supabase-go"
)

type User struct {
	ID                  int64     `json:"id,omitempty"`
	TelegramID          int64     `json:"telegram_id"`
	Username            string    `json:"username"`
	Credits             int       `json:"credits"`
	LastReset           time.Time `json:"last_reset"`
	IsPremium           bool      `json:"is_premium"`
	LanguageCode        string    `json:"language_code"`
	ReferrerID          int64     `json:"referrer_id,omitempty"`
	GeneratedImageCount int       `json:"generated_image_count"`
}

type Client struct {
	*supa.Client
}

func NewClient(cfg *config.Config) *Client {
	client, err := supa.NewClient(cfg.SupabaseURL, cfg.SupabaseServiceKey, nil)
	if err != nil {
		log.Fatalf("FATAL: Cannot initialize Supabase client: %v", err)
	}
	return &Client{client}
}

func (c *Client) GetUserByTelegramID(telegramID int64) (*User, error) {
	var results []User
	// PERBAIKAN: Menambahkan _ untuk menangani nilai kembalian kedua
	_, err := c.From("users").Select("*", "exact", false).Eq("telegram_id", strconv.FormatInt(telegramID, 10)).ExecuteTo(&results)
	if err != nil {
		log.Printf("ERROR: Failed to get user %d: %v", telegramID, err)
		return nil, err
	}

	if len(results) == 0 {
		return nil, nil
	}

	return &results[0], nil
}

func (c *Client) CreateUser(user User) (*User, error) {
	var results []User
	// PERBAIKAN: Menambahkan _ untuk menangani nilai kembalian kedua
	_, err := c.From("users").Insert(user, false, "do-nothing", "", "exact").ExecuteTo(&results)
	if err != nil {
		log.Printf("ERROR: Failed to create user %d: %v", user.TelegramID, err)
		return nil, err
	}

	if len(results) == 0 {
		return c.GetUserByTelegramID(user.TelegramID)
	}

	log.Printf("INFO: User %d created successfully", user.TelegramID)
	return &results[0], nil
}

func (c *Client) UpdateUser(user *User) error {
	var results []User
	// PERBAIKAN: Menambahkan _ untuk menangani nilai kembalian kedua
	_, err := c.From("users").Update(user, "", "exact").Eq("telegram_id", strconv.FormatInt(user.TelegramID, 10)).ExecuteTo(&results)
	if err != nil {
		log.Printf("ERROR: Failed to update user %d: %v", user.TelegramID, err)
	}
	return err
}