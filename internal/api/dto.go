package api

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ibrohimcoder/portfolio-api/internal/db"
)

const dateLayout = "2006-01-02"

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	locales  = map[string]bool{"en": true, "uz": true}
	emailRe  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

/* ------------------------------- auth DTOs ------------------------------- */

type registerInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResponse struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type authResponse struct {
	Token     string       `json:"token"`
	ExpiresAt time.Time    `json:"expires_at"`
	User      userResponse `json:"user"`
}

func toUserResponse(u db.User) userResponse {
	return userResponse{ID: u.ID, Email: u.Email, Name: u.Name, CreatedAt: u.CreatedAt}
}

/* ------------------------------- post DTOs ------------------------------- */

type postInput struct {
	Locale      string   `json:"locale"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	Cover       string   `json:"cover"`
	Featured    bool     `json:"featured"`
	Draft       bool     `json:"draft"`
	Date        string   `json:"date"`
}

func (in *postInput) validate() error {
	if !locales[in.Locale] {
		return fmt.Errorf("locale must be one of: en, uz")
	}
	if !slugRe.MatchString(in.Slug) {
		return fmt.Errorf("slug must be lowercase letters, numbers and hyphens")
	}
	if strings.TrimSpace(in.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if in.Date != "" {
		if _, err := time.Parse(dateLayout, in.Date); err != nil {
			return fmt.Errorf("date must be in YYYY-MM-DD format")
		}
	}
	return nil
}

type postResponse struct {
	ID          uuid.UUID  `json:"id"`
	Locale      string     `json:"locale"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Body        string     `json:"body"`
	Tags        []string   `json:"tags"`
	Cover       string     `json:"cover"`
	Featured    bool       `json:"featured"`
	Draft       bool       `json:"draft"`
	Date        string     `json:"date"`
	PublishedAt *time.Time `json:"published_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func toPostResponse(p db.Post) postResponse {
	return postResponse{
		ID:          p.ID,
		Locale:      p.Locale,
		Slug:        p.Slug,
		Title:       p.Title,
		Description: p.Description,
		Body:        p.Body,
		Tags:        p.Tags,
		Cover:       p.Cover,
		Featured:    p.Featured,
		Draft:       p.Draft,
		Date:        p.ContentDate.Format(dateLayout),
		PublishedAt: p.PublishedAt,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

func toPostResponses(items []db.Post) []postResponse {
	out := make([]postResponse, 0, len(items))
	for _, p := range items {
		out = append(out, toPostResponse(p))
	}
	return out
}

/* ----------------------------- project DTOs ------------------------------ */

type projectInput struct {
	Locale      string   `json:"locale"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	Stack       []string `json:"stack"`
	URL         string   `json:"url"`
	Repo        string   `json:"repo"`
	Order       int32    `json:"order"`
	Featured    bool     `json:"featured"`
	Draft       bool     `json:"draft"`
	Date        string   `json:"date"`
}

