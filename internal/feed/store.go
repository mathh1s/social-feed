package feed

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

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
	for _, col := range []string{
		"ALTER TABLE posts ADD COLUMN image TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE posts ADD COLUMN preview TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE posts ADD COLUMN parent_id INTEGER DEFAULT NULL",
	} {
		_, _ = db.Exec(col)
	}
	// Reactions table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS reactions (
			post_id  INTEGER NOT NULL,
			emoji    TEXT    NOT NULL,
			ip       TEXT    NOT NULL,
			PRIMARY KEY (post_id, emoji, ip),
			FOREIGN KEY (post_id) REFERENCES posts(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		return nil, err
	}

	// Delete tokens
	_, _ = db.Exec("ALTER TABLE posts ADD COLUMN delete_token TEXT NOT NULL DEFAULT ''")

	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_posts_id ON posts(id DESC)")
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_posts_parent ON posts(parent_id)")
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_reactions_post ON reactions(post_id)")
	return db, nil
}

type store struct {
	db     *sql.DB
	dbPath string
}

const postCols = "p.id, p.parent_id, p.author, p.avatar, p.content, p.image, p.preview, p.created_at, p.delete_token"

func scanPost(rows *sql.Rows) (post, error) {
	var p post
	var ts string
	var pid sql.NullInt64
	var replyCount int
	var deleteToken string
	if err := rows.Scan(&p.ID, &pid, &p.Author, &p.Avatar, &p.Content, &p.Image, &p.Preview, &ts, &deleteToken, &replyCount); err != nil {
		return p, err
	}
	if pid.Valid {
		v := int(pid.Int64)
		p.ParentID = &v
	}
	p.ReplyCount = replyCount
	p.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	p.Reactions = make(map[string]int)
	return p, nil
}

func (s *store) loadReactions(posts []post) {
	if len(posts) == 0 {
		return
	}
	ids := make([]any, len(posts))
	placeholders := make([]string, len(posts))
	idxMap := make(map[int]int, len(posts))
	for i, p := range posts {
		ids[i] = p.ID
		placeholders[i] = "?"
		idxMap[p.ID] = i
	}
	q := "SELECT post_id, emoji, COUNT(*) FROM reactions WHERE post_id IN (" + strings.Join(placeholders, ",") + ") GROUP BY post_id, emoji"
	rows, err := s.db.Query(q, ids...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var pid int
		var emoji string
		var count int
		if err := rows.Scan(&pid, &emoji, &count); err == nil {
			if idx, ok := idxMap[pid]; ok {
				posts[idx].Reactions[emoji] = count
			}
		}
	}
}

func collectPosts(rows *sql.Rows) ([]post, error) {
	var posts []post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	if posts == nil {
		posts = []post{}
	}
	return posts, rows.Err()
}

func (s *store) getPage(beforeID, limit int) ([]post, bool, error) {
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

	posts, err := collectPosts(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(posts) > limit
	if hasMore {
		posts = posts[:limit]
	}
	s.loadReactions(posts)
	return posts, hasMore, nil
}

func (s *store) getNewSince(afterID int) ([]post, error) {
	rows, err := s.db.Query(
		`SELECT `+postCols+`, (SELECT COUNT(*) FROM posts r WHERE r.parent_id = p.id) AS reply_count
		FROM posts p WHERE p.parent_id IS NULL AND p.id > ? ORDER BY p.id DESC LIMIT 50`,
		afterID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	posts, err := collectPosts(rows)
	if err != nil {
		return nil, err
	}
	s.loadReactions(posts)
	return posts, nil
}

func (s *store) getReplies(parentID int) ([]post, error) {
	rows, err := s.db.Query(
		`SELECT `+postCols+`, 0 AS reply_count FROM posts p WHERE p.parent_id = ? ORDER BY p.id ASC`,
		parentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	posts, err := collectPosts(rows)
	if err != nil {
		return nil, err
	}
	s.loadReactions(posts)
	return posts, nil
}

func generateToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *store) add(parentID *int, author, avatar, content, image, preview string) (post, error) {
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339)
	token := generateToken()
	var pid sql.NullInt64
	if parentID != nil {
		pid = sql.NullInt64{Int64: int64(*parentID), Valid: true}
	}
	res, err := s.db.Exec(
		"INSERT INTO posts (parent_id, author, avatar, content, image, preview, created_at, delete_token) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		pid, author, avatar, content, image, preview, ts, token,
	)
	if err != nil {
		return post{}, err
	}
	id, _ := res.LastInsertId()
	return post{
		ID: int(id), ParentID: parentID, Author: author, Avatar: avatar,
		Content: content, Image: image, Preview: preview, CreatedAt: now,
		DeleteToken: token, Reactions: make(map[string]int),
	}, nil
}

func (s *store) deletePost(id int, token string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM posts WHERE id = ? AND delete_token = ? AND delete_token != ''", id, token)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		// also delete child replies
		_, _ = s.db.Exec("DELETE FROM posts WHERE parent_id = ?", id)
		_, _ = s.db.Exec("DELETE FROM reactions WHERE post_id = ?", id)
	}
	return n > 0, nil
}

