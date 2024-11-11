package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Config struct {
	DBPath     string
	UploadDir  string
	FileExpiry time.Duration
	URLExpiry  time.Duration
}

var config = Config{
	DBPath:     "./data/database.db",
	UploadDir:  "./data/uploads",
	FileExpiry: time.Hour,
	URLExpiry:  time.Hour,
}

type FileMeta struct {
	Path      string
	FileName  string
	ExpiresAt time.Time
}

type URLMeta struct {
	ID          string
	OriginalURL string
	ExpiresAt   time.Time
}

var db *sql.DB
var allowedChars = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

func main() {
	initDB()
	os.MkdirAll(config.UploadDir, os.ModePerm)
	go cleanupExpiredData()
	http.HandleFunc("/upload", handleFileUpload)
	http.HandleFunc("/s/", handleShortURLRedirect)
	http.HandleFunc("/shorten", handleURLShorten)
	http.HandleFunc("/stats", handleStats)
	fmt.Println("Server is running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func initDB() {
	var err error
	db, err = sql.Open("sqlite3", config.DBPath)
	if err != nil {
		log.Fatalf("Error opening database: %v", err)
	}
	createFileTable := `CREATE TABLE IF NOT EXISTS files (
        path TEXT PRIMARY KEY,
        filename TEXT,
        expires_at DATETIME
    );`
	createURLTable := `CREATE TABLE IF NOT EXISTS urls (
        id TEXT PRIMARY KEY,
        original_url TEXT,
        expires_at DATETIME
    );`
	db.Exec(createFileTable)
	db.Exec(createURLTable)
}

func cleanupExpiredData() {
	for {
		time.Sleep(time.Minute)
		now := time.Now()
		rows, err := db.Query("SELECT path FROM files WHERE expires_at <= ?", now)
		if err == nil {
			for rows.Next() {
				var path string
				rows.Scan(&path)
				os.Remove(filepath.Join(config.UploadDir, path))
			}
			rows.Close()
			db.Exec("DELETE FROM files WHERE expires_at <= ?", now)
		}
		db.Exec("DELETE FROM urls WHERE expires_at <= ?", now)
	}
}

func handleFileDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[1:]
	var filename string
	var expiresAt time.Time
	err := db.QueryRow("SELECT filename, expires_at FROM files WHERE path = ?", path).Scan(&filename, &expiresAt)
	if err != nil || time.Now().After(expiresAt) {
		http.NotFound(w, r)
		return
	}
	filePath := filepath.Join(config.UploadDir, path)
	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	w.Header().Set("Content-Type", mimeType)
	http.ServeFile(w, r, filePath)
}

func handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.ParseMultipartForm(10 << 20)
	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Invalid file", http.StatusBadRequest)
		return
	}
	defer file.Close()
	filename := handler.Filename
	extension := filepath.Ext(filename)
	randomName := generateRandomString(8) + extension
	filePath := filepath.Join(config.UploadDir, randomName)
	dst, err := os.Create(filePath)
	if err != nil {
		http.Error(w, "Could not save file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()
	io.Copy(dst, file)
	expiry := config.FileExpiry
	if r.FormValue("long") == "true" {
		expiry = 30 * 24 * time.Hour
	}
	expiresAt := time.Now().Add(expiry)
	db.Exec("INSERT INTO files (path, filename, expires_at) VALUES (?, ?, ?)", randomName, filename, expiresAt)
	downloadURL := fmt.Sprintf("https://%s/%s", r.Host, randomName)
	fmt.Fprintf(w, "File uploaded successfully: %s\n", downloadURL)
}

func handleURLShorten(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var data struct {
		URL  string `json:"url"`
		Long bool   `json:"long"`
	}
	json.NewDecoder(r.Body).Decode(&data)
	if strings.TrimSpace(data.URL) == "" {
		http.Error(w, "Invalid input", http.StatusBadRequest)
		return
	}
	id := generateRandomString(8)
	expiry := config.URLExpiry
	if data.Long {
		expiry = 30 * 24 * time.Hour
	}
	expiresAt := time.Now().Add(expiry)
	db.Exec("INSERT INTO urls (id, original_url, expires_at) VALUES (?, ?, ?)", id, data.URL, expiresAt)
	shortURL := fmt.Sprintf("https://%s/s/%s", r.Host, id)
	fmt.Fprintf(w, "Short URL: %s\n", shortURL)
}

func handleShortURLRedirect(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/s/"):]
	var originalURL string
	var expiresAt time.Time
	err := db.QueryRow("SELECT original_url, expires_at FROM urls WHERE id = ?", id).Scan(&originalURL, &expiresAt)
	if err != nil || time.Now().After(expiresAt) {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, originalURL, http.StatusFound)
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	var fileCount, urlCount int
	db.QueryRow("SELECT COUNT(*) FROM files").Scan(&fileCount)
	db.QueryRow("SELECT COUNT(*) FROM urls").Scan(&urlCount)
	if r.URL.Query().Get("format") == "json" {
		stats := map[string]int{"files": fileCount, "urls": urlCount}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	} else {
		fmt.Fprintf(w, "Files stored: %d\nShort URLs stored: %d\n", fileCount, urlCount)
	}
}

func generateRandomString(length int) string {
	rand.Seed(time.Now().UnixNano())
	b := make([]rune, length)
	for i := range b {
		b[i] = allowedChars[rand.Intn(len(allowedChars))]
	}
	return string(b)
}
