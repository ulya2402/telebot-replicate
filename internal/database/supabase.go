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

type Statistics struct {
	TotalUsers     int `json:"total_users"`
	NewUsersToday  int `json:"new_users_today"`
	PremiumUsers   int `json:"premium_users"`
}

func (c *Client) GetAllUsers() ([]User, error) {
	var results []User
	_, err := c.From("users").Select("*", "exact", false).ExecuteTo(&results)
	if err != nil {
		log.Printf("ERROR: Failed to get all users: %v", err)
		return nil, err
	}
	return results, nil
}

func (c *Client) GetStatistics() (*Statistics, error) {
    var stats Statistics
    
    // Total Users
    var totalUsers []map[string]interface{}
    _, err := c.From("users").Select("count", "exact", true).ExecuteTo(&totalUsers)
    if err != nil || len(totalUsers) == 0 {
        return nil, err
    }
    stats.TotalUsers = int(totalUsers[0]["count"].(float64))

    // New Users Today (UTC)
    var newUsersToday []map[string]interface{}
    today := time.Now().UTC().Format("2006-01-02")
    _, err = c.From("users").Select("count", "exact", true).Gte("created_at", today).ExecuteTo(&newUsersToday)
    if err != nil || len(newUsersToday) == 0 {
        return nil, err
    }
    stats.NewUsersToday = int(newUsersToday[0]["count"].(float64))

    // Premium Users
    var premiumUsers []map[string]interface{}
    _, err = c.From("users").Select("count", "exact", true).Eq("is_premium", "true").ExecuteTo(&premiumUsers)
    if err != nil || len(premiumUsers) == 0 {
        return nil, err
    }
    stats.PremiumUsers = int(premiumUsers[0]["count"].(float64))

    return &stats, nil
}