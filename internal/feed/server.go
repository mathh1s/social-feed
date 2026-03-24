package feed

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Run starts the feed server. Reads DB_PATH and ADDR from env.
func Run() {
	dbPath := envOr("DB_PATH", "feed.db")
	db, err := initDB(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	store := &store{db: db, dbPath: dbPath}
	adminPass := envOr("ADMIN_PASS", "changeme")
	writeLimiter := newRateLimiter(10, time.Minute)
	previewLimiter := newRateLimiter(30, time.Minute)
	loginLimiter := newRateLimiter(5, time.Minute)

	// Admin session tokens (in-memory, 24h expiry)
	var adminMu sync.Mutex
	adminSessions := make(map[string]time.Time)

	newAdminToken := func() string {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		tok := hex.EncodeToString(b)
		adminMu.Lock()
		// Clean expired
		for k, exp := range adminSessions {
			if time.Now().After(exp) {
				delete(adminSessions, k)
			}
		}
		adminSessions[tok] = time.Now().Add(24 * time.Hour)
		adminMu.Unlock()
		return tok
	}

	checkAdmin := func(r *http.Request) bool {
		auth := r.Header.Get("Authorization")
		tok := strings.TrimPrefix(auth, "Bearer ")
		if tok == "" || tok == auth {
			return false
		}
		adminMu.Lock()
		defer adminMu.Unlock()
		exp, ok := adminSessions[tok]
		if !ok || time.Now().After(exp) {
			delete(adminSessions, tok)
			return false
		}
		return true
	}

	mux := http.NewServeMux()

	// GET /api/posts?before_id=N&limit=N
	// POST /api/posts
	mux.HandleFunc("/api/posts", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			beforeID := queryInt(r, "before_id", 0)
			limit := queryInt(r, "limit", defaultLimit)
			if limit > maxLimit {
				limit = maxLimit
			}
			posts, hasMore, err := store.getPage(beforeID, limit)
			if err != nil {
				log.Printf("db read error: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to load posts")
				return
			}
			writeJSON(w, http.StatusOK, postsResponse{Posts: posts, HasMore: hasMore})

		case http.MethodPost:
			ip := clientIP(r)
			if !writeLimiter.allow(ip) {
				writeError(w, http.StatusTooManyRequests, "slow down, max 10 posts per minute")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
			var req createPostRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			author, avatar, content, image, errMsg := validatePost(req)
			if errMsg != "" {
				writeError(w, http.StatusUnprocessableEntity, errMsg)
				return
			}
			if req.ParentID != nil && !store.exists(*req.ParentID) {
				writeError(w, http.StatusBadRequest, "parent post not found")
				return
			}
			var previewJSON string
			if rawURL := extractFirstURL(content); rawURL != "" {
				if lp, err := fetchPreview(rawURL); err == nil {
					if b, err := json.Marshal(lp); err == nil {
						previewJSON = string(b)
					}
				}
			}
			post, err := store.add(req.ParentID, author, avatar, content, image, previewJSON)
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
		posts, err := store.getNewSince(afterID)
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
		replies, err := store.getReplies(postID)
		if err != nil {
			log.Printf("db read error: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to load replies")
			return
		}
		writeJSON(w, http.StatusOK, replies)
	})

	// POST /api/react?post_id=N
	reactLimiter := newRateLimiter(60, time.Minute)
	mux.HandleFunc("/api/react", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ip := clientIP(r)
		if !reactLimiter.allow(ip) {
			writeError(w, http.StatusTooManyRequests, "slow down")
			return
		}
		postID := queryInt(r, "post_id", 0)
		if postID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid post_id")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var req reactRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		counts, err := store.react(postID, req.Emoji, ip)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid reaction")
			return
		}
		writeJSON(w, http.StatusOK, counts)
	})

	// DELETE /api/delete?post_id=N
	mux.HandleFunc("/api/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ip := clientIP(r)
		if !writeLimiter.allow(ip) {
			writeError(w, http.StatusTooManyRequests, "slow down")
			return
		}
		postID := queryInt(r, "post_id", 0)
		if postID <= 0 {
			writeError(w, http.StatusBadRequest, "invalid post_id")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var req deleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Token == "" {
			writeError(w, http.StatusUnauthorized, "token required")
			return
		}
		ok, err := store.deletePost(postID, req.Token)
		if err != nil {
			log.Printf("db delete error: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to delete")
			return
		}
		if !ok {
			writeError(w, http.StatusForbidden, "invalid token or post not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	})

	// GET /api/search?q=...
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		q := r.URL.Query().Get("q")
		if len(q) < 2 {
			writeError(w, http.StatusBadRequest, "query too short")
			return
		}
		if len(q) > 100 {
			q = q[:100]
		}
		posts, err := store.search(q, 30)
		if err != nil {
			log.Printf("db search error: %v", err)
			writeError(w, http.StatusInternalServerError, "search failed")
			return
		}
		writeJSON(w, http.StatusOK, posts)
	})

	// GET /api/preview?url=...
	mux.HandleFunc("/api/preview", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !previewLimiter.allow(clientIP(r)) {
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

	// POST /api/admin/login
	mux.HandleFunc("/api/admin/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !loginLimiter.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Password), []byte(adminPass)) != 1 {
			writeError(w, http.StatusUnauthorized, "wrong password")
			return
		}
		tok := newAdminToken()
		writeJSON(w, http.StatusOK, map[string]string{"token": tok})
	})

	// GET /api/admin/stats
	mux.HandleFunc("/api/admin/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !checkAdmin(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeJSON(w, http.StatusOK, store.stats())
	})

	// GET /admin
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/admin.html")
	})

	// Static files
	fs := http.FileServer(http.Dir("static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))
	// Favicons at root paths (browsers expect /favicon.ico etc.)
	for _, f := range []string{
		"favicon.ico", "favicon.svg", "favicon-16x16.png", "favicon-32x32.png",
		"apple-touch-icon.png", "android-chrome-192x192.png", "android-chrome-512x512.png",
		"site.webmanifest",
	} {
		file := f
		mux.HandleFunc("/"+file, func(w http.ResponseWriter, r *http.Request) {
			path := "static/images/logos/" + file
			if file == "site.webmanifest" {
				path = "static/" + file
			}
			http.ServeFile(w, r, path)
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
