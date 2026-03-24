package feed

import "time"

type post struct {
	ID          int                `json:"id"`
	ParentID    *int               `json:"parent_id"`
	Author      string             `json:"author"`
	Avatar      string             `json:"avatar"`
	Content     string             `json:"content"`
	Image       string             `json:"image"`
	Preview     string             `json:"preview"`
	ReplyCount  int                `json:"reply_count"`
	CreatedAt   time.Time          `json:"created_at"`
	DeleteToken string             `json:"delete_token,omitempty"`
	Reactions   map[string]int     `json:"reactions"`
}

type postsResponse struct {
	Posts   []post `json:"posts"`
	HasMore bool   `json:"has_more"`
}

type createPostRequest struct {
	ParentID *int   `json:"parent_id"`
	Author   string `json:"author"`
	Avatar   string `json:"avatar"`
	Content  string `json:"content"`
	Image    string `json:"image"`
}

type reactRequest struct {
	Emoji string `json:"emoji"`
}

type deleteRequest struct {
	Token string `json:"token"`
}

type linkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
	SiteName    string `json:"site_name"`
}

type errorResponse struct {
	Error string `json:"error"`
}
