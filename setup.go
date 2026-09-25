package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// runSetup walks the user through `liniget -stp`, an interactive wizard
// that writes ~/.config/liniget/config.json with their defaults.
func runSetup() error {
	reader := bufio.NewReader(os.Stdin)
	cfg, err := loadConfig()
	if err != nil {
		fmt.Printf("Note: couldn't read existing config (%v); starting fresh.\n", err)
		cfg = defaultConfig()
	}

	fmt.Println("liniget setup")
	fmt.Println("Press Enter at any prompt to keep the current/default value shown in [brackets].")
	fmt.Println()

	cfg.Threads = askInt(reader, "Default number of threads per download", cfg.Threads, 1, 64)
	cfg.DownloadDir = askString(reader, "Default download directory", cfg.DownloadDir)

	fmt.Println("Default speed limit, e.g. 500k, 2m, 1g. Leave blank for unlimited.")
	limitStr := "unlimited"
	if cfg.SpeedLimit > 0 {
		limitStr = humanBytes(cfg.SpeedLimit) + "/s"
	}
	for {
		raw := askString(reader, "Speed limit", limitStr)
		if raw == "unlimited" || raw == "" {
			cfg.SpeedLimit = 0
			break
		}
		v, err := parseSize(strings.TrimSuffix(raw, "/s"))
		if err != nil {
			fmt.Printf("  Couldn't parse that (%v), try again.\n", err)
			continue
		}
		cfg.SpeedLimit = v
		break
	}

	cfg.OnExisting = askChoice(reader, "When the destination file already exists", cfg.OnExisting, []string{"skip", "overwrite", "rename"})
	cfg.Retries = askInt(reader, "Retries per chunk/file on failure", cfg.Retries, 0, 20)
	cfg.TimeoutSecs = askInt(reader, "Per-request timeout (seconds)", cfg.TimeoutSecs, 1, 600)

	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("couldn't save config: %w", err)
	}

	path, _ := configPath()
	fmt.Println()
	fmt.Printf("Saved to %s\n", path)
	fmt.Println("You're all set — try: liniget grab <url>")
	return nil
}

func askString(reader *bufio.Reader, prompt, def string) string {
	fmt.Printf("%s [%s]: ", prompt, def)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func askInt(reader *bufio.Reader, prompt string, def, min, max int) int {
	for {
		fmt.Printf("%s [%d]: ", prompt, def)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < min || n > max {
			fmt.Printf("  Please enter a whole number between %d and %d.\n", min, max)
			continue
		}
		return n
	}
}

func askChoice(reader *bufio.Reader, prompt, def string, choices []string) string {
	for {
		fmt.Printf("%s (%s) [%s]: ", prompt, strings.Join(choices, "/"), def)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "" {
			return def
		}
		for _, c := range choices {
			if c == line {
				return c
			}
		}
		fmt.Printf("  Please choose one of: %s\n", strings.Join(choices, ", "))
	}
}