var allowedEmojis = map[string]bool{"👍": true, "🔥": true, "❤️": true, "😂": true}

func (s *store) react(postID int, emoji, ip string) (map[string]int, error) {
	if !allowedEmojis[emoji] {
		return nil, sql.ErrNoRows
	}
	// Toggle: if exists remove, else add
	var exists int
	_ = s.db.QueryRow("SELECT 1 FROM reactions WHERE post_id = ? AND emoji = ? AND ip = ?", postID, emoji, ip).Scan(&exists)
	if exists == 1 {
		_, err := s.db.Exec("DELETE FROM reactions WHERE post_id = ? AND emoji = ? AND ip = ?", postID, emoji, ip)
		if err != nil {
			return nil, err
		}
	} else {
		_, err := s.db.Exec("INSERT OR IGNORE INTO reactions (post_id, emoji, ip) VALUES (?, ?, ?)", postID, emoji, ip)
		if err != nil {
			return nil, err
		}
	}
	// Return updated counts
	rows, err := s.db.Query("SELECT emoji, COUNT(*) FROM reactions WHERE post_id = ? GROUP BY emoji", postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var e string
		var c int
		if err := rows.Scan(&e, &c); err == nil {
			counts[e] = c
		}
	}
	return counts, nil
}

func (s *store) search(query string, limit int) ([]post, error) {
	q := `SELECT ` + postCols + `, (SELECT COUNT(*) FROM posts r WHERE r.parent_id = p.id) AS reply_count
		FROM posts p WHERE p.parent_id IS NULL AND p.content LIKE ? ORDER BY p.id DESC LIMIT ?`
	rows, err := s.db.Query(q, "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	posts, err := collectPosts(rows)
	if err != nil {
		return nil, err
	}
	s.loadReactions(posts)
	return posts, nil
}

func (s *store) exists(id int) bool {
	var n int
	_ = s.db.QueryRow("SELECT 1 FROM posts WHERE id = ?", id).Scan(&n)
	return n == 1
}

func (s *store) stats() adminStats {
	var st adminStats

	_ = s.db.QueryRow("SELECT COUNT(*) FROM posts WHERE parent_id IS NULL").Scan(&st.TotalPosts)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM posts WHERE parent_id IS NOT NULL").Scan(&st.TotalReplies)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM reactions").Scan(&st.TotalReactions)
	_ = s.db.QueryRow("SELECT COUNT(DISTINCT author) FROM posts").Scan(&st.UniqueAuthors)

	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	weekAgo := now.AddDate(0, 0, -7).Format(time.RFC3339)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM posts WHERE created_at >= ?", today).Scan(&st.PostsToday)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM posts WHERE created_at >= ?", weekAgo).Scan(&st.PostsThisWeek)

	// Top 10 posters
	rows, err := s.db.Query("SELECT author, COUNT(*) AS c FROM posts GROUP BY author ORDER BY c DESC LIMIT 10")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var p posterStat
			if rows.Scan(&p.Author, &p.Count) == nil {
				st.TopPosters = append(st.TopPosters, p)
			}
		}
	}
	if st.TopPosters == nil {
		st.TopPosters = []posterStat{}
	}

	// Reaction breakdown
	st.ReactionBreak = make(map[string]int)
	rrows, err := s.db.Query("SELECT emoji, COUNT(*) FROM reactions GROUP BY emoji")
	if err == nil {
		defer rrows.Close()
		for rrows.Next() {
			var emoji string
			var count int
			if rrows.Scan(&emoji, &count) == nil {
				st.ReactionBreak[emoji] = count
			}
		}
	}

	// DB file size
	if s.dbPath != "" {
		if fi, err := os.Stat(s.dbPath); err == nil {
			st.DbSizeBytes = fi.Size()
		}
	}

	return st
}
