package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"
)

// CLI flags
var (
	urlFlag             = flag.String("url", "", "The full URL of the Kick VOD to archive (required)")
	outputFlag          = flag.String("output", ".", "The base directory to save the archive folder into")
	retriesFlag         = flag.Int("retries", 5, "The number of times to retry a failed network request")
	maxConcurrentEmotes = flag.Int("max-concurrent-emotes", 10, "The maximum number of emote download goroutines")
	ytdlpRetries        = flag.Int("ytdlp-retries", 3, "The number of times to retry yt-dlp if it fails completely")
	chatDelay           = flag.Int("chat-delay", 300, "Delay in milliseconds between chat API requests (default 300ms, min 100ms)")
	noVOD               = flag.Bool("no-vod", false, "Skip downloading the VOD")
	noChat              = flag.Bool("no-chat", false, "Skip downloading chat and emotes")
	noEmotes            = flag.Bool("no-emotes", false, "Download chat but skip downloading emotes")
	overwrite           = flag.Bool("overwrite", false, "Delete and re-create the archive directory if it exists")
	dryRun              = flag.Bool("dry-run", false, "Show what would be downloaded without actually downloading")
	quiet               = flag.Bool("quiet", false, "Minimal output (errors and warnings only)")
	debug               = flag.Bool("debug", false, "Enable debug output including raw API responses")
	ytdlpVerbose        = flag.Bool("ytdlp-verbose", false, "Show raw yt-dlp output (useful for debugging VOD download issues)")
	logFile             = flag.String("log-file", "", "Optional path to a file for log output")
	noEmoji             = flag.Bool("no-emoji", false, "Disable emoji characters in log output for older terminals")
	simpleProgress      = flag.Bool("simple-progress", false, "Use a simple, single-line progress display without bars")
)

// Logging levels
const (
	LogLevelQuiet = iota
	LogLevelNormal
	LogLevelDebug
)

var logLevel = LogLevelNormal

// Emoji characters for log output.
var (
	EMOJI_ROCKET   = "🚀"
	EMOJI_DOWNLOAD = "📥"
	EMOJI_CHECK    = "✅"
	EMOJI_FOLDER   = "📂"
	EMOJI_GEAR     = "🔧"
	EMOJI_INFO     = "ℹ️"
	EMOJI_FILM     = "🎥"
	EMOJI_CHAT     = "💬"
	EMOJI_PAINT    = "🎨"
	EMOJI_WORLD    = "🌐"
	EMOJI_STOP     = "🛑"
	EMOJI_RECYCLE  = "🔄"
	EMOJI_PARTY    = "🎉"
	EMOJI_WARN     = "⚠️"
	EMOJI_CROSS    = "❌"
	EMOJI_RULER    = "📏"
	EMOJI_BOX      = "📦"
	EMOJI_BUG      = "🐞"
	EMOJI_SAVE     = "💾"
	EMOJI_SEEDLING = "🌱"
	EMOJI_STATS    = "📊"
	EMOJI_EYE      = "🔍"
	EMOJI_SKIP     = "⏭️"
	EMOJI_PIN      = "📍"
	EMOJI_PAPER    = "📝"
	EMOJI_CLOCK    = "⏱️"
)

// VODMetadata holds all extracted information about the VOD.
type VODMetadata struct {
	UUID        string          `json:"uuid"`
	Title       string          `json:"title"`
	Source      string          `json:"source"`
	StartTime   time.Time       `json:"start_time"`
	Duration    int             `json:"duration_seconds"`
	IsMature    bool            `json:"is_mature"`
	Language    string          `json:"language"`
	Tags        []string        `json:"tags"`
	Views       int             `json:"views"`
	Categories  []Category      `json:"categories"`
	ChannelInfo ChannelInfo     `json:"channel_info"`
	ChatroomID  int             `json:"chatroom_id"`
	RawData     json.RawMessage `json:"raw_data"`
}

type ChannelInfo struct {
	ID                  int     `json:"id"`
	Slug                string  `json:"slug"`
	Username            string  `json:"username"`
	IsVerified          bool    `json:"is_verified"`
	FollowersCount      int     `json:"followers_count"`
	Bio                 string  `json:"bio"`
	ProfilePicURL       string  `json:"profile_pic_url"`
	SubscriptionEnabled bool    `json:"subscription_enabled"`
	Socials             Socials `json:"socials"`
}

type Socials struct {
	Instagram string `json:"instagram"`
	Twitter   string `json:"twitter"`
	Youtube   string `json:"youtube"`
	Discord   string `json:"discord"`
	Tiktok    string `json:"tiktok"`
	Facebook  string `json:"facebook"`
}

type Category struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// ProgramState tracks the download progress for graceful resumption.
type ProgramState struct {
	VODComplete   bool      `json:"vod_complete"`
	ChatComplete  bool      `json:"chat_complete"`
	LastChatTime  time.Time `json:"last_chat_time"`
	TotalMessages int64     `json:"total_messages"`
	TotalEmotes   int64     `json:"total_emotes"`
	LastUpdated   time.Time `json:"last_updated"`
}

// Statistics stores runtime metrics for the final summary.
type Statistics struct {
	StartTime            time.Time
	VODSize              int64
	VODDownloadDuration  time.Duration
	ChatDownloadDuration time.Duration
	TotalMessages        int64
	TotalEmotes          int64
	FailedEmotes         int64
	APICalls             int64
	LastAPIRequestTime   time.Time
	AvgAPIResponseTime   time.Duration
	mu                   sync.Mutex
}

// ProgressState holds data for rendering the live progress display.
type ProgressState struct {
	mu                sync.Mutex
	VODPercent        string
	VODSize           string
	VODSpeed          string
	VODETA            string
	VODStatusMessage  string
	ChatPercent       string
	Messages          string
	Emotes            string
	ChatETA           string
	ChatMsgsInBatch   string
	ChatEmotesInBatch string
	VODDone           bool
	ChatDone          bool
}

// ChatMessage structures for parsing API responses.
type ChatMessage struct {
	ID        string    `json:"id"`
	ChatID    int       `json:"chat_id"`
	UserID    int       `json:"user_id"`
	Content   string    `json:"content"`
	Type      string    `json:"type"`
	Metadata  string    `json:"metadata"`
	Sender    Sender    `json:"sender"`
	CreatedAt time.Time `json:"created_at"`
}

type Sender struct {
	ID       int      `json:"id"`
	Slug     string   `json:"slug"`
	Username string   `json:"username"`
	Identity Identity `json:"identity"`
}

type Identity struct {
	Color  string  `json:"color"`
	Badges []Badge `json:"badges"`
}

type Badge struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Count int    `json:"count,omitempty"`
}

type ChatResponse struct {
	Data struct {
		Messages      []ChatMessage   `json:"messages"`
		Cursor        string          `json:"cursor"`
		PinnedMessage json.RawMessage `json:"pinned_message"`
	} `json:"data"`
	Message string `json:"message"`
}

type PinnedMessageEvent struct {
	PinnedAt   string          `json:"pinned_at,omitempty"`
	UnpinnedAt string          `json:"unpinned_at,omitempty"`
	Message    json.RawMessage `json:"message"`
}

// FinalChatOutput is the structure for the final merged chat log file.
type FinalChatOutput struct {
	Data struct {
		Messages       []ChatMessage        `json:"messages"`
		Cursor         string               `json:"cursor"`
		PinnedMessages []PinnedMessageEvent `json:"pinned_messages"`
	} `json:"data"`
	Stats struct {
		TotalMessages    int64 `json:"total_messages"`
		TotalEmotes      int64 `json:"total_emotes"`
		UniqueEmotes     int64 `json:"unique_emotes"`
		DownloadedEmotes int64 `json:"downloaded_emotes"`
		FailedEmotes     int64 `json:"failed_emotes"`
	} `json:"stats"`
}

