// DISCLAIMER: This code was generated mostly by Claude, an AI assistant created by Anthropic.
// While efforts have been made to ensure the code is accurate and functional,
// it may contain errors or not perform as intended in all environments.
// This code is provided "as is" without warranty of any kind.
// Users should review, test, and validate the code before using it in production environments.
// The user assumes all responsibility for the implementation and use of this code.

package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed assets/html/index.html
var indexHTML []byte

//go:embed assets/css/bootstrap.min.css
var bootstrapCSS []byte

// Configuration struct
type Config struct {
	LogDir          string
	Port            int
	FileRefreshRate int // Milliseconds between file checks
	BufferSize      int64
}

// FileInfo struct for listing files
type FileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// TailCommand struct for WebSocket commands
type TailCommand struct {
	Command   string `json:"command"`
	File      string `json:"file,omitempty"`
	Lines     int    `json:"lines,omitempty"` // Max number of lines to display in the UI
	SearchStr string `json:"searchStr,omitempty"`
}

// TailResponse struct for WebSocket responses
type TailResponse struct {
	Type      string   `json:"type"`
	File      string   `json:"file,omitempty"`
	Lines     []string `json:"lines,omitempty"`
	Error     string   `json:"error,omitempty"`
	SearchStr string   `json:"searchStr,omitempty"`
}

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow all connections
		},
	}
	activeConnections = make(map[*websocket.Conn]bool)
	connectionsMutex  = sync.Mutex{}
	config            Config
)

func main() {
	// Configure logger with timestamp
	log.SetFlags(log.LstdFlags)

	// Parse command line flags
	flag.StringVar(&config.LogDir, "logdir", "", "Directory containing log files")
	flag.IntVar(&config.Port, "port", 8080, "HTTP server port")
	flag.IntVar(&config.FileRefreshRate, "refreshrate", 500, "File check interval in milliseconds")
	flag.Int64Var(&config.BufferSize, "buffersize", 4*1024, "Buffer size for reading file updates (bytes)")
	flag.Parse()

	// Check if logdir is set from environment variable
	if config.LogDir == "" {
		config.LogDir = os.Getenv("LOG_DIR")
		if config.LogDir == "" {
			config.LogDir = "/logs" // Default value
		}
	}

	// Create log directory if it doesn't exist
	if _, err := os.Stat(config.LogDir); os.IsNotExist(err) {
		if err := os.MkdirAll(config.LogDir, 0755); err != nil {
			log.Fatalf("Failed to create log directory: %v", err)
		}
	}

	// Set up HTTP handlers with logging middleware
	loggedMux := http.NewServeMux()
	loggedMux.HandleFunc("/", serveHome)
	loggedMux.HandleFunc("/api/files", listFiles)
	loggedMux.HandleFunc("/ws", handleWebSocket)

	// Add route to serve embedded bootstrap.min.css
	loggedMux.HandleFunc("/assets/css/bootstrap.min.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Write(bootstrapCSS)
	})

	// Create middleware to log HTTP requests
	loggedHandler := logRequestMiddleware(loggedMux)

	// Start HTTP server
	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("Starting WebTail server on %s with log directory: %s", addr, config.LogDir)
	if err := http.ListenAndServe(addr, loggedHandler); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}

// logRequestMiddleware logs all HTTP requests
func logRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("HTTP %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		next.ServeHTTP(w, r)
		log.Printf("HTTP %s %s completed in %v", r.Method, r.URL.Path, time.Since(start))
	})
}

// serveHome handles the home page
func serveHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	if _, err := w.Write(indexHTML); err != nil {
		log.Printf("Error writing response: %v", err)
	}
}

