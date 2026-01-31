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

// --- CONFIGURATION ---
const (
        ModelName = "gemini-3-flash-preview"
)

var (
        // Configurable paths via flags
        watchDir string
        destDir  string
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
        // 0. Parse Flags
        flag.StringVar(&watchDir, "watch", "", "Directory to watch for new receipts (required)")
        flag.StringVar(&destDir, "dest", "", "Directory to save processed receipts (required)")
        flag.Parse()

        if watchDir == "" || destDir == "" {
                flag.Usage()
                log.Fatal("Both -watch and -dest flags are required")
        }

        // 1. Setup Gemini Client
        ctx := context.Background()
        apiKey := os.Getenv("GEMINI_API_KEY")
        if apiKey == "" {
                log.Fatal("GEMINI_API_KEY environment variable not set")
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

                                // Trigger on any modification that might indicate a file is ready
                                // We include Rename/Chmod because some scanners write to a temp file then rename,
                                // or change permissions as a final step.
                                if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Chmod) {
                                        // DEDUPLICATION: Check if we are already handling this file
                                        if _, loaded := activeFiles.LoadOrStore(event.Name, true); loaded {
                                                continue
                                        }
                                        // Start processing in a new thread
                                        go processEvent(ctx, client, event.Name)
                                } else {
                    log.Printf("Ignored event: %v", event)
                }

                        case err, ok := <-watcher.Errors:
                                if !ok {
                                        return
                                }
                                log.Println("Watcher error:", err)
                        }
                }
        }()

        if err := watcher.Add(watchDir); err != nil {
                log.Fatalf("Failed to watch directory %s: %v", watchDir, err)
        }
        log.Printf("Listening for receipts in %s...", watchDir)
        log.Printf("Saving processed files to %s...", destDir)
        <-done
}

func processEvent(ctx context.Context, client *genai.Client, path string) {
        defer activeFiles.Delete(path)

        log.Printf("Detected: %s. Waiting for write to complete...", path)

        if err := waitForStableFile(path); err != nil {
                log.Printf("Processing aborted for %s: %v", path, err)
                return
        }

        // Filter valid extensions
        ext := strings.ToLower(filepath.Ext(path))
        if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".pdf" {
                return
        }

        log.Printf("Processing: %s", path)

        dataList, err := analyzeReceipt(ctx, client, path)
        if err != nil {
                log.Printf("Analysis failed for %s: %v", path, err)
                return
        }

        if len(dataList) == 0 {
                log.Printf("No receipt data found in %s", path)
                return
        }

        saveAndArchive(path, dataList)
}

// waitForStableFile monitors the file until size is constant for a duration
func waitForStableFile(path string) error {
        const stabilityThreshold = 10 * time.Second
        const maxWaitTime = 5 * time.Minute

        startTime := time.Now()
        lastSize := int64(-1)
        stableSince := time.Now()

        for {
                if time.Since(startTime) > maxWaitTime {
                        return fmt.Errorf("timeout waiting for file to stabilize")
                }

                info, err := os.Stat(path)
                if os.IsNotExist(err) {
                        return fmt.Errorf("file disappeared")
                }
                if err != nil {
                        return fmt.Errorf("error stating file: %w", err)
                }

                currentSize := info.Size()

                if currentSize != lastSize {
                        lastSize = currentSize
                        stableSince = time.Now()
                } else {
                        if time.Since(stableSince) >= stabilityThreshold {
                                if currentSize > 0 {
                                        return nil // Stable
                                }
                        }
                }

                time.Sleep(1 * time.Second)
        }
}