// Emote structures for downloading and tracking emotes.
type EmoteTask struct {
	ID   string
	Name string
}

type EmoteVersion struct {
	FilePath     string    `json:"file_path"`
	LastModified time.Time `json:"last_modified"`
	SHA256       string    `json:"sha256"`
}

type EmoteMetadata struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Versions []EmoteVersion `json:"versions"`
}

type EmoteDatabase struct {
	Emotes map[string]*EmoteMetadata `json:"emotes"`
	mu     sync.RWMutex
}

// Global state variables
var (
	logger       *log.Logger
	stats        = &Statistics{StartTime: time.Now()}
	ctx          context.Context
	cancel       context.CancelFunc
	shutdownOnce sync.Once
)

func main() {
	// Custom usage function to format flags with double dashes.
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "Usage of %s:\n", os.Args[0])
		flag.VisitAll(func(f *flag.Flag) {
			var flagSyntax string
			name, usage := flag.UnquoteUsage(f)
			if len(name) > 0 {
				flagSyntax = fmt.Sprintf("--%s %s", f.Name, name)
			} else {
				flagSyntax = fmt.Sprintf("--%s", f.Name)
			}
			fmt.Fprintf(out, "  %-25s %s\n", flagSyntax, usage)
		})
	}
	flag.Parse()

	// Validate required flags and arguments.
	if *quiet {
		logLevel = LogLevelQuiet
	} else if *debug {
		logLevel = LogLevelDebug
	}
	if *urlFlag == "" {
		fmt.Fprintln(os.Stderr, "Error: --url flag is required")
		flag.Usage()
		os.Exit(1)
	}
	if !isValidKickURL(*urlFlag) {
		fmt.Fprintln(os.Stderr, "Error: URL must be in format https://kick.com/{channel}/videos/{uuid}")
		os.Exit(1)
	}
	if *chatDelay < 100 {
		fmt.Fprintln(os.Stderr, "Error: --chat-delay must be at least 100ms to avoid rate limiting")
		os.Exit(1)
	}

	// Setup logging, emoji display, and signal handling for graceful shutdown.
	setupEmoji()
	setupLogger()
	ctx, cancel = context.WithCancel(context.Background())
	setupSignalHandler()

	logz("info", EMOJI_ROCKET, "Starting Kick VOD Archiver")

	// Verify yt-dlp is installed and accessible.
	if !*noVOD && !*dryRun {
		if err := verifyYtDlp(); err != nil {
			logz("fatal", EMOJI_CROSS, "yt-dlp verification failed: %v", err)
		}
	}

	// Fetch metadata from the Kick VOD page.
	logz("info", EMOJI_DOWNLOAD, "Extracting VOD metadata...")
	metadata, err := extractVODMetadata(*urlFlag)
	if err != nil {
		logz("fatal", EMOJI_CROSS, "Failed to extract metadata: %v", err)
	}
	logz("ok", EMOJI_CHECK, "Metadata extracted: Title=\"%s\", Channel=%s, Views=%d, Duration=%ds",
		metadata.Title, metadata.ChannelInfo.Username, metadata.Views, metadata.Duration)

	archiveDir := filepath.Join(*outputFlag, fmt.Sprintf("%s_%s", metadata.ChannelInfo.Slug, metadata.UUID))

	// Handle dry run mode.
	if *dryRun {
		logz("info", EMOJI_EYE, "DRY RUN MODE - No files will be downloaded")
		logz("info", EMOJI_FOLDER, "Would create directory: %s", archiveDir)
		if !*noVOD {
			logz("info", EMOJI_FILM, "Would download VOD from: %s", metadata.Source)
		}
		if !*noChat {
			duration := time.Duration(metadata.Duration) * time.Second
			logz("info", EMOJI_CHAT, "Would fetch chat from %s to %s (%s)",
				metadata.StartTime.Format(time.RFC3339),
				metadata.StartTime.Add(duration).Format(time.RFC3339),
				duration)
		}
		return
	}

	// Create the main directory for the archive.
	if err := createArchiveDir(archiveDir); err != nil {
		logz("fatal", EMOJI_CROSS, "Failed to create archive directory: %v", err)
	}

	// Load previous state for resuming or start fresh.
	state, isResuming := loadState(archiveDir, metadata)
	if isResuming {
		logz("info", EMOJI_FOLDER, "Loaded existing state: VOD_complete=%v, Chat_complete=%v, LastChatTime=%s",
			state.VODComplete, state.ChatComplete, state.LastChatTime.Format(time.RFC3339))
	}

	if state.TotalMessages > 0 {
		atomic.StoreInt64(&stats.TotalMessages, state.TotalMessages)
	}
	if state.TotalEmotes > 0 {
		atomic.StoreInt64(&stats.TotalEmotes, state.TotalEmotes)
	}

	// Save initial metadata to a file.
	metadataPath := filepath.Join(archiveDir, fmt.Sprintf("vod_metadata_%s.json", metadata.UUID))
	if err := saveJSONAtomic(metadataPath, metadata); err != nil {
		logz("fatal", EMOJI_CROSS, "Failed to save VOD metadata: %v", err)
	}
	logz("ok", EMOJI_SAVE, "Saved VOD metadata to %s", metadataPath)

	var wg sync.WaitGroup
	var vodErr, chatErr error
	var vodCompleted, chatCompleted bool

	// Start the progress rendering goroutine.
	var progressState ProgressState
	var progressWg sync.WaitGroup
	if logLevel >= LogLevelNormal {
		progressWg.Add(1)
		if *simpleProgress {
			go renderSimpleProgress(ctx, &progressState, &progressWg)
		} else {
			go renderProgress(ctx, &progressState, &progressWg)
		}
	}

	// Start VOD download in a separate goroutine if not skipped.
	if !*noVOD && !state.VODComplete {
		wg.Add(1)
		go func() {
			defer wg.Done()
			startTime := time.Now()
			if err := downloadVOD(ctx, archiveDir, metadata, state, &progressState); err != nil {
				logz("error", EMOJI_CROSS, "VOD download failed: %v", err)
				vodErr = err
			} else {
				vodCompleted = true
			}
			stats.mu.Lock()
			stats.VODDownloadDuration = time.Since(startTime)
			stats.mu.Unlock()
		}()
	} else {
		vodCompleted = true
		progressState.mu.Lock()
		progressState.VODDone = true
		progressState.mu.Unlock()
		if state.VODComplete {
			logz("info", EMOJI_SKIP, "VOD already complete, skipping")
		}
	}

	// Start chat download in a separate goroutine if not skipped.
	if !*noChat && !state.ChatComplete {
		wg.Add(1)
		go func() {
			defer wg.Done()
			startTime := time.Now()
			if err := processChatAndEmotes(ctx, archiveDir, metadata, state, &progressState); err != nil {
				logz("error", EMOJI_CROSS, "Chat/Emote processing failed: %v", err)
				chatErr = err
			}
			stats.mu.Lock()
			stats.ChatDownloadDuration = time.Since(startTime)
			stats.mu.Unlock()
		}()
	} else {
		chatCompleted = true
		progressState.mu.Lock()
		progressState.ChatDone = true
		progressState.mu.Unlock()
		if state.ChatComplete {
			logz("info", EMOJI_SKIP, "Chat already complete, skipping")
		}
	}

	// Wait for all download processes to finish.
	wg.Wait()
	cancel()

	if logLevel >= LogLevelNormal {
		progressWg.Wait()
	}

	// Print the final summary statistics.
	finalState, _ := loadState(archiveDir, metadata)
	printStatistics(
		vodCompleted || finalState.VODComplete,
		chatCompleted || finalState.ChatComplete,
		vodErr, chatErr)
}

