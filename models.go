package main

import "time"

type Post struct {
	ID         int       `json:"id"`
	ParentID   *int      `json:"parent_id"`
	Author     string    `json:"author"`
	Avatar     string    `json:"avatar"`
	Content    string    `json:"content"`
	Image      string    `json:"image"`
	Preview    string    `json:"preview"`
	ReplyCount int       `json:"reply_count"`
	CreatedAt  time.Time `json:"created_at"`
}

type PostsResponse struct {
	Posts   []Post `json:"posts"`
	HasMore bool   `json:"has_more"`
}

type CreatePostRequest struct {
	ParentID *int   `json:"parent_id"`
	Author   string `json:"author"`
	Avatar   string `json:"avatar"`
	Content  string `json:"content"`
	Image    string `json:"image"`
}

type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
	SiteName    string `json:"site_name"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
