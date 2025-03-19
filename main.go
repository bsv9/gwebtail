// DISCLAIMER: This code was generated mostly by Claude, an AI assistant created by Anthropic.
// While efforts have been made to ensure the code is accurate and functional,
// it may contain errors or not perform as intended in all environments.
// This code is provided "as is" without warranty of any kind.
// Users should review, test, and validate the code before using it in production environments.
// The user assumes all responsibility for the implementation and use of this code.

package main

import (
	"bufio"
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

//go:embed index.html
var indexHTML []byte

// Configuration struct
type Config struct {
	LogDir          string
	Port            int
	MaxLines        int
	MaxLineSize     int   // Maximum size of a single line
	ChunkSize       int64 // Chunk size for reading large files
	FileRefreshRate int   // Milliseconds between file checks
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
	// Parse command line flags
	flag.StringVar(&config.LogDir, "logdir", "", "Directory containing log files")
	flag.IntVar(&config.Port, "port", 8080, "HTTP server port")
	flag.IntVar(&config.MaxLines, "maxlines", 1000, "Maximum number of lines to display on screen")
	flag.IntVar(&config.MaxLineSize, "maxlinesize", 8192, "Maximum size of a single line in bytes")
	flag.Int64Var(&config.ChunkSize, "chunksize", 4*1024*1024, "Chunk size for reading large files (bytes)")
	flag.IntVar(&config.FileRefreshRate, "refreshrate", 500, "File check interval in milliseconds")
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

	// HTTP handlers
	http.HandleFunc("/", serveHome)
	http.HandleFunc("/api/files", listFiles)
	http.HandleFunc("/ws", handleWebSocket)

	// Start HTTP server
	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("Starting WebTail server on %s with log directory: %s", addr, config.LogDir)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
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
	activeConnections[conn] = true
	connectionsMutex.Unlock()
	defer func() {
		connectionsMutex.Lock()
		delete(activeConnections, conn)
		connectionsMutex.Unlock()
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

		case "tail":
			// Ensure lines is within limits
			if cmd.Lines <= 0 {
				cmd.Lines = 100 // Default
			}
			if cmd.Lines > config.MaxLines {
				cmd.Lines = config.MaxLines // Cap at maximum
			}

			// Cancel previous tail if any
			if tailCancel != nil {
				tailCancel <- true
				wg.Wait()
			}

			// Start new tail
			tailCancel = make(chan bool)
			wg.Add(1)
			go func(file string, lines int, searchStr string, cancel chan bool) {
				defer wg.Done()
				tailFile(conn, file, lines, searchStr, cancel)
			}(cmd.File, cmd.Lines, cmd.SearchStr, tailCancel)

		case "stop":
			// Stop current tail if any
			if tailCancel != nil {
				tailCancel <- true
				wg.Wait()
				tailCancel = nil

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

// tailFile tails a file and sends updates over WebSocket
func tailFile(conn *websocket.Conn, fileName string, lines int, searchStr string, cancel chan bool) {
	if lines <= 0 {
		lines = 100 // Default number of lines
	}
	if lines > config.MaxLines {
		lines = config.MaxLines // Cap at maximum
	}
	filePath := filepath.Join(config.LogDir, fileName)
	file, err := os.Open(filePath)
	if err != nil {
		sendError(conn, "Failed to open file: "+err.Error())
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
		return
	}
	fileSize := fileInfo.Size()

	// Send initial lines - memory optimized for large files
	initialLines, err := readLastLinesOptimized(file, lines, fileSize)
	if err != nil {
		sendError(conn, "Failed to read file: "+err.Error())
		return
	}

	response := TailResponse{
		Type:      "tail",
		File:      fileName,
		Lines:     initialLines,
		SearchStr: searchStr,
	}
	if err := conn.WriteJSON(response); err != nil {
		log.Println("Error sending initial lines:", err)
		return
	}

	// Set current position to end of file for watching new lines
	currentPos := fileSize

	// Watch for file changes
	ticker := time.NewTicker(time.Duration(config.FileRefreshRate) * time.Millisecond)
	defer ticker.Stop()

	// Buffer for reading new lines
	reader := bufio.NewReaderSize(file, config.MaxLineSize)

	for {
		select {
		case <-ticker.C:
			// Check if file has been updated
			fileInfo, err := file.Stat()
			if err != nil {
				sendError(conn, "Failed to stat file: "+err.Error())
				return
			}
			newSize := fileInfo.Size()

			if newSize > currentPos {
				// File has grown, read new content
				_, err := file.Seek(currentPos, io.SeekStart)
				if err != nil {
					sendError(conn, "Failed to seek in file: "+err.Error())
					return
				}

				// Reset the reader to use the current file position
				reader.Reset(file)

				// Read new lines with size limit to avoid memory problems
				var newLines []string
				var totalBytes int64
				const maxBatchBytes int64 = 1024 * 1024 // 1MB max per update to prevent memory spikes

				for totalBytes < maxBatchBytes {
					line, err := reader.ReadString('\n')
					if err != nil {
						if err == io.EOF {
							// Add the last line if it's not empty
							if len(line) > 0 {
								newLines = append(newLines, line)
								totalBytes += int64(len(line))
							}
							break
						}
						log.Printf("Error reading line: %v", err)
						break
					}

					// Remove trailing newline
					if len(line) > 0 && line[len(line)-1] == '\n' {
						line = line[:len(line)-1]
					}

					newLines = append(newLines, line)
					totalBytes += int64(len(line))

					// Check if we have read everything
					currentFilePos, _ := file.Seek(0, io.SeekCurrent)
					if currentFilePos >= newSize {
						break
					}
				}

				if len(newLines) > 0 {
					// Update current position
					currentPos, _ = file.Seek(0, io.SeekCurrent)

					response := TailResponse{
						Type:      "update",
						File:      fileName,
						Lines:     newLines,
						SearchStr: searchStr,
					}
					if err := conn.WriteJSON(response); err != nil {
						log.Println("Error sending updates:", err)
						return
					}
				}
			} else if newSize < currentPos {
				// File has been truncated, reset and read from beginning
				currentPos = 0
				_, err := file.Seek(0, io.SeekStart)
				if err != nil {
					sendError(conn, "Failed to seek to beginning of file: "+err.Error())
					return
				}

				// Reset reader
				reader.Reset(file)

				initialLines, err := readLastLinesOptimized(file, lines, newSize)
				if err != nil {
					sendError(conn, "Failed to read file after truncation: "+err.Error())
					return
				}

				response := TailResponse{
					Type:      "tail",
					File:      fileName,
					Lines:     initialLines,
					SearchStr: searchStr,
				}
				if err := conn.WriteJSON(response); err != nil {
					log.Println("Error sending reset after truncation:", err)
					return
				}

				currentPos = newSize
			}

		case <-cancel:
			// Cancel the tail
			return
		}
	}
}

// readLastLinesOptimized reads the last n lines from a file without loading the entire file
// This is optimized for large files by reading chunks from the end
func readLastLinesOptimized(file *os.File, n int, fileSize int64) ([]string, error) {
	if fileSize == 0 {
		return []string{}, nil
	}

	// For small files (< 1MB), just read the whole file normally
	if fileSize < 1024*1024 {
		_, err := file.Seek(0, io.SeekStart)
		if err != nil {
			return nil, err
		}

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, config.MaxLineSize), config.MaxLineSize)

		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}

		if err := scanner.Err(); err != nil {
			return nil, err
		}

		// Return last n lines or all lines if fewer
		if len(lines) <= n {
			return lines, nil
		}
		return lines[len(lines)-n:], nil
	}

	// For large files, read chunks from the end to find the last n lines
	var lines []string
	var readBytes int64
	chunkSize := config.ChunkSize

	// Start with a reasonable estimate of how many bytes we need to read
	// to get n lines (assume average line length of 100 bytes)
	estimatedBytes := int64(n * 100)
	if estimatedBytes > fileSize {
		estimatedBytes = fileSize
	}

	// Read in chunks from the end of the file
	for len(lines) < n && readBytes < fileSize {
		// Determine chunk size for this iteration
		if readBytes+chunkSize > fileSize {
			chunkSize = fileSize - readBytes
		}

		// Create buffer for this chunk
		buf := make([]byte, chunkSize)
		offset := fileSize - readBytes - chunkSize

		// Seek to the right position and read the chunk
		_, err := file.Seek(offset, io.SeekStart)
		if err != nil {
			return nil, err
		}

		bytesRead, err := io.ReadFull(file, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, err
		}

		// Find line breaks in the chunk
		var temp []string
		start := bytesRead - 1

		// Process the chunk from end to start
		for i := bytesRead - 1; i >= 0; i-- {
			if buf[i] == '\n' || i == 0 {
				lineStart := i
				if buf[i] == '\n' {
					lineStart = i + 1
				}

				// Extract the line (if not at the start of chunk or if it's the first chunk)
				if lineStart <= start && (offset > 0 || i > 0) {
					line := string(buf[lineStart : start+1])
					temp = append(temp, line)
				}
				start = i - 1
			}
		}

		// If we're at the beginning of the file and there's text before the first newline
		if offset == 0 && start >= 0 {
			temp = append(temp, string(buf[0:start+1]))
		}

		// Reverse the temporary array (since we read backwards)
		for i := len(temp) - 1; i >= 0; i-- {
			lines = append([]string{temp[i]}, lines...)
		}

		// Update read bytes and maybe increase chunk size for next iteration
		readBytes += int64(bytesRead)

		// If we haven't found enough lines yet, increase chunk size
		if len(lines) < n && chunkSize < fileSize/2 {
			chunkSize *= 2
		}

		// Break if we've read the whole file
		if offset == 0 {
			break
		}
	}

	// Ensure we don't return more lines than requested
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	// Reset file position to the start
	_, err := file.Seek(0, io.SeekStart)
	if err != nil {
		return nil, err
	}

	return lines, nil
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