// setupEmoji replaces emoji characters with text equivalents if the --no-emoji flag is set.
func setupEmoji() {
	if *noEmoji {
		EMOJI_ROCKET = "[START]"
		EMOJI_DOWNLOAD = "[DL]"
		EMOJI_CHECK = "[OK]"
		EMOJI_FOLDER = "[DIR]"
		EMOJI_GEAR = "[PROC]"
		EMOJI_INFO = "[INFO]"
		EMOJI_FILM = "[VOD]"
		EMOJI_CHAT = "[CHAT]"
		EMOJI_PAINT = "[EMOTE]"
		EMOJI_WORLD = "[API]"
		EMOJI_STOP = "[STOP]"
		EMOJI_RECYCLE = "[RETRY]"
		EMOJI_PARTY = "[DONE]"
		EMOJI_WARN = "[WARN]"
		EMOJI_CROSS = "[FAIL]"
		EMOJI_RULER = "[SIZE]"
		EMOJI_BOX = "[SPACE]"
		EMOJI_BUG = "[DEBUG]"
		EMOJI_SAVE = "[SAVE]"
		EMOJI_SEEDLING = "[START]"
		EMOJI_STATS = "[STATS]"
		EMOJI_EYE = "[DRYRUN]"
		EMOJI_SKIP = "[SKIP]"
		EMOJI_PIN = "[RESUME]"
		EMOJI_PAPER = "[MERGE]"
		EMOJI_CLOCK = "[TIME]"
	}
}

// logz is a centralized logging function that handles different log levels.
// It supports special "plain" levels to print final status messages without prefixes.
func logz(level string, emoji string, format string, v ...interface{}) {
	prefix := emoji + " "
	if *noEmoji {
		prefix = ""
	}

	msg := fmt.Sprintf(prefix+format, v...)

	switch level {
	case "info", "ok":
		if logLevel >= LogLevelNormal {
			fmt.Printf("\r\033[K")
			logger.Print(msg)
		}
	case "debug":
		if logLevel >= LogLevelDebug {
			logger.Printf("[DEBUG] %s", fmt.Sprintf(format, v...))
		}
	case "error":
		fmt.Printf("\r\033[K")
		logger.Printf("[ERROR] %s", msg)
	case "plain_error": // Used for final summary messages without the [ERROR] prefix.
		fmt.Printf("\r\033[K")
		logger.Printf("%s", msg)
	case "warn":
		if logLevel >= LogLevelNormal {
			fmt.Printf("\r\033[K")
			logger.Printf("[WARNING] %s", msg)
		}
	case "plain_warn": // Used for final summary messages without the [WARNING] prefix.
		if logLevel >= LogLevelNormal {
			fmt.Printf("\r\033[K")
			logger.Printf("%s", msg)
		}
	case "fatal":
		fmt.Printf("\r\033[K")
		logger.Fatalf("[FATAL] %s", msg)
	}
}

func isValidKickURL(url string) bool {
	re := regexp.MustCompile(`^https://kick\.com/[^/]+/videos/[a-f0-9-]+$`)
	return re.MatchString(url)
}

func setupLogger() {
	var writers []io.Writer
	if logLevel > LogLevelQuiet {
		writers = append(writers, os.Stdout)
	}
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to open log file: %v\n", err)
			os.Exit(1)
		}
		writers = append(writers, f)
	}
	if len(writers) == 0 {
		writers = append(writers, io.Discard)
	}
	logger = log.New(io.MultiWriter(writers...), "", log.LstdFlags)
}

// setupSignalHandler captures interrupt signals (Ctrl+C) for a graceful shutdown.
func setupSignalHandler() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		shutdownOnce.Do(func() {
			logz("info", EMOJI_STOP, "\nSignal received, initiating graceful shutdown...")
			cancel()
			time.Sleep(2 * time.Second)
			logz("ok", EMOJI_CHECK, "Shutdown complete. You can resume by running the same command.")
			os.Exit(0)
		})
	}()
}

func loadState(archiveDir string, metadata *VODMetadata) (*ProgramState, bool) {
	statePath := filepath.Join(archiveDir, ".state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return &ProgramState{LastChatTime: metadata.StartTime}, false
	}
	var state ProgramState
	if err := json.Unmarshal(data, &state); err != nil {
		logz("warn", EMOJI_WARN, "Failed to parse state file, starting fresh: %v", err)
		return &ProgramState{LastChatTime: metadata.StartTime}, false
	}
	return &state, true
}

func saveState(archiveDir string, state *ProgramState) {
	state.LastUpdated = time.Now()
	state.TotalMessages = atomic.LoadInt64(&stats.TotalMessages)
	state.TotalEmotes = atomic.LoadInt64(&stats.TotalEmotes)
	statePath := filepath.Join(archiveDir, ".state.json")
	if err := saveJSONAtomic(statePath, state); err != nil {
		logz("warn", EMOJI_WARN, "Failed to save state: %v", err)
	} else {
		logz("debug", EMOJI_SAVE, "State saved (LastChatTime=%s, Messages=%d)",
			state.LastChatTime.Format(time.RFC3339),
			atomic.LoadInt64(&stats.TotalMessages))
	}
}

func verifyYtDlp() error {
	_, err := exec.LookPath("yt-dlp")
	if err != nil {
		return fmt.Errorf("yt-dlp not found in PATH")
	}
	logz("ok", EMOJI_CHECK, "yt-dlp found")
	return nil
}

