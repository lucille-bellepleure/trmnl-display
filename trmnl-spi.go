package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ChristianHering/WaveShare" // Waveshare e-Paper library
	"github.com/disintegration/imaging"     // For image processing
	_ "image/jpeg"                         // Register JPEG decoder
	_ "image/png"                          // Register PNG decoder
)

// Version information
var (
	version   = "0.1.0"
	commit    = "unknown"
	buildDate = "unknown"
)

// TerminalResponse represents the JSON structure returned by the API
type TerminalResponse struct {
	ImageURL    string `json:"image_url"`
	Filename    string `json:"filename"`
	RefreshRate int    `json:"refresh_rate"`
}

// Config holds application configuration
type Config struct {
	APIKey string
}

// AppOptions holds command line options
type AppOptions struct {
	DarkMode bool
	Verbose  bool
}

// SPIConfig holds SPI and GPIO pin configuration for the Waveshare e-ink display
type SPIConfig struct {
	RSTPin  int // Reset pin
	DCPin   int // Data/Command pin
	CSPin   int // Chip Select pin
	BusyPin int // Busy pin
	Width   int // Display width in pixels
	Height  int // Display height in pixels
}

var (
	// SPI configuration for EPD7in5_V2
	spiConfig = SPIConfig{
		RSTPin:  17,  // GPIO17
		DCPin:   25,  // GPIO25
		CSPin:   8,   // GPIO8 (SPI0 CS0)
		BusyPin: 24,  // GPIO24
		Width:   800, // EPD7in5_V2 resolution: 800x480
		Height:  480,
	}
	// Global Waveshare display instance for 7.5" V2
	display *WaveShare.EPD7in5V2
)

func main() {
	options := parseCommandLineArgs()

	err := initDisplay()
	if err != nil {
		fmt.Printf("Error initializing e-ink display: %v\n", err)
		os.Exit(1)
	}
	defer cleanupDisplay()

	configDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Error getting home directory: %v\n", err)
		os.Exit(1)
	}
	configDir = filepath.Join(configDir, ".trmnl")
	err = os.MkdirAll(configDir, 0755)
	if err != nil {
		fmt.Printf("Error creating config directory: %v\n", err)
		os.Exit(1)
	}

	config := loadConfig(configDir)
	if config.APIKey == "" {
		config.APIKey = os.Getenv("TRMNL_API_KEY")
	}
	if config.APIKey == "" {
		fmt.Println("TRMNL API Key not found.")
		fmt.Print("Please enter your TRMNL API Key: ")
		fmt.Scanln(&config.APIKey)
		saveConfig(configDir, config)
	}

	tmpDir, err := os.MkdirTemp("", "trmnl-display")
	if err != nil {
		fmt.Printf("Error creating temp directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	clearDisplay()

	for {
		processNextImage(tmpDir, config.APIKey, options)
	}
}

// initDisplay initializes the Waveshare 7.5" V2 e-ink display
func initDisplay() error {
	display = WaveShare.NewEPD7in5V2(spiConfig.RSTPin, spiConfig.DCPin, spiConfig.CSPin, spiConfig.BusyPin)
	err := display.Init()
	if err != nil {
		return fmt.Errorf("failed to initialize Waveshare EPD7in5_V2: %v", err)
	}
	fmt.Println("Waveshare 7.5\" e-ink display (V2) initialized successfully")
	return nil
}

// cleanupDisplay handles cleanup on exit
func cleanupDisplay() {
	if display != nil {
		display.Sleep()
		fmt.Println("Waveshare 7.5\" e-ink display put to sleep")
	}
}

// clearDisplay clears the e-ink display
func clearDisplay() {
	fmt.Println("Clearing e-ink display...")
	err := display.Clear(0xFF) // 0xFF = white
	if err != nil {
		fmt.Printf("Error clearing display: %v\n", err)
	}
}

// setupSignalHandling sets up handlers for SIGINT, SIGTERM, and SIGHUP
func setupSignalHandling() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-c
		fmt.Println("\nReceived termination signal. Cleaning up...")
		cleanupDisplay()
		os.Exit(0)
	}()
}

