package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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

// --- Database ---

func initDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, err
		}
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS posts (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_id  INTEGER DEFAULT NULL,
			author     TEXT    NOT NULL,
			avatar     TEXT    NOT NULL DEFAULT '',
			content    TEXT    NOT NULL,
			image      TEXT    NOT NULL DEFAULT '',
			preview    TEXT    NOT NULL DEFAULT '',
			created_at TEXT    NOT NULL,
			FOREIGN KEY (parent_id) REFERENCES posts(id)
		)
	`)
	if err != nil {
		return nil, err
	}
	// Migrate existing DBs
	for _, col := range []string{
		"ALTER TABLE posts ADD COLUMN image TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE posts ADD COLUMN preview TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE posts ADD COLUMN parent_id INTEGER DEFAULT NULL",
	} {
		_, _ = db.Exec(col)
	}
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_posts_id ON posts(id DESC)")
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_posts_parent ON posts(parent_id)")
	return db, nil
}

type Store struct {
	db *sql.DB
}

const postCols = "p.id, p.parent_id, p.author, p.avatar, p.content, p.image, p.preview, p.created_at"

func scanPost(rows *sql.Rows) (Post, error) {
	var p Post
	var ts string
	var pid sql.NullInt64
	var replyCount int
	if err := rows.Scan(&p.ID, &pid, &p.Author, &p.Avatar, &p.Content, &p.Image, &p.Preview, &ts, &replyCount); err != nil {
		return p, err
	}
	if pid.Valid {
		v := int(pid.Int64)
		p.ParentID = &v
	}
	p.ReplyCount = replyCount
	p.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	return p, nil
}

func (s *Store) GetPage(beforeID, limit int) ([]Post, bool, error) {
	q := `SELECT ` + postCols + `, (SELECT COUNT(*) FROM posts r WHERE r.parent_id = p.id) AS reply_count
		FROM posts p WHERE p.parent_id IS NULL`
	args := []any{}
	if beforeID > 0 {
		q += " AND p.id < ?"
		args = append(args, beforeID)
	}
	q += " ORDER BY p.id DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var posts []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, false, err
		}
		posts = append(posts, p)
	}
	if posts == nil {
		posts = []Post{}
	}
	hasMore := len(posts) > limit
	if hasMore {
		posts = posts[:limit]
	}
	return posts, hasMore, rows.Err()
}

func (s *Store) GetNewSince(afterID int) ([]Post, error) {
	rows, err := s.db.Query(
		`SELECT `+postCols+`, (SELECT COUNT(*) FROM posts r WHERE r.parent_id = p.id) AS reply_count
		FROM posts p WHERE p.parent_id IS NULL AND p.id > ? ORDER BY p.id DESC LIMIT 50`,
		afterID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	if posts == nil {
		posts = []Post{}
	}
	return posts, rows.Err()
}

func (s *Store) GetReplies(parentID int) ([]Post, error) {
	rows, err := s.db.Query(
		`SELECT `+postCols+`, 0 AS reply_count FROM posts p WHERE p.parent_id = ? ORDER BY p.id ASC`,
		parentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	if posts == nil {
		posts = []Post{}
	}
	return posts, rows.Err()
}

func (s *Store) Add(parentID *int, author, avatar, content, image, preview string) (Post, error) {
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)
	var pid sql.NullInt64
	if parentID != nil {
		pid = sql.NullInt64{Int64: int64(*parentID), Valid: true}
	}
	res, err := s.db.Exec(
		"INSERT INTO posts (parent_id, author, avatar, content, image, preview, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		pid, author, avatar, content, image, preview, ts,
	)
	if err != nil {
		return Post{}, err
	}
	id, _ := res.LastInsertId()
	return Post{ID: int(id), ParentID: parentID, Author: author, Avatar: avatar, Content: content, Image: image, Preview: preview, CreatedAt: now}, nil
}

func (s *Store) Exists(id int) bool {
	var n int
	_ = s.db.QueryRow("SELECT 1 FROM posts WHERE id = ?", id).Scan(&n)
	return n == 1
}

// --- Link preview ---

var (
	ogTitleRe = regexp.MustCompile(`(?i)<meta\s+(?:[^>]*?\s+)?(?:property|name)=["']og:title["']\s+content=["']([^"']+)["']`)
	ogDescRe  = regexp.MustCompile(`(?i)<meta\s+(?:[^>]*?\s+)?(?:property|name)=["']og:description["']\s+content=["']([^"']+)["']`)
	ogImageRe = regexp.MustCompile(`(?i)<meta\s+(?:[^>]*?\s+)?(?:property|name)=["']og:image["']\s+content=["']([^"']+)["']`)
	ogSiteRe  = regexp.MustCompile(`(?i)<meta\s+(?:[^>]*?\s+)?(?:property|name)=["']og:site_name["']\s+content=["']([^"']+)["']`)
	titleRe   = regexp.MustCompile(`(?i)<title[^>]*>([^<]+)</title>`)
	urlRe     = regexp.MustCompile(`https?://[^\s<>"]+`)

	// Also match content-first variant: content="..." property="og:..."
	ogTitleRe2 = regexp.MustCompile(`(?i)<meta\s+content=["']([^"']+)["']\s+(?:property|name)=["']og:title["']`)
	ogDescRe2  = regexp.MustCompile(`(?i)<meta\s+content=["']([^"']+)["']\s+(?:property|name)=["']og:description["']`)
	ogImageRe2 = regexp.MustCompile(`(?i)<meta\s+content=["']([^"']+)["']\s+(?:property|name)=["']og:image["']`)
	ogSiteRe2  = regexp.MustCompile(`(?i)<meta\s+content=["']([^"']+)["']\s+(?:property|name)=["']og:site_name["']`)
)

func matchOG(html string, re1, re2 *regexp.Regexp) string {
	if m := re1.FindStringSubmatch(html); len(m) > 1 {
		return m[1]
	}
	if m := re2.FindStringSubmatch(html); len(m) > 1 {
		return m[1]
	}
	return ""
}

var previewClient = &http.Client{
	Timeout: 4 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

func fetchPreview(rawURL string) (*LinkPreview, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid url")
	}
	// Block private IPs (SSRF protection)
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return nil, fmt.Errorf("blocked")
		}
	}

	req, _ := http.NewRequest("GET", rawURL, nil)
	req.Header.Set("User-Agent", "feed-bot/1.0 (link preview)")
	req.Header.Set("Accept", "text/html")

	resp, err := previewClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read max 128KB (only need the <head>)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	html := string(body)

	title := matchOG(html, ogTitleRe, ogTitleRe2)
	if title == "" {
		if m := titleRe.FindStringSubmatch(html); len(m) > 1 {
			title = strings.TrimSpace(m[1])
		}
	}
	if title == "" {
		return nil, fmt.Errorf("no title found")
	}

	return &LinkPreview{
		URL:         rawURL,
		Title:       title,
		Description: matchOG(html, ogDescRe, ogDescRe2),
		Image:       matchOG(html, ogImageRe, ogImageRe2),
		SiteName:    matchOG(html, ogSiteRe, ogSiteRe2),
	}, nil
}