// analyzeReceipt uploads the file to Gemini and extracts receipt data
func analyzeReceipt(ctx context.Context, client *genai.Client, path string) ([]ReceiptData, error) {
        f, err := os.Open(path)
        if err != nil {
                return nil, fmt.Errorf("error opening file: %w", err)
        }
        defer f.Close()

        // Upload
        model := client.GenerativeModel(ModelName)
        model.ResponseMIMEType = "application/json"

        upFile, err := client.UploadFile(ctx, "", f, nil)
        if err != nil {
                return nil, fmt.Errorf("upload failed: %w", err)
        }
        defer client.DeleteFile(ctx, upFile.Name)

        // Wait for processing
        for upFile.State == genai.FileStateProcessing {
                time.Sleep(1 * time.Second)
                upFile, err = client.GetFile(ctx, upFile.Name)
                if err != nil {
                        return nil, fmt.Errorf("check failed state: %w", err)
                }
        }

        if upFile.State != genai.FileStateActive {
                return nil, fmt.Errorf("file processing failed state: %s", upFile.State)
        }

        // Generate
        prompt := `Analyze this Japanese receipt or certificate. Extract JSON with these keys:
    "date" (YYYY-MM-DD),
    "vendor" (Japanese name, if medical use clinic name),
    "category" (Medical, Grocery, Tax, Utilities, Septic, Other),
    "total_amount" (integer).`

        resp, err := model.GenerateContent(ctx, genai.FileData{URI: upFile.URI}, genai.Text(prompt))
        if err != nil {
                return nil, fmt.Errorf("gemini generate error: %w", err)
        }

        if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
                return nil, fmt.Errorf("empty response from model")
        }

        var jsonText string
        if txt, ok := resp.Candidates[0].Content.Parts[0].(genai.Text); ok {
                jsonText = string(txt)
        }

        return parseGeminiResponse(jsonText)
}

func parseGeminiResponse(jsonText string) ([]ReceiptData, error) {
        var dataList []ReceiptData
        var single ReceiptData

        // Attempt 1: Single Object
        if err := json.Unmarshal([]byte(jsonText), &single); err == nil {
                dataList = append(dataList, single)
                return dataList, nil
        }

        // Attempt 2: Array of Objects
        var list []ReceiptData
        if err := json.Unmarshal([]byte(jsonText), &list); err == nil {
                return list, nil
        }

        return nil, fmt.Errorf("failed to parse JSON as object or array")
}

func saveAndArchive(srcPath string, dataList []ReceiptData) {
        successCount := 0
        for _, data := range dataList {
                if err := saveProcessedFile(srcPath, data); err != nil {
                        log.Printf("Failed to save processed file: %v", err)
                } else {
                        successCount++
                }
        }

        if successCount > 0 {
                archiveOriginalFile(srcPath)
        } else {
                log.Printf("No receipts saved, skipping archive for %s", srcPath)
        }
}

func saveProcessedFile(srcPath string, data ReceiptData) error {
        vendor := strings.ReplaceAll(data.Vendor, " ", "")
        vendor = strings.ReplaceAll(vendor, "/", "-")

        if data.Date == "" {
                data.Date = time.Now().Format("2006-01-02")
        }
        if data.Category == "" {
                data.Category = "Unsorted"
        }

        processedFileName := fmt.Sprintf("%s_%s_%d円%s", data.Date, vendor, data.Amount, filepath.Ext(srcPath))
        processedDir := filepath.Join(destDir, data.Category)
        processedPath := filepath.Join(processedDir, processedFileName)

        if err := os.MkdirAll(processedDir, 0755); err != nil {
                return fmt.Errorf("failed to create directory %s: %w", processedDir, err)
        }

        if err := robustCopy(srcPath, processedPath); err != nil {
                return fmt.Errorf("failed to copy to processed folder: %w", err)
        }

        log.Printf("Saved processed file: %s", processedPath)
        return nil
}

func archiveOriginalFile(srcPath string) {
        originalsDir := filepath.Join(destDir, "originals")
        originalName := filepath.Base(srcPath)
        originalsPath := filepath.Join(originalsDir, originalName)

        if err := os.MkdirAll(originalsDir, 0755); err != nil {
                log.Printf("Failed to create originals directory: %v", err)
                return
        }

        if err := robustMove(srcPath, originalsPath); err != nil {
                log.Printf("Failed to move to originals: %v", err)
        } else {
                log.Printf("Archived original to: %s", originalsPath)
        }
}

// robustCopy performs a simple copy of the file content
func robustCopy(src, dst string) error {
        sourceFile, err := os.Open(src)
        if err != nil { return err }
        defer sourceFile.Close()

        destFile, err := os.Create(dst)
        if err != nil { return err }
        defer destFile.Close()

        _, err = io.Copy(destFile, sourceFile)
        return err
}

// robustMove tries atomic rename first, then falls back to Copy+Delete
func robustMove(src, dst string) error {
        // Try atomic rename
        err := os.Rename(src, dst)
        if err == nil { return nil }

        // If error is NOT cross-device, fail
        if !strings.Contains(err.Error(), "cross-device link") {
                return err
        }

        // Fallback: Copy and Delete
        if err := robustCopy(src, dst); err != nil {
                return err
        }

        return os.Remove(src)
}
