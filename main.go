package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// --- Models ---

type Post struct {
	ID        int       `json:"id"`
	Author    string    `json:"author"`
	Avatar    string    `json:"avatar"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type CreatePostRequest struct {
	Author  string `json:"author"`
	Avatar  string `json:"avatar"`
	Content string `json:"content"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// --- Database ---

func initDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, err
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS posts (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			author     TEXT    NOT NULL,
			avatar     TEXT    NOT NULL DEFAULT '',
			content    TEXT    NOT NULL,
			created_at TEXT    NOT NULL
		)
	`)
	return db, err
}

type Store struct {
	db *sql.DB
}

func (s *Store) GetAll() ([]Post, error) {
	rows, err := s.db.Query(
		"SELECT id, author, avatar, content, created_at FROM posts ORDER BY id DESC LIMIT 200",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []Post
	for rows.Next() {
		var p Post
		var ts string
		if err := rows.Scan(&p.ID, &p.Author, &p.Avatar, &p.Content, &ts); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		posts = append(posts, p)
	}
	if posts == nil {
		posts = []Post{}
	}
	return posts, rows.Err()
}

func (s *Store) Add(author, avatar, content string) (Post, error) {
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)
	res, err := s.db.Exec(
		"INSERT INTO posts (author, avatar, content, created_at) VALUES (?, ?, ?, ?)",
		author, avatar, content, ts,
	)
	if err != nil {
		return Post{}, err
	}
	id, _ := res.LastInsertId()
	return Post{
		ID:        int(id),
		Author:    author,
		Avatar:    avatar,
		Content:   content,
		CreatedAt: now,
	}, nil
}

// --- Rate limiter ---

type RateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int
	window   time.Duration
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	times := rl.requests[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= rl.limit {
		rl.requests[ip] = valid
		return false
	}
	rl.requests[ip] = append(valid, now)
	return true
}

// --- Validation ---

const (
	maxAuthorLen   = 40
	maxContentLen  = 500
	maxAvatarBytes = 150_000
	maxBodyBytes   = 256_000
)

var avatarPrefixRe = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,`)

func sanitize(s string) string {
	s = strings.TrimSpace(s)
	s = html.EscapeString(s)
	return s
}

func validateAvatar(raw string) (string, string) {
	if raw == "" {
		return "", ""
	}
	if len(raw) > maxAvatarBytes {
		return "", "avatar too large (max ~100KB)"
	}
	if !avatarPrefixRe.MatchString(raw) {
		return "", "avatar must be a base64 data URI (png/jpeg/gif/webp)"
	}
	return raw, ""
}

func validatePost(req CreatePostRequest) (author, avatar, content, errMsg string) {
	author = sanitize(req.Author)
	content = sanitize(req.Content)
	if author == "" || content == "" {
		return "", "", "", "author and content are required"
	}
	if utf8.RuneCountInString(author) > maxAuthorLen {
		return "", "", "", fmt.Sprintf("author too long (max %d chars)", maxAuthorLen)
	}
	if utf8.RuneCountInString(content) > maxContentLen {
		return "", "", "", fmt.Sprintf("content too long (max %d chars)", maxContentLen)
	}
	avatar, errMsg = validateAvatar(req.Avatar)
	if errMsg != "" {
		return "", "", "", errMsg
	}
	return author, avatar, content, ""
}

// --- Middleware ---

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func clientIP(r *http.Request) string {
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i != -1 {
		ip = ip[:i]
	}
	return ip
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	dbPath := envOr("DB_PATH", "feed.db")
	db, err := initDB(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	store := &Store{db: db}
	limiter := NewRateLimiter(10, time.Minute)
	mux := http.NewServeMux()

	mux.HandleFunc("/api/posts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			posts, err := store.GetAll()
			if err != nil {
				log.Printf("db read error: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to load posts")
				return
			}
			writeJSON(w, http.StatusOK, posts)

		case http.MethodPost:
			ip := clientIP(r)
			if !limiter.Allow(ip) {
				writeError(w, http.StatusTooManyRequests, "slow down, max 10 posts per minute")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

			var req CreatePostRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON")
				return
			}

			author, avatar, content, errMsg := validatePost(req)
			if errMsg != "" {
				writeError(w, http.StatusUnprocessableEntity, errMsg)
				return
			}

			post, err := store.Add(author, avatar, content)
			if err != nil {
				log.Printf("db write error: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to save post")
				return
			}
			writeJSON(w, http.StatusCreated, post)

		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})

	// Serve frontend at root
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "index.html")
	})

	handler := corsMiddleware(securityHeaders(mux))
	addr := envOr("ADDR", ":8080")
	log.Printf("listening on %s (db: %s)", addr, dbPath)
	log.Fatal(http.ListenAndServe(addr, handler))
}
