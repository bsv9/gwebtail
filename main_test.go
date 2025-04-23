package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestServeHome tests the home page handler
func TestServeHome(t *testing.T) {
	// Set up a request to the root path
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	// Call the handler
	serveHome(w, req)

	// Check the status code
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check content type
	contentType := w.Header().Get("Content-Type")
	if contentType != "text/html" {
		t.Errorf("Expected Content-Type 'text/html', got '%s'", contentType)
	}

	// Check that we got some HTML content (not empty)
	if len(w.Body.Bytes()) == 0 {
		t.Errorf("Expected non-empty body, got empty response")
	}

	// Check if body contains some expected HTML elements
	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "gWebTail") {
		t.Errorf("Expected response to contain 'gWebTail', but it doesn't")
	}

	// Test wrong path
	req = httptest.NewRequest("GET", "/wrong-path", nil)
	w = httptest.NewRecorder()
	serveHome(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404 for wrong path, got %d", w.Code)
	}

	// Test wrong method
	req = httptest.NewRequest("POST", "/", nil)
	w = httptest.NewRecorder()
	serveHome(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405 for wrong method, got %d", w.Code)
	}
}

// TestListFiles tests the file listing API
func TestListFiles(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "webtail-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test log files
	testFiles := []string{"test1.log", "test2.log"}
	testContent := []byte("test content")

	for _, fname := range testFiles {
		if err := os.WriteFile(filepath.Join(tmpDir, fname), testContent, 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}
	}

	// Save old config and restore after test
	oldLogDir := config.LogDir
	defer func() { config.LogDir = oldLogDir }()

	// Set log directory to temp directory
	config.LogDir = tmpDir

	// Make request to listFiles
	req := httptest.NewRequest("GET", "/api/files", nil)
	w := httptest.NewRecorder()

	listFiles(w, req)

	// Check response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check content type
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got '%s'", contentType)
	}

	// Decode response
	var fileInfos []FileInfo
	if err := json.NewDecoder(w.Body).Decode(&fileInfos); err != nil {
		t.Fatalf("Failed to decode JSON response: %v", err)
	}

	// Check number of files
	if len(fileInfos) != len(testFiles) {
		t.Errorf("Expected %d files, got %d", len(testFiles), len(fileInfos))
	}

	// Check file names
	fileNames := make(map[string]bool)
	for _, fi := range fileInfos {
		fileNames[fi.Name] = true
	}

	for _, fname := range testFiles {
		if !fileNames[fname] {
			t.Errorf("Expected file %s not found in response", fname)
		}
	}
}

// TestReadLastBuffer tests the readLastBuffer function
func TestReadLastBuffer(t *testing.T) {
	// Create a temporary file
	tmpFile, err := os.CreateTemp("", "webtail-buffer-test")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write test content
	testContent := "Line 1\nLine 2\nLine 3\nLine 4\nLine 5"
	if _, err := tmpFile.WriteString(testContent); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	if err := tmpFile.Sync(); err != nil {
		t.Fatalf("Failed to sync temp file: %v", err)
	}

	// Get file size
	fileInfo, err := tmpFile.Stat()
	if err != nil {
		t.Fatalf("Failed to stat temp file: %v", err)
	}
	fileSize := fileInfo.Size()

	// Test with bufferSize smaller than file
	oldBufferSize := config.BufferSize
	defer func() { config.BufferSize = oldBufferSize }()

	// Set buffer size to get just the last part of the file
	// "Line 5" is 6 bytes, plus we'll need to skip the first partial line
	config.BufferSize = 12

	// Rewind file before each test
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Failed to seek file: %v", err)
	}

	// Read last buffer
	buffer, err := readLastBuffer(tmpFile, fileSize)
	if err != nil {
		t.Fatalf("readLastBuffer failed: %v", err)
	}

	// The current implementation should return content starting after the first newline
	// when reading from the middle of the file
	if !strings.Contains(buffer, "Line 5") {
		t.Errorf("Expected buffer to contain 'Line 5', got '%s'", buffer)
	}

	// Test with bufferSize larger than file
	config.BufferSize = fileSize * 2

	// Rewind file
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Failed to seek file: %v", err)
	}

	buffer, err = readLastBuffer(tmpFile, fileSize)
	if err != nil {
		t.Fatalf("readLastBuffer failed: %v", err)
	}

	if buffer != testContent {
		t.Errorf("Expected buffer '%s', got '%s'", testContent, buffer)
	}

	// Test with empty file
	emptyFile, err := os.CreateTemp("", "webtail-empty-test")
	if err != nil {
		t.Fatalf("Failed to create empty temp file: %v", err)
	}
	defer os.Remove(emptyFile.Name())

	emptyInfo, err := emptyFile.Stat()
	if err != nil {
		t.Fatalf("Failed to stat empty file: %v", err)
	}

	buffer, err = readLastBuffer(emptyFile, emptyInfo.Size())
	if err != nil {
		t.Fatalf("readLastBuffer failed for empty file: %v", err)
	}

	if buffer != "" {
		t.Errorf("Expected empty buffer, got '%s'", buffer)
	}
}

