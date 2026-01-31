# Scanner Bot

Scanner Bot is a Go-based automation tool designed to process scanned receipts and documents. It watches a specified directory for new files, uploads them to Google's Gemini Pro Vision model for analysis, and automatically renames and organizes them based on their content.

## Features

- **Automated Directory Watching**: Monitors a folder for new scans (PDF, JPG, PNG, JPEG).
- **AI-Powered Analysis**: Uses Google Gemini to extract date, vendor, category, and total amount from receipts.
- **Smart Renaming**: Renames files to a standard format: `YYYY-MM-DD_Vendor_Amount円.ext`.
- **Categorization**: Moves processed files into subdirectories based on their category (e.g., Grocery, Medical, Tax).
- **Multi-Receipt Support**: handling multiple receipts on a single page if recognized by the AI.
- **Originals Archiving**: Keeps the original raw scan in an `originals` folder.
- **Robustness**: Handles file stability checks (waiting for scanners to finish writing) and atomic moves.

## Prerequisites

- Go 1.21 or later
- A Google Gemini API Key

## Installation

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/yourusername/scanner-bot.git
    cd scanner-bot
    ```

2.  **Build the binary:**
    ```bash
    go build -o scanner-bot .
    ```

3.  **Install (Linux/Systemd):**

    Edit the provided `scanner-bot.service` file to match your paths and user:

    ```ini
    [Service]
    User=your_user
    Environment="GEMINI_API_KEY=your_api_key_here"
    ExecStart=/path/to/scanner-bot -watch "/path/to/input" -dest "/path/to/output"
    ```

    Then copy it to systemd and enable it:
    ```bash
    sudo cp scanner-bot.service /etc/systemd/system/
    sudo systemctl daemon-reload
    sudo systemctl enable --now scanner-bot
    ```

## Usage

You can run the bot manually from the command line:

```bash
export GEMINI_API_KEY="your_api_key_here"
./scanner-bot -watch "/path/to/watch/dir" -dest "/path/to/output/dir"
```

### Flags

- `-watch`: (Required) The directory to watch for new incoming scan files.
- `-dest`: (Required) The root directory where processed files and the `originals` folder will be created.

## How it Works

1.  **Detect**: The bot watches for `Create`, `Write`, `Rename`, or `Chmod` events in the watch directory.
2.  **Wait**: It waits for the file size to stabilize (indicating the scanner has finished writing).
3.  **Analyze**: The file is uploaded to Google Gemini.
4.  **Extract**: The AI extracts the Date, Vendor, Category, and Total Amount.
5.  **Process**:
    - The file is copied to `dest/Category/YYYY-MM-DD_Vendor_Amount円.ext`.
    - The original file is moved to `dest/originals/filename.ext`.

## License

MIT
