// grafeio — the terminal office (Go). Entry point.
//
//	grafeio                 live mode: spawn/attach `opencode serve` for <cwd>
//	grafeio --demo          touring mode: simulated events (explicitly labeled)
//	grafeio --server URL    attach to an existing server, don't spawn
//	grafeio --autokill 6s   exit after duration (CI / screenshot runs)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theboringhumane/grafeio/internal/app"
	"github.com/theboringhumane/grafeio/internal/backend"
	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/config"
	"github.com/theboringhumane/grafeio/internal/office"
	"github.com/theboringhumane/grafeio/internal/state"
)

func main() {
	demo := flag.Bool("demo", os.Getenv("GRAFEIO_DEMO") == "1", "run with simulated events")
	server := flag.String("server", "", "opencode serve URL (attach, don't spawn)")
	autokill := flag.Duration("autokill", 0, "exit after this duration (shots/CI)")
	theme := flag.String("theme", "", "color theme: noir|paper|mono|dracula|solarized")
	printCfg := flag.Bool("print-default-config", false, "print the default brain.json and exit")
	flag.Parse()

	if *printCfg {
		b, _ := json.MarshalIndent(config.Default(), "", "  ")
		fmt.Println(string(b))
		fmt.Fprintln(os.Stderr, "(written to "+config.Path()+" on first run)")
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[grafeio] brain.json: %v (using defaults)\n", err)
		cfg = config.Default()
	}
	if os.Getenv("GRAFEIO_SERVER") != "" && *server == "" {
		*server = os.Getenv("GRAFEIO_SERVER")
	}
	// theme precedence: --theme flag > GRAFEIO_THEME > brain.json ui.theme > persisted > default
	if os.Getenv("GRAFEIO_THEME") != "" && *theme == "" {
		*theme = os.Getenv("GRAFEIO_THEME")
	}
	if *theme == "" {
		*theme = cfg.UI.Theme
	}
	if *theme == "" {
		*theme = chrome.LoadPersistedTheme()
	}
	if *theme != "" {
		if chrome.SetTheme(*theme) {
			office.SetTheme(*theme)
		}
	}

	var b state.Backend
	if *demo {
		b = backend.NewDemo(cfg)
	} else {
		b = backend.NewLive(*server, mustGetwd(), cfg)
	}

	p := tea.NewProgram(app.New(b, cfg))

	// bridge backend goroutines -> tea loop
	go func() {
		if err := b.Start(func(e state.Event) { p.Send(e) }); err != nil {
			p.Send(state.Event{Kind: state.EvStatus, Text: "[grafeio] backend failed: " + err.Error()})
		}
	}()

	if *autokill > 0 {
		go func() {
			<-time.After(*autokill)
			p.Send(tea.Quit())
		}()
	}

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "[grafeio] fatal: %v\n", err)
		os.Exit(1)
	}
	_ = b.Stop()
}

func mustGetwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}
