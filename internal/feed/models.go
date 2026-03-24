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

type loginRequest struct {
	Password string `json:"password"`
}

type adminStats struct {
	TotalPosts     int            `json:"total_posts"`
	TotalReplies   int            `json:"total_replies"`
	TotalReactions int            `json:"total_reactions"`
	UniqueAuthors  int            `json:"unique_authors"`
	PostsToday     int            `json:"posts_today"`
	PostsThisWeek  int            `json:"posts_this_week"`
	TopPosters     []posterStat   `json:"top_posters"`
	ReactionBreak  map[string]int `json:"reaction_breakdown"`
	DbSizeBytes    int64          `json:"db_size_bytes"`
}

type posterStat struct {
	Author string `json:"author"`
	Count  int    `json:"count"`
}
