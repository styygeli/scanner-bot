package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// Config variables (set via flags)
var (
	WatchDir  string
	DestDir   string
	ModelName string
)

// ReceiptData maps the JSON response from Gemini
type ReceiptData struct {
	Date     string `json:"date"`
	Vendor   string `json:"vendor"`
	Category string `json:"category"`
	Amount   int    `json:"total_amount"`
}

// Global tracker to prevent double-processing
var activeFiles sync.Map

func main() {
	// Parse command line flags
	// Defaults are empty to force user input, ensuring no personal data is hardcoded.
	flag.StringVar(&WatchDir, "watch", "", "Directory to watch for new files (Required)")
	flag.StringVar(&DestDir, "dest", "", "Root directory for processed documents (Required)")
	flag.StringVar(&ModelName, "model", "gemini-3-flash-preview", "Gemini model to use")
	flag.Parse()

	// Validation: Ensure required flags are provided
	if WatchDir == "" || DestDir == "" {
		fmt.Println("Error: You must provide both -watch and -dest directories.")
		fmt.Println("Usage:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// 1. Setup Gemini Client
	ctx := context.Background()
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("Error: GEMINI_API_KEY environment variable not set")
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	// 2. Setup File Watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	done := make(chan bool)

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}

				// Trigger on Write or Create events
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					// DEDUPLICATION: Check if we are already handling this file
					if _, loaded := activeFiles.LoadOrStore(event.Name, true); loaded {
						continue
					}
					// Start processing in a new thread
					go processFileWithDelay(ctx, client, event.Name)
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("Watcher error:", err)
			}
		}
	}()

	if err := watcher.Add(WatchDir); err != nil {
		log.Fatalf("Failed to watch directory: %v", err)
	}
	log.Printf("Scanner Bot Started")
	log.Printf("Watching: %s", WatchDir)
	log.Printf("Output:   %s", DestDir)
	log.Printf("Model:    %s", ModelName)
	<-done
}

func processFileWithDelay(ctx context.Context, client *genai.Client, path string) {
	defer activeFiles.Delete(path)

	// DEBOUNCE: Wait 5s for scanner to finish writing
	time.Sleep(5 * time.Second)

	// Filter valid extensions
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".pdf" {
		return
	}

	// Verify file exists
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return
	}

	// Growth check (ensure write is complete)
	initialSize := info.Size()
	time.Sleep(1 * time.Second)
	finalInfo, err := os.Stat(path)
	if err == nil && finalInfo.Size() != initialSize {
		log.Printf("File %s is still growing, skipping.", path)
		return
	}

	log.Printf("Processing: %s", path)

	// Open file for upload
	f, err := os.Open(path)
	if err != nil {
		log.Printf("Error opening file: %v", err)
		return
	}
	defer f.Close()

	// Upload to Gemini
	model := client.GenerativeModel(ModelName)
	model.ResponseMIMEType = "application/json"

	upFile, err := client.UploadFile(ctx, "", f, nil)
	if err != nil {
		log.Printf("Upload failed: %v", err)
		return
	}
	defer client.DeleteFile(ctx, upFile.Name)

	// Wait for processing
	for upFile.State == genai.FileStateProcessing {
		time.Sleep(1 * time.Second)
		upFile, err = client.GetFile(ctx, upFile.Name)
		if err != nil {
			log.Printf("Check failed: %v", err)
			return
		}
	}

	if upFile.State != genai.FileStateActive {
		log.Printf("File processing failed state: %s", upFile.State)
		return
	}

	// Generate Content
	prompt := `Analyze this Japanese receipt. Extract JSON with these keys: 
    "date" (YYYY-MM-DD), 
    "vendor" (Japanese name, if medical use clinic name), 
    "category" (Medical, Grocery, Tax, Utilities, Other), 
    "total_amount" (integer).`

	resp, err := model.GenerateContent(ctx, genai.FileData{URI: upFile.URI}, genai.Text(prompt))
	if err != nil {
		log.Printf("Gemini error: %v", err)
		return
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return
	}

	var jsonText string
	if txt, ok := resp.Candidates[0].Content.Parts[0].(genai.Text); ok {
		jsonText = string(txt)
	}

	var data ReceiptData
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		log.Printf("JSON parse error: %v", err)
		return
	}

	finalizeFiles(path, data)
}

func finalizeFiles(srcPath string, data ReceiptData) {
	// --- PREPARE PATHS ---
	vendor := strings.ReplaceAll(data.Vendor, " ", "")
	vendor = strings.ReplaceAll(vendor, "/", "-")
	if data.Date == "" {
		data.Date = time.Now().Format("2006-01-02")
	}
	if data.Category == "" {
		data.Category = "Unsorted"
	}

	processedFileName := fmt.Sprintf("%s_%s_%d円%s", data.Date, vendor, data.Amount, filepath.Ext(srcPath))
	processedDir := filepath.Join(DestDir, data.Category)
	processedPath := filepath.Join(processedDir, processedFileName)

	originalsDir := filepath.Join(DestDir, "originals")
	originalName := filepath.Base(srcPath)
	originalsPath := filepath.Join(originalsDir, originalName)

	// Create directories
	os.MkdirAll(processedDir, 0755)
	os.MkdirAll(originalsDir, 0755)

	// --- STEP 1: Copy to Processed Location ---
	if err := robustCopy(srcPath, processedPath); err != nil {
		log.Printf("Failed to copy to processed folder: %v", err)
		return
	}
	log.Printf("Saved processed file: %s", processedPath)

	// --- STEP 2: Move Source to Originals ---
	if err := robustMove(srcPath, originalsPath); err != nil {
		log.Printf("Failed to move to originals: %v", err)
	} else {
		log.Printf("Archived original to: %s", originalsPath)
	}
}

func robustCopy(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

func robustMove(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "cross-device link") {
		return err
	}
	if err := robustCopy(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}