// Internal struct used for unmarshalling the complex JSON data from the VOD page.
type fullVODData struct {
	UUID       string `json:"uuid"`
	Views      int    `json:"views"`
	Source     string `json:"source"`
	Livestream struct {
		SessionTitle string `json:"session_title"`
		IsMature     bool   `json:"is_mature"`
		StartTime    string `json:"start_time"`
		Duration     int    `json:"duration"`
		Language     string `json:"language"`
		Tags         []string
		Categories   []struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"categories"`
		Channel struct {
			ID                  int    `json:"id"`
			Slug                string `json:"slug"`
			FollowersCount      int    `json:"followersCount"`
			SubscriptionEnabled bool   `json:"subscription_enabled"`
			Verified            *struct {
				ID int `json:"id"`
			} `json:"verified"`
			User struct {
				Username   string `json:"username"`
				Bio        string `json:"bio"`
				ProfilePic string `json:"profilepic"`
				Instagram  string `json:"instagram"`
				Twitter    string `json:"twitter"`
				Youtube    string `json:"youtube"`
				Discord    string `json:"discord"`
				Tiktok     string `json:"tiktok"`
				Facebook   string `json:"facebook"`
			} `json:"user"`
		} `json:"channel"`
	} `json:"livestream"`
}

// extractVODMetadata scrapes the VOD's HTML page to find and parse the embedded Next.js JSON data payload.
func extractVODMetadata(url string) (*VODMetadata, error) {
	logz("debug", EMOJI_BUG, "Fetching VOD page: %s", url)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch VOD page: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	logz("debug", EMOJI_BUG, "HTML page size: %d bytes", len(body))

	// Find all potential Next.js data payloads in the HTML.
	re := regexp.MustCompile(`self\.__next_f\.push\(\[1,"((?:[^"\\]|\\.)*)"\]\)`)
	allMatches := re.FindAllStringSubmatch(string(body), -1)

	var payloadData string
	for _, match := range allMatches {
		if len(match) > 1 && strings.Contains(match[1], "livestream") && strings.Contains(match[1], "session_title") {
			payloadData = match[1]
			logz("debug", EMOJI_BUG, "Found potential data payload")
			break
		}
	}
	if payloadData == "" {
		return nil, fmt.Errorf("failed to find Next.js data payload in HTML")
	}

	// The payload is escaped; unescape it to process as a string.
	unescaped := strings.ReplaceAll(strings.ReplaceAll(payloadData, `\"`, `"`), `\\`, `\`)
	uuidFromURL := regexp.MustCompile(`/videos/([a-f0-9-]+)`).FindStringSubmatch(*urlFlag)[1]

	// This logic is brittle. It finds the start of the VOD's JSON object by UUID,
	// then works backwards and forwards counting braces to find the complete object boundaries.
	// This is necessary because the payload contains multiple concatenated JSON objects.
	jsonStartIdx := strings.Index(unescaped, `"uuid":"`+uuidFromURL+`"`)
	if jsonStartIdx == -1 {
		return nil, fmt.Errorf("could not find VOD JSON object start")
	}
	braceLevel := 0
	for i := jsonStartIdx; i >= 0; i-- {
		if unescaped[i] == '}' {
			braceLevel++
		}
		if unescaped[i] == '{' {
			if braceLevel == 0 {
				jsonStartIdx = i
				break
			}
			braceLevel--
		}
	}
	braceLevel = 0
	jsonEndIdx := -1
	for i := jsonStartIdx; i < len(unescaped); i++ {
		if unescaped[i] == '{' {
			braceLevel++
		}
		if unescaped[i] == '}' {
			braceLevel--
			if braceLevel == 0 {
				jsonEndIdx = i + 1
				break
			}
		}
	}
	if jsonEndIdx == -1 {
		return nil, fmt.Errorf("could not find matching end brace for VOD JSON object")
	}

	vodJSON := unescaped[jsonStartIdx:jsonEndIdx]
	logz("debug", EMOJI_BUG, "Extracted VOD JSON object: %s", vodJSON)

	var data fullVODData
	if err := json.Unmarshal([]byte(vodJSON), &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal VOD JSON: %w", err)
	}

	// Chatroom ID is in a different part of the payload, so we find it with another regex.
	reChatroom := regexp.MustCompile(fmt.Sprintf(`"id":%d,.*?"chatroom":\{"id":(\d+)`, data.Livestream.Channel.ID))
	chatroomMatches := reChatroom.FindStringSubmatch(unescaped)
	var chatroomID int
	if len(chatroomMatches) > 1 {
		chatroomID, _ = strconv.Atoi(chatroomMatches[1])
	} else {
		logz("warn", EMOJI_WARN, "Could not find chatroom ID; chat download may fail.")
	}

	startTime, err := time.Parse(time.RFC3339, data.Livestream.StartTime)
	if err != nil {
		return nil, fmt.Errorf("failed to parse start time: %v", err)
	}

	var categories []Category
	for _, cat := range data.Livestream.Categories {
		categories = append(categories, Category{Name: cat.Name, Slug: cat.Slug})
	}

	// Map the parsed data to our clean VODMetadata struct.
	return &VODMetadata{
		UUID: data.UUID, Title: data.Livestream.SessionTitle, Source: data.Source, StartTime: startTime,
		Duration: data.Livestream.Duration / 1000, IsMature: data.Livestream.IsMature, Language: data.Livestream.Language,
		Tags: data.Livestream.Tags, Views: data.Views, Categories: categories, ChatroomID: chatroomID,
		ChannelInfo: ChannelInfo{
			ID: data.Livestream.Channel.ID, Slug: data.Livestream.Channel.Slug, Username: data.Livestream.Channel.User.Username,
			IsVerified: data.Livestream.Channel.Verified != nil, FollowersCount: data.Livestream.Channel.FollowersCount,
			Bio: data.Livestream.Channel.User.Bio, ProfilePicURL: data.Livestream.Channel.User.ProfilePic,
			SubscriptionEnabled: data.Livestream.Channel.SubscriptionEnabled,
			Socials: Socials{
				Instagram: data.Livestream.Channel.User.Instagram, Twitter: data.Livestream.Channel.User.Twitter,
				Youtube: data.Livestream.Channel.User.Youtube, Discord: data.Livestream.Channel.User.Discord,
				Tiktok: data.Livestream.Channel.User.Tiktok, Facebook: data.Livestream.Channel.User.Facebook,
			},
		},
		RawData: json.RawMessage(vodJSON),
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func createArchiveDir(dir string) error {
	if *overwrite {
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	logz("ok", EMOJI_CHECK, "Created archive directory: %s", dir)
	return nil
}

func checkDiskSpace(archiveDir string, requiredBytes int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(archiveDir, &stat); err != nil {
		return fmt.Errorf("failed to check disk space: %v", err)
	}
	available := stat.Bavail * uint64(stat.Bsize)
	required := uint64(requiredBytes)
	if available < required {
		return fmt.Errorf("insufficient disk space: need %s, have %s", formatBytes(requiredBytes), formatBytes(int64(available)))
	}
	logz("ok", EMOJI_CHECK, "Sufficient disk space available (%s required, %s available)", formatBytes(requiredBytes), formatBytes(int64(available)))
	return nil
}

// getVODSize uses yt-dlp to estimate the required disk space for the download.
func getVODSize(source string) (int64, error) {
	logz("info", EMOJI_RULER, "Checking VOD size...")

	cmd := exec.Command("yt-dlp", "-F", "--check-formats", source)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("failed to get format info: %v", err)
	}
	logz("debug", EMOJI_BUG, "yt-dlp format output:\n%s", string(output))

	re := regexp.MustCompile(`~?\s*([\d\.]+)(KiB|MiB|GiB|TiB)`)
	var maxSize int64
	for _, line := range strings.Split(string(output), "\n") {
		matches := re.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			if len(match) < 3 {
				continue
			}
			size, _ := strconv.ParseFloat(match[1], 64)
			var bytes int64
			switch match[2] {
			case "KiB":
				bytes = int64(size * 1024)
			case "MiB":
				bytes = int64(size * 1024 * 1024)
			case "GiB":
				bytes = int64(size * 1024 * 1024 * 1024)
			case "TiB":
				bytes = int64(size * 1024 * 1024 * 1024 * 1024)
			}
			if bytes > maxSize {
				maxSize = bytes
			}
		}
	}
	if maxSize == 0 {
		logz("warn", EMOJI_WARN, "Could not determine VOD size from yt-dlp output, skipping space check")
		return 0, nil
	}

	// Double the size to account for temporary files during post-processing.
	maxSize *= 2
	logz("info", EMOJI_BOX, "Estimated space needed: %s (including post-processing)", formatBytes(maxSize))
	return maxSize, nil
}

// downloadVOD handles the entire VOD download process using yt-dlp.
func downloadVOD(ctx context.Context, archiveDir string, metadata *VODMetadata, state *ProgramState, progressState *ProgressState) error {
	logz("info", EMOJI_FILM, "Starting VOD download...")

	outputPath := filepath.Join(archiveDir, fmt.Sprintf("vod_%s.mp4", metadata.UUID))

	// Pre-flight check for available disk space.
	vodSize, err := getVODSize(metadata.Source)
	if err == nil && vodSize > 0 {
		var alreadyDownloaded int64
		if fileInfo, err := os.Stat(outputPath); err == nil {
			alreadyDownloaded = fileInfo.Size()
		} else if fileInfo, err := os.Stat(outputPath + ".part"); err == nil {
			alreadyDownloaded = fileInfo.Size()
		}
		if remaining := vodSize - alreadyDownloaded; remaining > 0 {
			if err := checkDiskSpace(archiveDir, remaining); err != nil {
				return err
			}
		}
	}

	// Main download loop with retries.
	for attempt := 1; attempt <= *ytdlpRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if attempt > 1 {
			logz("info", EMOJI_RECYCLE, "Retry attempt %d/%d for VOD download", attempt, *ytdlpRetries)
		}

		cmd := exec.CommandContext(ctx, "yt-dlp", "--newline", "--output", outputPath, "--write-info-json", metadata.Source)

		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()
		cmd.Start()

		// Goroutine to parse yt-dlp's stdout/stderr for progress updates.
		scanner := bufio.NewScanner(io.MultiReader(stdout, stderr))
		go func() {
			progressRe := regexp.MustCompile(
				`\[download\]\s+(?P<percentage>[\d\.]+)%\s+of\s+~?\s*(?P<size>[\d\.]+)(?P<unit>\w+B)\s+at\s+(?P<speed>[\d\.]+)(?P<speed_unit>\w+B/s)\s+ETA\s+(?P<eta>[\d:]+)(?:\s+\(frag\s+(?P<frag_current>\d+)/(?P<frag_total>\d+)\))?`,
			)
			names := progressRe.SubexpNames()

			for scanner.Scan() {
				line := scanner.Text()
				if *ytdlpVerbose {
					logz("info", EMOJI_FILM, "[yt-dlp] %s", line)
					continue
				}
				logz("debug", EMOJI_BUG, "[yt-dlp] %s", line)

				matches := progressRe.FindStringSubmatch(line)
				if len(matches) > 0 {
					results := make(map[string]string)
					for i, name := range names {
						if i > 0 && name != "" {
							results[name] = matches[i]
						}
					}
					progressState.mu.Lock()
					progressState.VODPercent = results["percentage"] + "%"
					progressState.VODSize = results["size"] + results["unit"]
					progressState.VODSpeed = results["speed"] + results["speed_unit"]
					progressState.VODETA = results["eta"]
					if frag, ok := results["frag_current"]; ok && frag != "" {
						progressState.VODSize += fmt.Sprintf(" (frag %s/%s)", frag, results["frag_total"])
					}
					progressState.VODStatusMessage = ""
					progressState.mu.Unlock()
				} else if strings.Contains(line, "[download] Destination:") {
					logz("info", EMOJI_DOWNLOAD, "[yt-dlp] %s", line)
					progressState.mu.Lock()
					if progressState.VODPercent == "" {
						progressState.VODPercent, progressState.VODSize, progressState.VODSpeed, progressState.VODETA = "0.0%", "...", "...", "..."
					}
					progressState.mu.Unlock()
				} else if strings.Contains(line, "[download]") && strings.Contains(line, "has already been downloaded") {
					logz("ok", EMOJI_CHECK, "[yt-dlp] %s", line)
				} else if strings.Contains(line, "[FixupM3u8]") || strings.Contains(line, "[Fixup") || strings.Contains(line, "[Merger]") {
					logz("info", EMOJI_GEAR, "[yt-dlp] %s", line)
					progressState.mu.Lock()
					progressState.VODStatusMessage = "Post-processing..."
					progressState.mu.Unlock()
				} else if strings.Contains(line, "[info]") {
					logz("info", EMOJI_INFO, "[yt-dlp] %s", line)
				}
			}
		}()

		if err := cmd.Wait(); err != nil {
			if attempt == *ytdlpRetries {
				return fmt.Errorf("yt-dlp failed after %d attempts: %v", *ytdlpRetries, err)
			}
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}
		break
	}

	if fileInfo, err := os.Stat(outputPath); err == nil {
		atomic.StoreInt64(&stats.VODSize, fileInfo.Size())
	}
	os.Chtimes(outputPath, metadata.StartTime, metadata.StartTime)

	state.VODComplete = true
	saveState(archiveDir, state)

	progressState.mu.Lock()
	progressState.VODDone = true
	progressState.mu.Unlock()

	logz("ok", EMOJI_CHECK, "VOD download complete!")
	return nil
}

// processChatAndEmotes manages the entire chat and emote download lifecycle.
func processChatAndEmotes(ctx context.Context, archiveDir string, metadata *VODMetadata, state *ProgramState, progressState *ProgressState) error {
	resumeTime, isResuming := findResumePoint(archiveDir, metadata, state)
	if isResuming {
		logz("info", EMOJI_RECYCLE, "Resuming from: %s", resumeTime.Format(time.RFC3339))
	} else {
		logz("info", EMOJI_SEEDLING, "Starting from: %s", resumeTime.Format(time.RFC3339))
	}

	// Setup a worker pool for concurrent emote downloads if not skipped.
	var emoteDB *EmoteDatabase
	var emoteTasks chan EmoteTask
	var emoteResults chan *EmoteMetadata
	var emoteWg sync.WaitGroup
	if !*noEmotes {
		emoteDB = loadEmoteDatabase(archiveDir)
		emoteTasks = make(chan EmoteTask, 100)
		emoteResults = make(chan *EmoteMetadata, 100)
		for i := 0; i < *maxConcurrentEmotes; i++ {
			emoteWg.Add(1)
			go emoteWorker(ctx, archiveDir, emoteTasks, emoteResults, &emoteWg, emoteDB)
		}
		go emoteMetadataSaver(ctx, archiveDir, emoteDB, emoteResults)
	}

	var pinnedEvents []PinnedMessageEvent
	var currentPinnedID string

	endTime := metadata.StartTime.Add(time.Duration(metadata.Duration) * time.Second)
	currentTime := resumeTime
	seenEmotes := make(map[string]bool)
	seenMessages := make(map[string]bool)

	totalDuration := endTime.Sub(metadata.StartTime)
	chatProcessingStartTime := time.Now()

	// Main loop: fetch chat messages in 5-second intervals until the end of the VOD.
	for currentTime.Before(endTime) {
		select {
		case <-ctx.Done():
			logz("warn", EMOJI_STOP, "Chat processing cancelled, merging existing data...")
			if !*noEmotes {
				close(emoteTasks)
				emoteWg.Wait()
				close(emoteResults)
				time.Sleep(time.Second)
			}
			return mergeChatFiles(archiveDir, metadata, pinnedEvents)
		default:
		}

		time.Sleep(time.Duration(*chatDelay) * time.Millisecond)
		atomic.AddInt64(&stats.APICalls, 1)

		timeStr := currentTime.UTC().Format("2006-01-02T15:04:05.000Z")
		apiURL := fmt.Sprintf("https://web.kick.com/api/v1/chat/%d/history?start_time=%s", metadata.ChannelInfo.ID, url.QueryEscape(timeStr))
		logz("debug", EMOJI_BUG, "Fetching chat: %s", apiURL)

		apiStartTime := time.Now()
		resp, err := httpGetWithRetry(apiURL)
		apiDuration := time.Since(apiStartTime)

		stats.mu.Lock()
		stats.AvgAPIResponseTime = (stats.AvgAPIResponseTime*time.Duration(stats.APICalls-1) + apiDuration) / time.Duration(stats.APICalls)
		stats.LastAPIRequestTime = time.Now()
		stats.mu.Unlock()

		if err != nil {
			logz("warn", EMOJI_WARN, "Failed to fetch chat at %s: %v", currentTime.Format(time.RFC3339), err)
			currentTime = currentTime.Add(5 * time.Second)
			continue
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		logz("debug", EMOJI_BUG, "Chat API response: %s", string(bodyBytes))

		var chatResp ChatResponse
		if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
			logz("warn", EMOJI_WARN, "Failed to parse chat response: %v", err)
			currentTime = currentTime.Add(5 * time.Second)
			continue
		}

		if len(chatResp.Data.Messages) > 0 {
			// Save each chunk of messages to a temporary part file.
			partFile := filepath.Join(archiveDir, fmt.Sprintf("chat_%s.%d.part.json", metadata.UUID, currentTime.Unix()))
			saveJSONAtomic(partFile, chatResp)

			currentMsgsInRequest, currentEmotesInRequest := 0, 0
			for _, msg := range chatResp.Data.Messages {
				if !seenMessages[msg.ID] {
					seenMessages[msg.ID] = true
					currentMsgsInRequest++
					if !*noEmotes {
						re := regexp.MustCompile(`\[emote:(\d+):([a-zA-Z0-9]+)\]`)
						matches := re.FindAllStringSubmatch(msg.Content, -1)
						currentEmotesInRequest += len(matches)
						extractEmotes(msg.Content, seenEmotes, emoteTasks)
					}
				}
			}
			atomic.AddInt64(&stats.TotalMessages, int64(currentMsgsInRequest))

			// Calculate progress and ETA for the UI.
			elapsed := currentTime.Sub(metadata.StartTime)
			progress := float64(elapsed) / float64(totalDuration) * 100

			elapsedRealTime := time.Since(chatProcessingStartTime)
			elapsedVODTime := currentTime.Sub(resumeTime)
			var estimatedETA time.Duration
			if elapsedRealTime > time.Second && elapsedVODTime > 0 {
				processingRate := float64(elapsedVODTime) / float64(elapsedRealTime)
				remainingVODTime := endTime.Sub(currentTime)
				if processingRate > 0 {
					etaSeconds := float64(remainingVODTime) / processingRate
					estimatedETA = time.Duration(etaSeconds)
				}
			}

			progressState.mu.Lock()
			progressState.ChatPercent = fmt.Sprintf("%.1f%%", progress)
			progressState.Messages = strconv.FormatInt(atomic.LoadInt64(&stats.TotalMessages), 10)
			progressState.Emotes = strconv.FormatInt(atomic.LoadInt64(&stats.TotalEmotes), 10)
			progressState.ChatETA = formatDuration(estimatedETA)
			progressState.ChatMsgsInBatch = strconv.Itoa(currentMsgsInRequest)
			progressState.ChatEmotesInBatch = strconv.Itoa(currentEmotesInRequest)
			progressState.mu.Unlock()
		}

		trackPinnedMessages(&chatResp, &pinnedEvents, &currentPinnedID, currentTime)

		if len(chatResp.Data.Messages) > 0 {
			state.LastChatTime = chatResp.Data.Messages[len(chatResp.Data.Messages)-1].CreatedAt
		} else {
			state.LastChatTime = currentTime
		}
		saveState(archiveDir, state)
		currentTime = currentTime.Add(5 * time.Second)
	}

	// Clean up emote workers and save final database.
	if !*noEmotes {
		close(emoteTasks)
		emoteWg.Wait()
		close(emoteResults)
		time.Sleep(time.Second)
		logz("ok", EMOJI_CHECK, "Downloaded %d unique emotes (%d failed)", atomic.LoadInt64(&stats.TotalEmotes), atomic.LoadInt64(&stats.FailedEmotes))
	}

	// Merge all temporary chat files into one final JSON file.
	if err := mergeChatFiles(archiveDir, metadata, pinnedEvents); err != nil {
		return err
	}

	state.ChatComplete = true
	saveState(archiveDir, state)
	progressState.mu.Lock()
	progressState.ChatDone = true
	progressState.mu.Unlock()

	logz("ok", EMOJI_CHECK, "Chat and emote processing complete!")
	return nil
}

// findResumePoint determines the latest timestamp from all possible sources to resume chat download.
func findResumePoint(archiveDir string, metadata *VODMetadata, state *ProgramState) (time.Time, bool) {
	finalChatPath := filepath.Join(archiveDir, fmt.Sprintf("chat_%s.json", metadata.UUID))
	finalChatTime := getLastMessageTimeFromFinalChat(finalChatPath)

	stateTime := state.LastChatTime
	if stateTime.IsZero() {
		stateTime = metadata.StartTime
	}
	partFileTime := getLastMessageTimeFromPartFiles(archiveDir, metadata.UUID)

	latestTime := metadata.StartTime
	if finalChatTime.After(latestTime) {
		latestTime = finalChatTime
	}
	if partFileTime.After(latestTime) {
		latestTime = partFileTime
	}
	if stateTime.After(latestTime) {
		latestTime = stateTime
	}

	isResuming := latestTime.Sub(metadata.StartTime).Seconds() > 1
	return latestTime.Add(5 * time.Second), isResuming
}

func getLastMessageTimeFromFinalChat(chatPath string) time.Time {
	data, err := os.ReadFile(chatPath)
	if err != nil {
		return time.Time{}
	}
	var finalChat FinalChatOutput
	if err := json.Unmarshal(data, &finalChat); err != nil {
		return time.Time{}
	}
	if len(finalChat.Data.Messages) == 0 {
		return time.Time{}
	}
	var latestTime time.Time
	for _, msg := range finalChat.Data.Messages {
		if msg.CreatedAt.After(latestTime) {
			latestTime = msg.CreatedAt
		}
	}
	return latestTime
}

func getLastMessageTimeFromPartFiles(archiveDir, uuid string) time.Time {
	pattern := filepath.Join(archiveDir, fmt.Sprintf("chat_%s.*.part.json", uuid))
	matches, _ := filepath.Glob(pattern)
	var latestTime time.Time
	for _, match := range matches {
		data, _ := os.ReadFile(match)
		var chatResp ChatResponse
		if json.Unmarshal(data, &chatResp) == nil {
			for _, msg := range chatResp.Data.Messages {
				if msg.CreatedAt.After(latestTime) {
					latestTime = msg.CreatedAt
				}
			}
		}
	}
	return latestTime
}

func extractEmotes(content string, seen map[string]bool, tasks chan EmoteTask) {
	re := regexp.MustCompile(`\[emote:(\d+):([a-zA-Z0-9]+)\]`)
	for _, match := range re.FindAllStringSubmatch(content, -1) {
		key := match[1] + ":" + match[2]
		if !seen[key] {
			seen[key] = true
			select {
			case tasks <- EmoteTask{ID: match[1], Name: match[2]}:
			default:
			}
		}
	}
}

// emoteWorker is a single worker that pulls emote download tasks from a channel.
func emoteWorker(ctx context.Context, archiveDir string, tasks chan EmoteTask, results chan *EmoteMetadata, wg *sync.WaitGroup, db *EmoteDatabase) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case task, ok := <-tasks:
			if !ok {
				return
			}
			processEmote(archiveDir, task, results, db)
		}
	}
}

func processEmote(archiveDir string, task EmoteTask, results chan *EmoteMetadata, db *EmoteDatabase) {
	emoteURL := fmt.Sprintf("https://files.kick.com/emotes/%s/fullsize", task.ID)
	resp, err := httpHeadWithRetry(emoteURL)
	if err != nil {
		atomic.AddInt64(&stats.FailedEmotes, 1)
		return
	}
	defer resp.Body.Close()
	lastMod, _ := http.ParseTime(resp.Header.Get("Last-Modified"))

	db.mu.RLock()
	existing, exists := db.Emotes[task.ID]
	db.mu.RUnlock()
	if exists {
		for _, ver := range existing.Versions {
			if ver.LastModified.Equal(lastMod) {
				return
			}
		}
	}

	resp, err = httpGetWithRetry(emoteURL)
	if err != nil {
		atomic.AddInt64(&stats.FailedEmotes, 1)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	hash := sha256.Sum256(data)
	ext := "png"
	if strings.Contains(resp.Header.Get("Content-Type"), "gif") {
		ext = "gif"
	}

	emotesDir := filepath.Join(archiveDir, "Emotes")
	os.MkdirAll(emotesDir, 0755)

	var filename string
	if exists {
		filename = fmt.Sprintf("%s_%s_%s.%s", task.Name, task.ID, lastMod.Format("20060102_150405"), ext)
	} else {
		filename = fmt.Sprintf("%s_%s.%s", task.Name, task.ID, ext)
	}
	filePath := filepath.Join(emotesDir, filename)
	atomicWriteFile(filePath, data)
	os.Chtimes(filePath, lastMod, lastMod)

	newVersion := EmoteVersion{FilePath: filepath.Join("Emotes", filename), LastModified: lastMod, SHA256: hex.EncodeToString(hash[:])}
	if exists {
		existing.Versions = append(existing.Versions, newVersion)
		results <- existing
	} else {
		results <- &EmoteMetadata{ID: task.ID, Name: task.Name, Versions: []EmoteVersion{newVersion}}
	}
	atomic.AddInt64(&stats.TotalEmotes, 1)
}

func loadEmoteDatabase(archiveDir string) *EmoteDatabase {
	db := &EmoteDatabase{Emotes: make(map[string]*EmoteMetadata)}
	data, err := os.ReadFile(filepath.Join(archiveDir, "Emotes", "emotes.json"))
	if err == nil {
		json.Unmarshal(data, &db)
	}
	return db
}

// emoteMetadataSaver periodically saves the emote database to disk.
func emoteMetadataSaver(ctx context.Context, archiveDir string, db *EmoteDatabase, results chan *EmoteMetadata) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	save := func() {
		db.mu.RLock()
		defer db.mu.RUnlock()
		data, _ := json.MarshalIndent(db, "", "  ")
		atomicWriteFile(filepath.Join(archiveDir, "Emotes", "emotes.json"), data)
	}
	for {
		select {
		case <-ctx.Done():
			save()
			return
		case metadata, ok := <-results:
			if !ok {
				save()
				return
			}
			db.mu.Lock()
			db.Emotes[metadata.ID] = metadata
			db.mu.Unlock()
		case <-ticker.C:
			save()
		}
	}
}

func trackPinnedMessages(resp *ChatResponse, events *[]PinnedMessageEvent, currentID *string, timestamp time.Time) {
	if len(resp.Data.PinnedMessage) == 0 || string(resp.Data.PinnedMessage) == "null" {
		if *currentID != "" {
			for i := range *events {
				if (*events)[i].UnpinnedAt == "" {
					(*events)[i].UnpinnedAt = timestamp.Format(time.RFC3339)
					break
				}
			}
			*currentID = ""
		}
	} else {
		var pinnedMsg map[string]interface{}
		json.Unmarshal(resp.Data.PinnedMessage, &pinnedMsg)
		if id, ok := pinnedMsg["id"].(string); ok && id != *currentID {
			*events = append(*events, PinnedMessageEvent{PinnedAt: timestamp.Format(time.RFC3339), Message: resp.Data.PinnedMessage})
			*currentID = id
		}
	}
}

// mergeChatFiles combines all temporary part files into a single final chat log.
func mergeChatFiles(archiveDir string, metadata *VODMetadata, pinnedEvents []PinnedMessageEvent) error {
	logz("info", EMOJI_PAPER, "Merging chat files...")
	matches, _ := filepath.Glob(filepath.Join(archiveDir, fmt.Sprintf("chat_%s.*.part.json", metadata.UUID)))
	sort.Strings(matches)
	var allMessages []ChatMessage
	var lastCursor string
	uniqueEmotes := make(map[string]bool)
	for _, match := range matches {
		data, _ := os.ReadFile(match)
		var resp ChatResponse
		if json.Unmarshal(data, &resp) == nil {
			allMessages = append(allMessages, resp.Data.Messages...)
			lastCursor = resp.Data.Cursor
			for _, msg := range resp.Data.Messages {
				re := regexp.MustCompile(`\[emote:(\d+):([a-zA-Z0-9]+)\]`)
				for _, emMatch := range re.FindAllStringSubmatch(msg.Content, -1) {
					uniqueEmotes[emMatch[1]+":"+emMatch[2]] = true
				}
			}
		}
	}
	output := FinalChatOutput{}
	output.Data.Messages, output.Data.Cursor, output.Data.PinnedMessages = allMessages, lastCursor, pinnedEvents
	output.Stats.TotalMessages, output.Stats.UniqueEmotes = int64(len(allMessages)), int64(len(uniqueEmotes))
	output.Stats.DownloadedEmotes = atomic.LoadInt64(&stats.TotalEmotes)
	output.Stats.FailedEmotes = atomic.LoadInt64(&stats.FailedEmotes)

	saveJSONAtomic(filepath.Join(archiveDir, fmt.Sprintf("chat_%s.json", metadata.UUID)), output)
	for _, match := range matches {
		os.Remove(match)
	}

	logz("ok", EMOJI_CHECK, "Merged %d messages into final chat file", len(allMessages))
	return nil
}

func httpGetWithRetry(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	for i := 0; i < *retriesFlag; i++ {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			return resp, nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		if i < *retriesFlag-1 {
			time.Sleep(time.Duration(i+1) * time.Second)
		}
	}
	return nil, fmt.Errorf("GET failed after %d retries", *retriesFlag)
}

func httpHeadWithRetry(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	for i := 0; i < *retriesFlag; i++ {
		req, _ := http.NewRequest("HEAD", url, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			return resp, nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		if i < *retriesFlag-1 {
			time.Sleep(time.Duration(i+1) * time.Second)
		}
	}
	return nil, fmt.Errorf("HEAD failed after %d retries", *retriesFlag)
}

func saveJSONAtomic(path string, data interface{}) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return err
	}
	return atomicWriteFile(path, buf.Bytes())
}

// atomicWriteFile writes data to a temporary file first, then renames it to the final destination.
// This prevents corrupted files if the program is interrupted during a write.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmpFile.Name(), path)
}

func printStatistics(vodComplete, chatComplete bool, vodErr, chatErr error) {
	duration := time.Since(stats.StartTime)
	logz("info", "", "\n"+strings.Repeat("=", 50))
	logz("info", EMOJI_STATS, "Archive Statistics")
	logz("info", "", strings.Repeat("=", 50))
	logz("info", EMOJI_CLOCK, "Total Time: %s", formatDuration(duration))

	stats.mu.Lock()
	vodDuration, chatDuration := stats.VODDownloadDuration, stats.ChatDownloadDuration
	stats.mu.Unlock()

	vodSize := atomic.LoadInt64(&stats.VODSize)
	if vodDuration > 0 {
		logz("info", EMOJI_FILM, "VOD Download Time: %s", formatDuration(vodDuration))
		if vodSize > 0 {
			speed := float64(vodSize) / vodDuration.Seconds()
			logz("info", "", "   - VOD Size: %s (Avg speed: %s/s)", formatBytes(vodSize), formatBytes(int64(speed)))
		}
	}

	totalMsgs := atomic.LoadInt64(&stats.TotalMessages)
	if chatDuration > 0 {
		logz("info", EMOJI_CHAT, "Chat Download Time: %s", formatDuration(chatDuration))
		if totalMsgs > 0 {
			msgsPerSec := float64(totalMsgs) / chatDuration.Seconds()
			logz("info", "", "   - Total Messages: %d (Avg %.2f msgs/s)", totalMsgs, msgsPerSec)
		}
		logz("info", "", "   - Emotes Downloaded: %d (failed: %d)", atomic.LoadInt64(&stats.TotalEmotes), atomic.LoadInt64(&stats.FailedEmotes))
	}

	if apiCalls := atomic.LoadInt64(&stats.APICalls); apiCalls > 0 {
		stats.mu.Lock()
		avgResponseTime := stats.AvgAPIResponseTime
		stats.mu.Unlock()
		logz("info", EMOJI_WORLD, "API Calls: %d (avg response: %s)", apiCalls, avgResponseTime.Round(time.Millisecond))
	}

	logz("info", "", strings.Repeat("=", 50))

	if vodComplete && chatComplete {
		logz("ok", EMOJI_PARTY, "Archive complete!")
	} else {
		logz("plain_warn", EMOJI_WARN, "Archive incomplete:")
		if !vodComplete {
			if vodErr != nil {
				logz("plain_error", EMOJI_CROSS, "   - VOD: Failed (%v)", vodErr)
			} else {
				logz("plain_warn", EMOJI_WARN, "   - VOD: Not downloaded (skipped or incomplete)")
			}
		} else {
			logz("ok", EMOJI_CHECK, "   - VOD: Complete")
		}
		if !chatComplete {
			if chatErr != nil {
				logz("plain_error", EMOJI_CROSS, "   - Chat: Failed (%v)", chatErr)
			} else {
				logz("plain_warn", EMOJI_WARN, "   - Chat: Incomplete (can be resumed)")
			}
		} else {
			logz("ok", EMOJI_CHECK, "   - Chat: Complete")
		}
	}
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := d/time.Hour, (d%time.Hour)/time.Minute, (d%time.Minute)/time.Second
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// renderProgress displays a rich, multi-line progress status with bars.
func renderProgress(ctx context.Context, state *ProgressState, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			printProgressLine(state)
			fmt.Println()
			return
		case <-ticker.C:
			state.mu.Lock()
			done := state.VODDone && state.ChatDone
			state.mu.Unlock()
			printProgressLine(state)
			if done {
				return
			}
		}
	}
}

func printProgressLine(state *ProgressState) {
	state.mu.Lock()
	defer state.mu.Unlock()

	var vodPart, chatPart, vodBar, chatBar string
	var vodPercentVal, chatPercentVal float64

	vodEmoji := EMOJI_FILM
	chatEmoji := EMOJI_CHAT
	if *noEmoji {
		vodEmoji = "[VOD]"
		chatEmoji = "[CHAT]"
	}

	if state.VODDone {
		vodPart = fmt.Sprintf("%s: %s Complete", vodEmoji, EMOJI_CHECK)
		vodPercentVal = 100.0
	} else if state.VODStatusMessage != "" {
		vodPart = fmt.Sprintf("%s: %s", vodEmoji, state.VODStatusMessage)
		vodPercentVal = 100.0
	} else if state.VODPercent == "" {
		vodPart = fmt.Sprintf("%s: Starting...", vodEmoji)
	} else {
		vodPart = fmt.Sprintf("%s: %s %s @ %s ETA: %s", vodEmoji, state.VODPercent, state.VODSize, state.VODSpeed, state.VODETA)
		vodPercentVal, _ = strconv.ParseFloat(strings.TrimSuffix(state.VODPercent, "%"), 64)
	}

	if state.ChatDone {
		chatPart = fmt.Sprintf("%s: %s Complete", chatEmoji, EMOJI_CHECK)
		chatPercentVal = 100.0
	} else if state.ChatPercent == "" {
		chatPart = fmt.Sprintf("%s: Starting...", chatEmoji)
	} else {
		chatPart = fmt.Sprintf("%s: %s | %s (%s) msgs | %s (%s) emotes | ETA: %s",
			chatEmoji, state.ChatPercent, state.Messages, state.ChatMsgsInBatch, state.Emotes, state.ChatEmotesInBatch, state.ChatETA)
		chatPercentVal, _ = strconv.ParseFloat(strings.TrimSuffix(state.ChatPercent, "%"), 64)
	}

	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width = 80
	}

	vodBar = generateProgressBar(vodPercentVal, width, "\033[32m")
	chatBar = generateProgressBar(chatPercentVal, width, "\033[36m")

	fmt.Printf("\r\033[K%s\n\033[K%s\n\033[K%s\n\033[K%s\033[3A", vodPart, vodBar, chatPart, chatBar)
}

func generateProgressBar(percent float64, width int, colorCode string) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	barWidth := width - 2
	filled := int(float64(barWidth) * (percent / 100.0))

	head := "╸"
	if filled >= barWidth || filled <= 0 {
		head = ""
	}

	filledWidth := filled
	if filled > 0 {
		filledWidth = filled - 1
	}

	return fmt.Sprintf("%s%s%s%s\033[90m%s\033[0m%s",
		colorCode, "▕", strings.Repeat("━", filledWidth), head, strings.Repeat(" ", barWidth-filled), "▏")
}

// renderSimpleProgress displays a compact, single-line progress status.
func renderSimpleProgress(ctx context.Context, state *ProgressState, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			printSimpleProgressLine(state)
			fmt.Println()
			return
		case <-ticker.C:
			state.mu.Lock()
			done := state.VODDone && state.ChatDone
			state.mu.Unlock()
			printSimpleProgressLine(state)
			if done {
				fmt.Println() // Print a final newline
				return
			}
		}
	}
}

func printSimpleProgressLine(state *ProgressState) {
	state.mu.Lock()
	defer state.mu.Unlock()

	var vodPart, chatPart string

	if state.VODDone {
		vodPart = "VOD: Complete"
	} else if state.VODStatusMessage != "" {
		vodPart = "VOD: " + state.VODStatusMessage
	} else if state.VODPercent == "" {
		vodPart = "VOD: Starting..."
	} else {
		vodPart = fmt.Sprintf("VOD: %s (%s @ %s ETA: %s)", state.VODPercent, state.VODSize, state.VODSpeed, state.VODETA)
	}

	if state.ChatDone {
		chatPart = "Chat: Complete"
	} else if state.ChatPercent == "" {
		chatPart = "Chat: Starting..."
	} else {
		chatPart = fmt.Sprintf("Chat: %s (%s msgs, %s emotes ETA: %s)", state.ChatPercent, state.Messages, state.Emotes, state.ChatETA)
	}

	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width = 80
	}

	line := fmt.Sprintf("%s | %s", vodPart, chatPart)
	if len(line) > width {
		line = line[:width]
	}
	padding := ""
	if len(line) < width {
		padding = strings.Repeat(" ", width-len(line))
	}

	fmt.Printf("\r\033[K%s%s", line, padding)
}
