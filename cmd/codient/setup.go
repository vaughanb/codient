package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"codient/internal/config"
	"codient/internal/openaiclient"
)

// runSetupWizard walks the user through configuring their API connection
// and selecting a model. It returns true if the setup completed successfully.
func (s *session) runSetupWizard(ctx context.Context, sc *bufio.Scanner) bool {
	fmt.Fprintf(os.Stderr, "\n  Welcome! Let's connect to your OpenAI-compatible API.\n\n")

	baseURL := promptWithDefault(sc, "  Base URL", s.cfg.BaseURL)
	apiKey := promptWithDefault(sc, "  API key", s.cfg.APIKey)

	s.cfg.BaseURL = strings.TrimRight(baseURL, "/")
	s.cfg.APIKey = apiKey
	s.client = openaiclient.New(s.cfg)

	fmt.Fprintf(os.Stderr, "\n  Connecting to %s ...\n", s.cfg.BaseURL)
	models, err := s.client.ListModels(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Could not fetch models: %v\n", err)
		fmt.Fprintf(os.Stderr, "  You can set the model manually later with /config model <name>\n\n")
		if err := saveCurrentConfig(s.cfg); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not save config: %v\n", err)
		}
		return false
	}

	if len(models) == 0 {
		fmt.Fprintf(os.Stderr, "  Server returned no models.\n")
		fmt.Fprintf(os.Stderr, "  You can set the model manually later with /config model <name>\n\n")
		if err := saveCurrentConfig(s.cfg); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not save config: %v\n", err)
		}
		return false
	}

	fmt.Fprintf(os.Stderr, "\n  Available models:\n\n")
	for i, m := range models {
		fmt.Fprintf(os.Stderr, "    %d) %s\n", i+1, m)
	}
	fmt.Fprintf(os.Stderr, "\n")

	for {
		fmt.Fprintf(os.Stderr, "  Select a model [1-%d]: ", len(models))
		if !sc.Scan() {
			return false
		}
		input := strings.TrimSpace(sc.Text())
		if input == "" {
			continue
		}
		n, err := strconv.Atoi(input)
		if err != nil || n < 1 || n > len(models) {
			// Allow typing the model name directly.
			for _, m := range models {
				if strings.EqualFold(m, input) {
					s.cfg.Model = m
					if err := saveCurrentConfig(s.cfg); err != nil {
						fmt.Fprintf(os.Stderr, "  Warning: could not save config: %v\n", err)
					}
					fmt.Fprintf(os.Stderr, "\n  Configuration saved. Model set to %s.\n\n", s.cfg.Model)
					return true
				}
			}
			fmt.Fprintf(os.Stderr, "  Please enter a number between 1 and %d.\n", len(models))
			continue
		}
		s.cfg.Model = models[n-1]
		break
	}

	s.setupPerModeModels(sc, models)
	s.setupWebSearch(sc)

	if err := saveCurrentConfig(s.cfg); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not save config: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "\n  Configuration saved. Model set to %s.\n", s.cfg.Model)
	if s.cfg.HasModeOverrides() {
		for _, m := range []string{"plan", "build"} {
			if em := s.cfg.EffectiveModel(m); em != s.cfg.Model {
				fmt.Fprintf(os.Stderr, "  %s model: %s\n", m, em)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "\n")
	return true
}

// setupPerModeModels optionally configures different models for plan and build modes.
func (s *session) setupPerModeModels(sc *bufio.Scanner, models []string) {
	if len(models) < 2 {
		return
	}

	fmt.Fprintf(os.Stderr, "\n  You can use different models for planning (code analysis) and building (writing code).\n")
	fmt.Fprintf(os.Stderr, "  Configure per-mode models? [y/N] ")
	if !sc.Scan() {
		return
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	if answer != "y" && answer != "yes" {
		return
	}

	if s.cfg.Models == nil {
		s.cfg.Models = make(map[string]*config.ModelProfile)
	}

	fmt.Fprintf(os.Stderr, "\n  Available models:\n\n")
	for i, m := range models {
		fmt.Fprintf(os.Stderr, "    %d) %s\n", i+1, m)
	}

	planModel := pickModelFromList(sc, models, "  Plan model (code analysis, implementation design)", s.cfg.Model)
	if planModel != "" && planModel != s.cfg.Model {
		if s.cfg.Models["plan"] == nil {
			s.cfg.Models["plan"] = &config.ModelProfile{}
		}
		s.cfg.Models["plan"].Model = planModel
		fmt.Fprintf(os.Stderr, "  Plan model set to %s.\n", planModel)
	} else {
		delete(s.cfg.Models, "plan")
		fmt.Fprintf(os.Stderr, "  Plan model: same as default (%s).\n", s.cfg.Model)
	}

	buildModel := pickModelFromList(sc, models, "  Build model (writing and editing code)", s.cfg.Model)
	if buildModel != "" && buildModel != s.cfg.Model {
		if s.cfg.Models["build"] == nil {
			s.cfg.Models["build"] = &config.ModelProfile{}
		}
		s.cfg.Models["build"].Model = buildModel
		fmt.Fprintf(os.Stderr, "  Build model set to %s.\n", buildModel)
	} else {
		delete(s.cfg.Models, "build")
		fmt.Fprintf(os.Stderr, "  Build model: same as default (%s).\n", s.cfg.Model)
	}

	if len(s.cfg.Models) == 0 {
		s.cfg.Models = nil
	}
}

// pickModelFromList prompts the user to select a model by number or name.
// Returns the chosen model, or defaultModel if the user presses Enter.
func pickModelFromList(sc *bufio.Scanner, models []string, label, defaultModel string) string {
	fmt.Fprintf(os.Stderr, "\n%s [%s]: ", label, defaultModel)
	if !sc.Scan() {
		return defaultModel
	}
	input := strings.TrimSpace(sc.Text())
	if input == "" {
		return defaultModel
	}
	n, err := strconv.Atoi(input)
	if err == nil && n >= 1 && n <= len(models) {
		return models[n-1]
	}
	for _, m := range models {
		if strings.EqualFold(m, input) {
			return m
		}
	}
	fmt.Fprintf(os.Stderr, "  Unrecognized model %q, keeping default.\n", input)
	return defaultModel
}

func (s *session) setupWebSearch(sc *bufio.Scanner) {
	fmt.Fprintf(os.Stderr, "\n  Web search (optional) lets the agent look up documentation and API references.\n")
	fmt.Fprintf(os.Stderr, "  It requires a SearXNG instance (runs in Docker).\n\n")

	if s.cfg.SearchBaseURL != "" {
		fmt.Fprintf(os.Stderr, "  Currently configured: %s\n\n", s.cfg.SearchBaseURL)
	}

	fmt.Fprintf(os.Stderr, "  [1] Auto-install SearXNG via Docker (recommended)\n")
	fmt.Fprintf(os.Stderr, "  [2] I already have SearXNG running — enter URL\n")
	fmt.Fprintf(os.Stderr, "  [3] Skip web search\n\n")

	for {
		fmt.Fprintf(os.Stderr, "  Choice [1-3]: ")
		if !sc.Scan() {
			return
		}
		switch strings.TrimSpace(sc.Text()) {
		case "1":
			s.setupWebSearchDocker(sc)
			return
		case "2":
			s.setupWebSearchManual(sc)
			return
		case "3":
			if s.cfg.SearchBaseURL != "" {
				s.cfg.SearchBaseURL = ""
				fmt.Fprintf(os.Stderr, "  Web search disabled.\n")
			} else {
				fmt.Fprintf(os.Stderr, "  Skipped.\n")
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "  Please enter 1, 2, or 3.\n")
		}
	}
}

func (s *session) setupWebSearchDocker(sc *bufio.Scanner) {
	if !dockerAvailable() {
		fmt.Fprintf(os.Stderr, "\n  Docker is not installed or not running.\n")
		fmt.Fprintf(os.Stderr, "  Install Docker Desktop: https://www.docker.com/products/docker-desktop\n")
		fmt.Fprintf(os.Stderr, "  Then re-run /setup, or enter a SearXNG URL manually.\n\n")
		s.setupWebSearchManual(sc)
		return
	}

	if running, port := searxngContainerRunning(); running {
		url := fmt.Sprintf("http://localhost:%d", port)
		fmt.Fprintf(os.Stderr, "\n  SearXNG is already running at %s.\n", url)
		s.cfg.SearchBaseURL = url
		fmt.Fprintf(os.Stderr, "  Web search enabled (%s).\n", s.cfg.SearchBaseURL)
		return
	}

	portStr := promptWithDefault(sc, "  Port for SearXNG", strconv.Itoa(defaultSearxngPort))
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil || port < 1 || port > 65535 {
		fmt.Fprintf(os.Stderr, "  Invalid port, using default %d.\n", defaultSearxngPort)
		port = defaultSearxngPort
	}

	fmt.Fprintf(os.Stderr, "\n  Starting SearXNG on port %d (first run pulls the image, may take a minute)...\n\n", port)

	if err := startSearxng(port); err != nil {
		fmt.Fprintf(os.Stderr, "\n  Failed to start SearXNG: %v\n", err)
		fmt.Fprintf(os.Stderr, "  You can try manually: docker compose -f ~/.codient/docker/searxng/docker-compose.yml up -d\n\n")
		return
	}

	url := fmt.Sprintf("http://localhost:%d", port)
	fmt.Fprintf(os.Stderr, "\n  Waiting for SearXNG to be ready...")
	if err := waitForSearxng(url, 30*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, " timed out.\n")
		fmt.Fprintf(os.Stderr, "  The container is running but may still be starting up.\n")
		fmt.Fprintf(os.Stderr, "  Check manually: %s\n", url)
	} else {
		fmt.Fprintf(os.Stderr, " ready!\n")
	}

	s.cfg.SearchBaseURL = url
	fmt.Fprintf(os.Stderr, "  Web search enabled (%s).\n", s.cfg.SearchBaseURL)
}

func (s *session) setupWebSearchManual(sc *bufio.Scanner) {
	current := s.cfg.SearchBaseURL
	label := "  SearXNG base URL (leave empty to skip)"
	if current != "" {
		label = "  SearXNG base URL (empty to disable)"
	}
	u := promptWithDefault(sc, label, current)
	u = strings.TrimSpace(u)
	if u == "" {
		if current != "" {
			s.cfg.SearchBaseURL = ""
			fmt.Fprintf(os.Stderr, "  Web search disabled.\n")
		} else {
			fmt.Fprintf(os.Stderr, "  Skipped.\n")
		}
		return
	}
	s.cfg.SearchBaseURL = strings.TrimRight(u, "/")
	fmt.Fprintf(os.Stderr, "  Web search enabled (%s).\n", s.cfg.SearchBaseURL)
}

func promptWithDefault(sc *bufio.Scanner, label, def string) string {
	if def != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	}
	if !sc.Scan() {
		return def
	}
	v := strings.TrimSpace(sc.Text())
	if v == "" {
		return def
	}
	return v
}

func saveCurrentConfig(cfg *config.Config) error {
	pc := config.ConfigToPersistent(cfg)
	return config.SavePersistentConfig(pc)
}