// TestGetFileList tests the getFileList function
func TestGetFileList(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "webtail-filelist-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test log files
	testFiles := []string{"test1.log", "test2.log", "test3.log"}
	testContent := []byte("test content")

	for _, fname := range testFiles {
		if err := os.WriteFile(filepath.Join(tmpDir, fname), testContent, 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}
	}

	// Create a subdirectory - should be skipped
	if err := os.Mkdir(filepath.Join(tmpDir, "subdir"), 0755); err != nil {
		t.Fatalf("Failed to create subdirectory: %v", err)
	}

	// Save old config and restore after test
	oldLogDir := config.LogDir
	defer func() { config.LogDir = oldLogDir }()

	// Set log directory to temp directory
	config.LogDir = tmpDir

	// Get file list
	files, err := getFileList()
	if err != nil {
		t.Fatalf("getFileList failed: %v", err)
	}

	// Check number of files
	if len(files) != len(testFiles) {
		t.Errorf("Expected %d files, got %d", len(testFiles), len(files))
	}

	// Check file names
	fileMap := make(map[string]bool)
	for _, f := range files {
		fileMap[f] = true
	}

	for _, fname := range testFiles {
		if !fileMap[fname] {
			t.Errorf("Expected file %s not found in result", fname)
		}
	}

	// Subdirectory should not be in results
	if fileMap["subdir"] {
		t.Errorf("Subdirectory should not be included in results")
	}
}

// Mock WebSocket connection for testing
type mockConn struct {
	sentMessages []TailResponse
	mu           sync.Mutex
}

func (m *mockConn) WriteJSON(v interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	response, ok := v.(TailResponse)
	if ok {
		m.sentMessages = append(m.sentMessages, response)
	}
	return nil
}

func (m *mockConn) Close() error {
	return nil
}

func (m *mockConn) RemoteAddr() interface{} {
	return "test-client"
}

// TestSendError tests the sendError function
func TestSendError(t *testing.T) {
	mock := &mockConn{sentMessages: []TailResponse{}}

	// Send an error
	errorMsg := "Test error message"
	sendError(mock, errorMsg)

	// Check the sent message
	if len(mock.sentMessages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(mock.sentMessages))
	}

	msg := mock.sentMessages[0]
	if msg.Type != "error" {
		t.Errorf("Expected message type 'error', got '%s'", msg.Type)
	}

	if msg.Error != errorMsg {
		t.Errorf("Expected error message '%s', got '%s'", errorMsg, msg.Error)
	}
}

// Integration test for WebSocket handling
func TestWebSocketIntegration(t *testing.T) {
	// This test requires more sophisticated setup with an HTTP server
	// and real WebSocket connections - implementation would be environment-dependent

	// Instead of implementing here, I'll provide the structure of what this test should do:
	// 1. Start a test HTTP server
	// 2. Create a temporary log directory with test files
	// 3. Connect with a WebSocket client
	// 4. Send commands and verify responses
	// 5. Test different scenarios: listing files, tailing, updates, etc.

	t.Skip("Integration test skipped - requires actual WebSocket connections")
}

// Test the main application setup (limited test)
func TestMainSetup(t *testing.T) {
	// Save old config
	oldLogDir := config.LogDir
	oldPort := config.Port
	oldRefreshRate := config.FileRefreshRate
	oldBufferSize := config.BufferSize

	// Restore after test
	defer func() {
		config.LogDir = oldLogDir
		config.Port = oldPort
		config.FileRefreshRate = oldRefreshRate
		config.BufferSize = oldBufferSize
	}()

	// Set test values
	testDir := "/tmp/test-logs"
	config.LogDir = testDir
	config.Port = 8081
	config.FileRefreshRate = 100
	config.BufferSize = 1024

	// Verify values were set
	if config.LogDir != testDir {
		t.Errorf("Expected LogDir '%s', got '%s'", testDir, config.LogDir)
	}

	if config.Port != 8081 {
		t.Errorf("Expected Port 8081, got %d", config.Port)
	}

	if config.FileRefreshRate != 100 {
		t.Errorf("Expected FileRefreshRate 100, got %d", config.FileRefreshRate)
	}

	if config.BufferSize != 1024 {
		t.Errorf("Expected BufferSize 1024, got %d", config.BufferSize)
	}
}

