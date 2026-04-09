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
	"codient/internal/tools"
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

	s.setupPerModeModels(ctx, sc, models)
	s.setupWebSearch(ctx, sc)

	if err := saveCurrentConfig(s.cfg); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not save config: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "\n  Configuration saved. Model set to %s.\n", s.cfg.Model)
	if s.cfg.HasModeOverrides() {
		for _, m := range []string{"plan", "build", "ask"} {
			base, _, em := s.cfg.EffectiveModelConfig(m)
			if em != s.cfg.Model || (s.cfg.Models[m] != nil && s.cfg.Models[m].BaseURL != "") {
				if s.cfg.Models[m] != nil && s.cfg.Models[m].BaseURL != "" {
					fmt.Fprintf(os.Stderr, "  %s: %s @ %s\n", m, em, base)
				} else if em != s.cfg.Model {
					fmt.Fprintf(os.Stderr, "  %s model: %s\n", m, em)
				}
			}
		}
	}
	fmt.Fprintf(os.Stderr, "\n")
	return true
}

// setupSeparatePlanEndpoint optionally points plan mode at another OpenAI-compatible server
// (e.g. cloud for design, local LM for implementation).
func (s *session) setupSeparatePlanEndpoint(ctx context.Context, sc *bufio.Scanner) {
	fmt.Fprintf(os.Stderr, "\n  Use a different API base URL for plan mode (e.g. cloud) while keeping your default server for build? [y/N] ")
	if !sc.Scan() {
		return
	}
	answer := strings.ToLower(strings.TrimSpace(sc.Text()))
	if answer != "y" && answer != "yes" {
		return
	}

	planURL := promptWithDefault(sc, "  Plan mode base URL (include /v1)", "")
	planURL = strings.TrimRight(strings.TrimSpace(planURL), "/")
	if planURL == "" {
		fmt.Fprintf(os.Stderr, "  Skipped (empty URL). You can set plan_base_url later with /config.\n")
		return
	}

	planKey := promptWithDefault(sc, "  Plan mode API key", s.cfg.APIKey)
	probe := openaiclient.NewFromParams(planURL, planKey, "probe", s.cfg.MaxConcurrent)
	fmt.Fprintf(os.Stderr, "\n  Connecting to plan server %s ...\n", planURL)
	planModels, err := probe.ListModels(ctx)
	if err != nil || len(planModels) == 0 {
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Could not list models: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  Server returned no models.\n")
		}
		fmt.Fprintf(os.Stderr, "  You can fix this later with /config plan_base_url, plan_api_key, plan_model.\n\n")
		return
	}

	fmt.Fprintf(os.Stderr, "\n  Models on plan server:\n\n")
	for i, m := range planModels {
		fmt.Fprintf(os.Stderr, "    %d) %s\n", i+1, m)
	}
	chosen := pickModelFromList(sc, planModels, "  Plan mode model", planModels[0])

	if s.cfg.Models == nil {
		s.cfg.Models = make(map[string]*config.ModelProfile)
	}
	s.cfg.Models["plan"] = &config.ModelProfile{
		BaseURL: planURL,
		APIKey:  planKey,
		Model:   chosen,
	}
	fmt.Fprintf(os.Stderr, "  Plan mode will use %q @ %s\n", chosen, planURL)
}

// setupPerModeModels optionally configures different models (same server) or a separate plan endpoint.
func (s *session) setupPerModeModels(ctx context.Context, sc *bufio.Scanner, models []string) {
	s.setupSeparatePlanEndpoint(ctx, sc)

	planRemote := s.cfg.Models != nil && s.cfg.Models["plan"] != nil && s.cfg.Models["plan"].BaseURL != ""

	if len(models) < 2 && !planRemote {
		return
	}

	fmt.Fprintf(os.Stderr, "\n  You can use different models on your **default** server for plan and build.\n")
	fmt.Fprintf(os.Stderr, "  Configure per-mode models on this server? [y/N] ")
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

	fmt.Fprintf(os.Stderr, "\n  Available models on default server:\n\n")
	for i, m := range models {
		fmt.Fprintf(os.Stderr, "    %d) %s\n", i+1, m)
	}

	if !planRemote {
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
	} else {
		fmt.Fprintf(os.Stderr, "  Plan mode already uses a separate server; skipping local plan model.\n")
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

func (s *session) setupWebSearch(ctx context.Context, sc *bufio.Scanner) {
	for _, u := range searxngDiscoveryURLs(s.cfg.SearchBaseURL) {
		if tools.ProbeSearxng(ctx, u) {
			s.cfg.SearchBaseURL = u
			fmt.Fprintf(os.Stderr, "\n  SearXNG is already available at %s. Web search enabled.\n\n", u)
			return
		}
	}

	fmt.Fprintf(os.Stderr, "\n  Web search (optional) lets the agent look up documentation and API references.\n")
	fmt.Fprintf(os.Stderr, "  It requires a SearXNG instance (runs in Docker).\n\n")

	if s.cfg.SearchBaseURL != "" {
		fmt.Fprintf(os.Stderr, "  Configured URL %s did not respond; you can fix it below.\n\n", s.cfg.SearchBaseURL)
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
		if port < 1 {
			port = defaultSearxngPort
		}
		url := fmt.Sprintf("http://127.0.0.1:%d", port)
		fmt.Fprintf(os.Stderr, "\n  SearXNG container is running; using %s.\n", url)
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