// processNextImage handles fetching and displaying images
func processNextImage(tmpDir, apiKey string, options AppOptions) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Recovered from panic: %v\n", r)
			time.Sleep(60 * time.Second)
		}
	}()

	req, err := http.NewRequest("GET", "https://usetrmnl.com/api/display", nil)
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		time.Sleep(60 * time.Second)
		return
	}

	req.Header.Add("access-token", apiKey)
	req.Header.Add("User-Agent", fmt.Sprintf("trmnl-display/%s", version))
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Error fetching display: %v\n", err)
		time.Sleep(60 * time.Second)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		fmt.Printf("Error fetching display: status code %d\n", resp.StatusCode)
		time.Sleep(60 * time.Second)
		return
	}

	var terminal TerminalResponse
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&terminal); err != nil {
		fmt.Printf("Error parsing JSON: %v\n", err)
		time.Sleep(60 * time.Second)
		return
	}

	filename := terminal.Filename
	if filename == "" {
		filename = "display.jpg"
	}
	filePath := filepath.Join(tmpDir, filename)

	imgResp, err := http.Get(terminal.ImageURL)
	if err != nil {
		fmt.Printf("Error downloading image: %v\n", err)
		time.Sleep(60 * time.Second)
		return
	}
	defer imgResp.Body.Close()

	out, err := os.Create(filePath)
	if err != nil {
		fmt.Printf("Error creating file: %v\n", err)
		time.Sleep(60 * time.Second)
		return
	}
	_, err = io.Copy(out, imgResp.Body)
	if err != nil {
		fmt.Printf("Error saving image: %v\n", err)
		out.Close()
		time.Sleep(60 * time.Second)
		return
	}
	out.Close()

	err = displayImage(filePath, options)
	if err != nil {
		fmt.Printf("Error displaying image: %v\n", err)
		time.Sleep(60 * time.Second)
		return
	}

	refreshRate := terminal.RefreshRate
	if refreshRate <= 0 {
		refreshRate = 60
	}
	time.Sleep(time.Duration(refreshRate) * time.Second)
}

// displayImage processes and sends the image to the Waveshare e-ink display
func displayImage(imagePath string, options AppOptions) error {
	file, err := os.Open(imagePath)
	if err != nil {
		return fmt.Errorf("error opening image file: %v", err)
	}
	defer file.Close()

	if options.Verbose {
		fmt.Printf("Reading image from %s\n", imagePath)
	}

	img, _, err := image.Decode(file)
	if err != nil {
		return fmt.Errorf("error decoding image: %v", err)
	}

	// Resize image to match EPD7in5_V2 dimensions (800x480)
	resizedImg := imaging.Resize(img, spiConfig.Width, spiConfig.Height, imaging.NearestNeighbor)

	// Convert to monochrome (1-bit) for e-ink
	monoImg := image.NewGray(resizedImg.Bounds())
	threshold := uint8(128) // Adjust threshold as needed
	for y := 0; y < resizedImg.Bounds().Dy(); y++ {
		for x := 0; x < resizedImg.Bounds().Dx(); x++ {
			r, g, b, _ := resizedImg.At(x, y).RGBA()
			gray := uint8((r*299 + g*587 + b*114) / 1000 >> 8) // ITU-R 601-2 luma transform
			if options.DarkMode {
				if gray < threshold {
					monoImg.SetGray(x, y, color.Gray{255}) // White
				} else {
					monoImg.SetGray(x, y, color.Gray{0}) // Black
				}
			} else {
				if gray >= threshold {
					monoImg.SetGray(x, y, color.Gray{255}) // White
				} else {
					monoImg.SetGray(x, y, color.Gray{0}) // Black
				}
			}
		}
	}

	// Convert to Waveshare-compatible buffer (800x480 = 38400 bytes, 1 bit per pixel)
	buffer := make([]byte, spiConfig.Width*spiConfig.Height/8)
	for y := 0; y < spiConfig.Height; y++ {
		for x := 0; x < spiConfig.Width; x++ {
			if monoImg.GrayAt(x, y).Y == 0 { // Black pixel
				bytePos := (y*spiConfig.Width + x) / 8
				bitPos := 7 - (x % 8)
				buffer[bytePos] |= 1 << bitPos
			}
		}
	}

	// Display the image
	err = display.Display(buffer)
	if err != nil {
		return fmt.Errorf("error displaying image on Waveshare EPD7in5_V2: %v", err)
	}

	if options.Verbose {
		fmt.Println("Image displayed on Waveshare 7.5\" e-ink display")
	}
	return nil
}

// parseCommandLineArgs parses command line arguments
func parseCommandLineArgs() AppOptions {
	darkMode := flag.Bool("d", false, "Enable dark mode (invert monochrome images)")
	showVersion := flag.Bool("v", false, "Show version information")
	verbose := flag.Bool("verbose", true, "Enable verbose output")
	quiet := flag.Bool("q", false, "Quiet mode (disable verbose output)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("trmnl-display version %s (commit: %s, built: %s)\n", version, commit, buildDate)
		os.Exit(0)
	}

	return AppOptions{
		DarkMode: *darkMode,
		Verbose:  *verbose && !*quiet,
	}
}

// Helper functions (loadConfig, saveConfig)
func loadConfig(configDir string) Config {
	configFile := filepath.Join(configDir, "config.json")
	config := Config{}
	data, err := os.ReadFile(configFile)
	if err != nil {
		return config
	}
	_ = json.Unmarshal(data, &config)
	return config
}

func saveConfig(configDir string, config Config) {
	configFile := filepath.Join(configDir, "config.json")
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fmt.Printf("Error saving config: %v\n", err)
		return
	}
	err = os.WriteFile(configFile, data, 0600)
	if err != nil {
		fmt.Printf("Error writing config file: %v\n", err)
	}
}