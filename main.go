package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	Image     string    `json:"image"`
	CreatedAt time.Time `json:"created_at"`
}

type PostsResponse struct {
	Posts   []Post `json:"posts"`
	HasMore bool   `json:"has_more"`
}

type CreatePostRequest struct {
	Author  string `json:"author"`
	Avatar  string `json:"avatar"`
	Content string `json:"content"`
	Image   string `json:"image"`
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
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, err
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS posts (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			author     TEXT    NOT NULL,
			avatar     TEXT    NOT NULL DEFAULT '',
			content    TEXT    NOT NULL,
			image      TEXT    NOT NULL DEFAULT '',
			created_at TEXT    NOT NULL
		)
	`)
	if err != nil {
		return nil, err
	}
	// Migrate: add image column if it doesn't exist (for existing DBs)
	_, _ = db.Exec("ALTER TABLE posts ADD COLUMN image TEXT NOT NULL DEFAULT ''")
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_posts_id ON posts(id DESC)")
	return db, nil
}

type Store struct {
	db *sql.DB
}

func scanPosts(rows *sql.Rows) ([]Post, error) {
	var posts []Post
	for rows.Next() {
		var p Post
		var ts string
		if err := rows.Scan(&p.ID, &p.Author, &p.Avatar, &p.Content, &p.Image, &ts); err != nil {
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

func (s *Store) GetPage(beforeID, limit int) ([]Post, bool, error) {
	var rows *sql.Rows
	var err error
	if beforeID > 0 {
		rows, err = s.db.Query(
			"SELECT id, author, avatar, content, image, created_at FROM posts WHERE id < ? ORDER BY id DESC LIMIT ?",
			beforeID, limit+1,
		)
	} else {
		rows, err = s.db.Query(
			"SELECT id, author, avatar, content, image, created_at FROM posts ORDER BY id DESC LIMIT ?",
			limit+1,
		)
	}
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	posts, err := scanPosts(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(posts) > limit
	if hasMore {
		posts = posts[:limit]
	}
	return posts, hasMore, nil
}

func (s *Store) GetNewSince(afterID int) ([]Post, error) {
	rows, err := s.db.Query(
		"SELECT id, author, avatar, content, image, created_at FROM posts WHERE id > ? ORDER BY id DESC LIMIT 50",
		afterID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPosts(rows)
}

func (s *Store) Add(author, avatar, content, image string) (Post, error) {
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)
	res, err := s.db.Exec(
		"INSERT INTO posts (author, avatar, content, image, created_at) VALUES (?, ?, ?, ?, ?)",
		author, avatar, content, image, ts,
	)
	if err != nil {
		return Post{}, err
	}
	id, _ := res.LastInsertId()
	return Post{ID: int(id), Author: author, Avatar: avatar, Content: content, Image: image, CreatedAt: now}, nil
}

// --- Rate limiter ---

type RateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int
	window   time.Duration
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{requests: make(map[string][]time.Time), limit: limit, window: window}
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
	maxAvatarBytes = 150_000  // ~100KB decoded
	maxImageBytes  = 700_000  // ~500KB decoded
	maxBodyBytes   = 1_000_000
	defaultLimit   = 20
	maxLimit       = 50
)

var imageDataURIRe = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,`)

func sanitize(s string) string {
	return strings.TrimSpace(s)
}

func validateDataURI(raw string, maxBytes int, label string) (string, string) {
	if raw == "" {
		return "", ""
	}
	if len(raw) > maxBytes {
		return "", fmt.Sprintf("%s too large", label)
	}
	if !imageDataURIRe.MatchString(raw) {
		return "", fmt.Sprintf("%s must be a base64 data URI (png/jpeg/gif/webp)", label)
	}
	return raw, ""
}

func validatePost(req CreatePostRequest) (author, avatar, content, image, errMsg string) {
	author = sanitize(req.Author)
	content = sanitize(req.Content)
	if author == "" || content == "" {
		return "", "", "", "", "author and content are required"
	}
	if utf8.RuneCountInString(author) > maxAuthorLen {
		return "", "", "", "", fmt.Sprintf("author too long (max %d chars)", maxAuthorLen)
	}
	if utf8.RuneCountInString(content) > maxContentLen {
		return "", "", "", "", fmt.Sprintf("content too long (max %d chars)", maxContentLen)
	}
	avatar, errMsg = validateDataURI(req.Avatar, maxAvatarBytes, "avatar")
	if errMsg != "" {
		return "", "", "", "", errMsg
	}
	image, errMsg = validateDataURI(req.Image, maxImageBytes, "image")
	if errMsg != "" {
		return "", "", "", "", errMsg
	}
	return author, avatar, content, image, ""
}

func queryInt(r *http.Request, key string, fallback int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return fallback
	}
	return n
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

type statusCapture struct {
	http.ResponseWriter
	code int
}

func (s *statusCapture) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusCapture{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), sw.code, time.Since(start).Round(time.Microsecond))
	})
}

// --- Helpers ---

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

// --- Main ---

func main() {
	dbPath := envOr("DB_PATH", "feed.db")
	db, err := initDB(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	store := &Store{db: db}
	writeLimiter := NewRateLimiter(10, time.Minute)
	mux := http.NewServeMux()

	mux.HandleFunc("/api/posts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			beforeID := queryInt(r, "before_id", 0)
			limit := queryInt(r, "limit", defaultLimit)
			if limit > maxLimit {
				limit = maxLimit
			}
			posts, hasMore, err := store.GetPage(beforeID, limit)
			if err != nil {
				log.Printf("db read error: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to load posts")
				return
			}
			writeJSON(w, http.StatusOK, PostsResponse{Posts: posts, HasMore: hasMore})

		case http.MethodPost:
			ip := clientIP(r)
			if !writeLimiter.Allow(ip) {
				writeError(w, http.StatusTooManyRequests, "slow down, max 10 posts per minute")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
			var req CreatePostRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			author, avatar, content, image, errMsg := validatePost(req)
			if errMsg != "" {
				writeError(w, http.StatusUnprocessableEntity, errMsg)
				return
			}
			post, err := store.Add(author, avatar, content, image)
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

	mux.HandleFunc("/api/posts/new", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		afterID := queryInt(r, "after_id", 0)
		posts, err := store.GetNewSince(afterID)
		if err != nil {
			log.Printf("db read error: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to load posts")
			return
		}
		writeJSON(w, http.StatusOK, posts)
	})

	// Static assets (favicons, manifest)
	staticFiles := map[string]string{
		"/favicon.ico":                "favicon.ico",
		"/favicon.svg":                "favicon.svg",
		"/favicon-16x16.png":          "favicon-16x16.png",
		"/favicon-32x32.png":          "favicon-32x32.png",
		"/apple-touch-icon.png":       "apple-touch-icon.png",
		"/android-chrome-192x192.png": "android-chrome-192x192.png",
		"/android-chrome-512x512.png": "android-chrome-512x512.png",
		"/site.webmanifest":           "site.webmanifest",
	}
	for path, file := range staticFiles {
		f := file
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, f)
		})
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "index.html")
	})

	handler := requestLogger(corsMiddleware(securityHeaders(mux)))
	addr := envOr("ADDR", ":7291")

	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 20 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("listening on %s (db: %s)", addr, dbPath)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Println("stopped")
}