// listFiles handles listing files in the log directory
func listFiles(w http.ResponseWriter, r *http.Request) {
	files, err := os.ReadDir(config.LogDir)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error reading directory: %v", err), http.StatusInternalServerError)
		return
	}

	var fileInfos []FileInfo
	for _, file := range files {
		if file.IsDir() {
			continue // Skip directories
		}
		fileInfo, err := file.Info()
		if err != nil {
			continue // Skip files with errors
		}
		fileInfos = append(fileInfos, FileInfo{
			Name:    fileInfo.Name(),
			Size:    fileInfo.Size(),
			ModTime: fileInfo.ModTime().Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fileInfos); err != nil {
		http.Error(w, fmt.Sprintf("Error encoding JSON: %v", err), http.StatusInternalServerError)
	}
}

// handleWebSocket handles WebSocket connections
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Error upgrading to WebSocket:", err)
		return
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("Error closing WebSocket connection: %v", err)
		}
	}()

	// Register connection
	connectionsMutex.Lock()
	connID := conn.RemoteAddr().String()
	activeConnections[conn] = true
	activeCount := len(activeConnections)
	connectionsMutex.Unlock()

	log.Printf("WebSocket connected from %s (active connections: %d)", connID, activeCount)

	defer func() {
		connectionsMutex.Lock()
		delete(activeConnections, conn)
		remainingCount := len(activeConnections)
		connectionsMutex.Unlock()
		log.Printf("WebSocket disconnected from %s (active connections: %d)", connID, remainingCount)
	}()

	// Send initial file list
	files, _ := getFileList()
	response := TailResponse{
		Type:  "files",
		Lines: files,
	}
	if err := conn.WriteJSON(response); err != nil {
		log.Println("Error sending file list:", err)
		return
	}

	// Handle WebSocket messages
	var tailCancel chan bool
	var wg sync.WaitGroup

	for {
		var cmd TailCommand
		if err := conn.ReadJSON(&cmd); err != nil {
			log.Println("Error reading JSON:", err)
			break
		}

		// Log the received command
		cmdDetails := cmd.Command
		if cmd.File != "" {
			cmdDetails += fmt.Sprintf(" (file: %s)", cmd.File)
		}
		if cmd.SearchStr != "" {
			cmdDetails += fmt.Sprintf(" (search: %s)", cmd.SearchStr)
		}
		if cmd.Lines > 0 {
			cmdDetails += fmt.Sprintf(" (lines: %d)", cmd.Lines)
		}
		log.Printf("WebSocket command from %s: %s", connID, cmdDetails)

		switch cmd.Command {
		case "list":
			files, err := getFileList()
			if err != nil {
				sendError(conn, "Failed to list files: "+err.Error())
				continue
			}
			response := TailResponse{
				Type:  "files",
				Lines: files,
			}
			if err := conn.WriteJSON(response); err != nil {
				log.Println("Error sending file list:", err)
			}
			// log.Printf("WebSocket sent file list to %s (%d files)", connID, len(files))

		case "tail":
			// Cancel previous tail if any
			if tailCancel != nil {
				tailCancel <- true
				wg.Wait()
				log.Printf("WebSocket previous tail canceled for %s", connID)
			}

			// Start new tail
			tailCancel = make(chan bool)
			wg.Add(1)
			log.Printf("WebSocket starting tail for %s (file: %s)", connID, cmd.File)
			go func(file string, cancel chan bool) {
				defer wg.Done()
				tailFile(conn, file, cancel, connID)
			}(cmd.File, tailCancel)

		case "stop":
			// Stop current tail if any
			if tailCancel != nil {
				tailCancel <- true
				wg.Wait()
				tailCancel = nil
				log.Printf("WebSocket tail stopped for %s", connID)

				// Send acknowledgment
				response := TailResponse{
					Type: "stopped",
				}
				if err := conn.WriteJSON(response); err != nil {
					log.Println("Error sending stop acknowledgment:", err)
				}
			}

		case "search":
			// Update search in current tail
			if tailCancel != nil {
				// This doesn't interrupt the current tail, just updates the search
				response := TailResponse{
					Type:      "search",
					SearchStr: cmd.SearchStr,
				}
				if err := conn.WriteJSON(response); err != nil {
					log.Println("Error sending search update:", err)
				}
				log.Printf("WebSocket search updated for %s (search: %s)", connID, cmd.SearchStr)
			}
		}
	}

	// Cancel tail on connection close
	if tailCancel != nil {
		tailCancel <- true
		wg.Wait()
	}
}

// getFileList returns a list of files in the log directory
func getFileList() ([]string, error) {
	files, err := os.ReadDir(config.LogDir)
	if err != nil {
		return nil, err
	}

	var fileNames []string
	for _, file := range files {
		if !file.IsDir() {
			fileNames = append(fileNames, file.Name())
		}
	}
	return fileNames, nil
}

