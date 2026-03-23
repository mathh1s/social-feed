package feed

import (
	"database/sql"
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
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_posts_id ON posts(id DESC)")
	_, _ = db.Exec("CREATE INDEX IF NOT EXISTS idx_posts_parent ON posts(parent_id)")
	return db, nil
}

type store struct {
	db *sql.DB
}

const postCols = "p.id, p.parent_id, p.author, p.avatar, p.content, p.image, p.preview, p.created_at"

func scanPost(rows *sql.Rows) (post, error) {
	var p post
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
	return collectPosts(rows)
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
	return collectPosts(rows)
}

func (s *store) add(parentID *int, author, avatar, content, image, preview string) (post, error) {
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
		return post{}, err
	}
	id, _ := res.LastInsertId()
	return post{ID: int(id), ParentID: parentID, Author: author, Avatar: avatar, Content: content, Image: image, Preview: preview, CreatedAt: now}, nil
}

func (s *store) exists(id int) bool {
	var n int
	_ = s.db.QueryRow("SELECT 1 FROM posts WHERE id = ?", id).Scan(&n)
	return n == 1
}