func (in *projectInput) validate() error {
	if !locales[in.Locale] {
		return fmt.Errorf("locale must be one of: en, uz")
	}
	if !slugRe.MatchString(in.Slug) {
		return fmt.Errorf("slug must be lowercase letters, numbers and hyphens")
	}
	if strings.TrimSpace(in.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if in.Date != "" {
		if _, err := time.Parse(dateLayout, in.Date); err != nil {
			return fmt.Errorf("date must be in YYYY-MM-DD format")
		}
	}
	return nil
}

type projectResponse struct {
	ID          uuid.UUID `json:"id"`
	Locale      string    `json:"locale"`
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Body        string    `json:"body"`
	Tags        []string  `json:"tags"`
	Stack       []string  `json:"stack"`
	URL         string    `json:"url"`
	Repo        string    `json:"repo"`
	Order       int32     `json:"order"`
	Featured    bool      `json:"featured"`
	Draft       bool      `json:"draft"`
	Date        string    `json:"date"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toProjectResponse(p db.Project) projectResponse {
	return projectResponse{
		ID:          p.ID,
		Locale:      p.Locale,
		Slug:        p.Slug,
		Title:       p.Title,
		Description: p.Description,
		Body:        p.Body,
		Tags:        p.Tags,
		Stack:       p.Stack,
		URL:         p.Url,
		Repo:        p.Repo,
		Order:       p.SortOrder,
		Featured:    p.Featured,
		Draft:       p.Draft,
		Date:        p.ContentDate.Format(dateLayout),
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

func toProjectResponses(items []db.Project) []projectResponse {
	out := make([]projectResponse, 0, len(items))
	for _, p := range items {
		out = append(out, toProjectResponse(p))
	}
	return out
}

/* ---------------------------- invitation DTOs ---------------------------- */

type invitationInput struct {
	Source     string `json:"source"`
	SessionID  string `json:"session_id"`
	Date       string `json:"date"`
	Time       string `json:"time"`
	FoodID     string `json:"food_id"`
	FoodLabel  string `json:"food_label"`
	FoodEmoji  string `json:"food_emoji"`
	PlaceID    string `json:"place_id"`
	PlaceLabel string `json:"place_label"`
	PlaceEmoji string `json:"place_emoji"`
	InviteText string `json:"invite_text"`
}

func (in *invitationInput) validate() error {
	if _, err := time.Parse(dateLayout, strings.TrimSpace(in.Date)); err != nil {
		return fmt.Errorf("date must be in YYYY-MM-DD format")
	}
	if strings.TrimSpace(in.Time) == "" {
		return fmt.Errorf("time is required")
	}
	if strings.TrimSpace(in.FoodLabel) == "" && strings.TrimSpace(in.PlaceLabel) == "" {
		return fmt.Errorf("at least one of food or place is required")
	}
	return nil
}

type invitationResponse struct {
	ID         uuid.UUID `json:"id"`
	Source     string    `json:"source"`
	SessionID  string    `json:"session_id"`
	Date       string    `json:"date"`
	Time       string    `json:"time"`
	FoodID     string    `json:"food_id"`
	FoodLabel  string    `json:"food_label"`
	FoodEmoji  string    `json:"food_emoji"`
	PlaceID    string    `json:"place_id"`
	PlaceLabel string    `json:"place_label"`
	PlaceEmoji string    `json:"place_emoji"`
	InviteText string    `json:"invite_text"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
}

type invitationListResponse struct {
	Items []invitationResponse `json:"items"`
	Total int64                `json:"total"`
}

func toInvitationResponse(v db.Invitation) invitationResponse {
	return invitationResponse{
		ID:         v.ID,
		Source:     v.Source,
		SessionID:  v.SessionID,
		Date:       v.EventDate.Format(dateLayout),
		Time:       v.EventTime,
		FoodID:     v.FoodID,
		FoodLabel:  v.FoodLabel,
		FoodEmoji:  v.FoodEmoji,
		PlaceID:    v.PlaceID,
		PlaceLabel: v.PlaceLabel,
		PlaceEmoji: v.PlaceEmoji,
		InviteText: v.InviteText,
		UserAgent:  v.UserAgent,
		CreatedAt:  v.CreatedAt,
	}
}

func toInvitationResponses(items []db.Invitation) []invitationResponse {
	out := make([]invitationResponse, 0, len(items))
	for _, v := range items {
		out = append(out, toInvitationResponse(v))
	}
	return out
}

func clampField(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

/* -------------------------------- helpers -------------------------------- */

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// parseContentDate returns the parsed date or today (UTC) when empty.
func parseContentDate(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Now().UTC().Truncate(24 * time.Hour)
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Now().UTC().Truncate(24 * time.Hour)
	}
	return t
}

// publishedAtFor decides the published_at timestamp from the draft flag.
func publishedAtFor(draft bool, existing *time.Time) *time.Time {
	if draft {
		return nil
	}
	if existing != nil {
		return existing
	}
	now := time.Now().UTC()
	return &now
}