func tailFile(conn *websocket.Conn, fileName string, cancel chan bool, connID string) {
	filePath := filepath.Join(config.LogDir, fileName)
	file, err := os.Open(filePath)
	if err != nil {
		sendError(conn, "Failed to open file: "+err.Error())
		log.Printf("WebSocket error for %s: Failed to open file %s: %v", connID, fileName, err)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("Error closing file: %v", err)
		}
	}()

	// Get initial file size
	fileInfo, err := file.Stat()
	if err != nil {
		sendError(conn, "Failed to stat file: "+err.Error())
		log.Printf("WebSocket error for %s: Failed to stat file %s: %v", connID, fileName, err)
		return
	}
	fileSize := fileInfo.Size()

	// Send initial buffer content
	initialBuffer, err := readLastBuffer(file, fileSize)
	if err != nil {
		sendError(conn, "Failed to read file: "+err.Error())
		log.Printf("WebSocket error for %s: Failed to read file %s: %v", connID, fileName, err)
		return
	}

	response := TailResponse{
		Type:  "tail",
		File:  fileName,
		Lines: []string{initialBuffer},
	}
	if err := conn.WriteJSON(response); err != nil {
		log.Println("Error sending initial buffer:", err)
		return
	}

	// Set current position to end of file for watching new updates
	currentPos := fileSize

	// Watch for file changes
	ticker := time.NewTicker(time.Duration(config.FileRefreshRate) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Check if file has been updated
			fileInfo, err := file.Stat()
			if err != nil {
				sendError(conn, "Failed to stat file: "+err.Error())
				log.Printf("WebSocket error for %s: Failed to stat file during tail %s: %v", connID, fileName, err)
				return
			}
			newSize := fileInfo.Size()

			if newSize > currentPos {
				// File has grown, read only the new content
				_, err := file.Seek(currentPos, io.SeekStart)
				if err != nil {
					sendError(conn, "Failed to seek in file: "+err.Error())
					log.Printf("WebSocket error for %s: Failed to seek in file %s: %v", connID, fileName, err)
					return
				}

				// Read only the new bytes, but limit to config.BufferSize
				bytesToRead := min(newSize-currentPos, config.BufferSize)
				buffer := make([]byte, bytesToRead)
				bytesRead, err := file.Read(buffer)
				if err != nil && err != io.EOF {
					sendError(conn, "Failed to read file: "+err.Error())
					log.Printf("WebSocket error for %s: Failed to read file %s: %v", connID, fileName, err)
					return
				}

				response := TailResponse{
					Type:  "update",
					File:  fileName,
					Lines: []string{string(buffer[:bytesRead])},
				}
				if err := conn.WriteJSON(response); err != nil {
					log.Println("Error sending updates:", err)
					log.Printf("WebSocket error for %s: Failed to send updates for %s: %v", connID, fileName, err)
					return
				}

				currentPos = newSize
			} else if newSize < currentPos {
				// File has been truncated, reset and read from beginning
				currentPos = 0
				_, err := file.Seek(0, io.SeekStart)
				if err != nil {
					sendError(conn, "Failed to seek to beginning of file: "+err.Error())
					log.Printf("WebSocket error for %s: Failed to seek to beginning of %s after truncation: %v", connID, fileName, err)
					return
				}

				initialBuffer, err := readLastBuffer(file, newSize)
				if err != nil {
					sendError(conn, "Failed to read file after truncation: "+err.Error())
					log.Printf("WebSocket error for %s: Failed to read %s after truncation: %v", connID, fileName, err)
					return
				}

				response := TailResponse{
					Type:  "tail",
					File:  fileName,
					Lines: []string{initialBuffer},
				}
				if err := conn.WriteJSON(response); err != nil {
					log.Println("Error sending reset after truncation:", err)
					log.Printf("WebSocket error for %s: Failed to send reset after truncation of %s: %v", connID, fileName, err)
					return
				}
				log.Printf("WebSocket file %s was truncated, reset and sent buffer to %s", fileName, connID)

				currentPos = newSize
			}

		case <-cancel:
			// Cancel the tail
			log.Printf("WebSocket tail of %s canceled for %s", fileName, connID)
			return
		}
	}
}

// readLastBuffer reads the last buffer from the file
func readLastBuffer(file *os.File, fileSize int64) (string, error) {
	if fileSize == 0 {
		return "", nil
	}

	bufferSize := config.BufferSize
	if bufferSize > fileSize {
		bufferSize = fileSize
	}

	buffer := make([]byte, bufferSize)
	offset := max(0, fileSize-bufferSize)
	_, err := file.Seek(offset, io.SeekStart)
	if err != nil {
		return "", fmt.Errorf("failed to seek to offset %d: %w", offset, err)
	}

	bytesRead, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("failed to read buffer: %w", err)
	}

	return string(buffer[:bytesRead]), nil
}

// sendError sends an error message over WebSocket
func sendError(conn *websocket.Conn, message string) {
	response := TailResponse{
		Type:  "error",
		Error: message,
	}
	if err := conn.WriteJSON(response); err != nil {
		log.Printf("Error sending error message: %v", err)
	}
}

// Helper function to get the maximum of two int64 values
func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Helper function to get the minimum of two int64 values
func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