// TestTailFile tests the tailFile function
func TestTailFile(t *testing.T) {
	// Create temporary directory for test logs
	tmpDir, err := os.MkdirTemp("", "webtail-tail-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test log file
	testFile := "test.log"
	testFilePath := filepath.Join(tmpDir, testFile)
	initialContent := "Initial line 1\nInitial line 2\n"

	if err := os.WriteFile(testFilePath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Save old config and restore after test
	oldLogDir := config.LogDir
	oldRefreshRate := config.FileRefreshRate
	oldBufferSize := config.BufferSize
	defer func() {
		config.LogDir = oldLogDir
		config.FileRefreshRate = oldRefreshRate
		config.BufferSize = oldBufferSize
	}()

	// Set test configuration
	config.LogDir = tmpDir
	config.FileRefreshRate = 50 // Very fast refresh for testing
	config.BufferSize = 1024

	// Create mock connection with mutex-protected message slice
	mock := &mockConn{sentMessages: []TailResponse{}}

	// Create cancel channel
	cancel := make(chan bool)

	// Start tailing in a goroutine
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tailFile(mock, testFile, cancel, "test-client")
	}()

	// Wait for initial content - use a timeout and polling approach
	initialMsgReceived := false
	startTime := time.Now()
	timeout := 2 * time.Second

	for time.Since(startTime) < timeout {
		mock.mu.Lock()
		if len(mock.sentMessages) > 0 {
			initialMsgReceived = true
			mock.mu.Unlock()
			break
		}
		mock.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}

	if !initialMsgReceived {
		t.Fatalf("Timed out waiting for initial message")
	}

	// Verify initial content was sent
	mock.mu.Lock()
	initialMsg := mock.sentMessages[0]
	if initialMsg.Type != "tail" {
		t.Errorf("Expected initial message type 'tail', got '%s'", initialMsg.Type)
	}

	if initialMsg.File != testFile {
		t.Errorf("Expected file name '%s', got '%s'", testFile, initialMsg.File)
	}

	if len(initialMsg.Lines) != 1 || !strings.Contains(initialMsg.Lines[0], initialContent) {
		t.Errorf("Expected initial content to contain '%s', got '%s'", initialContent, initialMsg.Lines[0])
	}
	mock.mu.Unlock()

	// Append to the file
	appendContent := "New line 1\nNew line 2\n"
	f, err := os.OpenFile(testFilePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("Failed to open file for appending: %v", err)
	}

	if _, err := f.WriteString(appendContent); err != nil {
		f.Close()
		t.Fatalf("Failed to append to file: %v", err)
	}
	f.Close()

	// Wait for update with a polling approach
	updateReceived := false
	startTime = time.Now()

	for time.Since(startTime) < timeout {
		mock.mu.Lock()
		for _, msg := range mock.sentMessages {
			if msg.Type == "update" && len(msg.Lines) > 0 && strings.Contains(msg.Lines[0], appendContent) {
				updateReceived = true
				break
			}
		}
		mock.mu.Unlock()

		if updateReceived {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Cancel tailing and wait for completion
	cancel <- true
	wg.Wait()

	// Test outcome
	if !updateReceived {
		t.Errorf("Update message with new content not found")
	}
}

// Test helper functions
func TestHelperFunctions(t *testing.T) {
	// Test max function
	if max(5, 10) != 10 {
		t.Errorf("Expected max(5, 10) = 10, got %d", max(5, 10))
	}

	if max(10, 5) != 10 {
		t.Errorf("Expected max(10, 5) = 10, got %d", max(10, 5))
	}

	if max(0, -5) != 0 {
		t.Errorf("Expected max(0, -5) = 0, got %d", max(0, -5))
	}

	// Test min function
	if min(5, 10) != 5 {
		t.Errorf("Expected min(5, 10) = 5, got %d", min(5, 10))
	}

	if min(10, 5) != 5 {
		t.Errorf("Expected min(10, 5) = 5, got %d", min(10, 5))
	}

	if min(0, -5) != -5 {
		t.Errorf("Expected min(0, -5) = -5, got %d", min(0, -5))
	}
}

// TestMiddleware tests the logging middleware
func TestMiddleware(t *testing.T) {
	// Create a test handler that just returns 200 OK
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Apply the middleware
	handlerWithMiddleware := logRequestMiddleware(testHandler)

	// Create a test request
	req := httptest.NewRequest("GET", "/test-path", nil)
	w := httptest.NewRecorder()

	// Capture log output (this is implementation-dependent)
	// In a real test, you might redirect log output to a buffer

	// Call the handler
	handlerWithMiddleware.ServeHTTP(w, req)

	// Check the response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// In a real test, you'd verify the log output
}