func extractFirstURL(text string) string {
	m := urlRe.FindString(text)
	// Trim trailing punctuation
	m = strings.TrimRight(m, ".,;:!?)")
	return m
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
	maxAvatarBytes = 150_000
	maxImageBytes  = 700_000
	maxBodyBytes   = 1_000_000
	defaultLimit   = 20
	maxLimit       = 50
)

var imageDataURIRe = regexp.MustCompile(`^data:image/(png|jpeg|gif|webp);base64,`)

func sanitize(s string) string { return strings.TrimSpace(s) }

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
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), sw.code, time.Since(start).Round(time.Microsecond))
		}
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
	previewLimiter := NewRateLimiter(30, time.Minute)
	mux := http.NewServeMux()

	// GET /api/posts?before_id=N&limit=N  (top-level only)
	// POST /api/posts
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
			// Validate parent exists
			if req.ParentID != nil {
				if !store.Exists(*req.ParentID) {
					writeError(w, http.StatusBadRequest, "parent post not found")
					return
				}
			}
			// Auto-fetch link preview from first URL in content
			var previewJSON string
			if rawURL := extractFirstURL(content); rawURL != "" {
				if lp, err := fetchPreview(rawURL); err == nil {
					if b, err := json.Marshal(lp); err == nil {
						previewJSON = string(b)
					}
				}
			}
			post, err := store.Add(req.ParentID, author, avatar, content, image, previewJSON)
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

	// GET /api/posts/new?after_id=N
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

	// GET /api/posts/replies?post_id=N
	mux.HandleFunc("/api/posts/replies", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		postID := queryInt(r, "post_id", 0)
		if postID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid post_id")
			return
		}
		replies, err := store.GetReplies(postID)
		if err != nil {
			log.Printf("db read error: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to load replies")
			return
		}
		writeJSON(w, http.StatusOK, replies)
	})

	// GET /api/preview?url=...
	mux.HandleFunc("/api/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ip := clientIP(r)
		if !previewLimiter.Allow(ip) {
			writeError(w, http.StatusTooManyRequests, "slow down")
			return
		}
		rawURL := r.URL.Query().Get("url")
		if rawURL == "" {
			writeError(w, http.StatusBadRequest, "url required")
			return
		}
		lp, err := fetchPreview(rawURL)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "could not fetch preview")
			return
		}
		writeJSON(w, http.StatusOK, lp)
	})

	// Static files
	fs := http.FileServer(http.Dir("static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	// Serve favicons/manifest at root paths too (browsers expect them there)
	rootStatic := []string{
		"favicon.ico", "favicon.svg", "favicon-16x16.png", "favicon-32x32.png",
		"apple-touch-icon.png", "android-chrome-192x192.png", "android-chrome-512x512.png",
		"site.webmanifest",
	}
	for _, f := range rootStatic {
		file := f
		mux.HandleFunc("/"+file, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, "static/"+file)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "static/index.html")
	})

	handler := requestLogger(corsMiddleware(securityHeaders(mux)))
	addr := envOr("ADDR", ":7291")

	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 25 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	log.Printf("listening on %s (db: %s)", addr, dbPath)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Println("stopped")
}
