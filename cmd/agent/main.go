// Command agent is the proxypanel node agent. It manages one Xray core process
// per node described by the panel snapshot and reports usage reliably.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"proxypanel/internal/agent/config"
	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/supervisor"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to the agent JSON configuration file")
	showVersion := flag.Bool("version", false, "print the agent version and exit")
	checkOnly := flag.Bool("check", false, "validate the configuration, print a summary and exit")
	acceptGeneration := flag.String("accept-generation", "",
		"explicitly accept a new panel control generation on this node (requires -config and a stopped agent)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: agent -config <file> [-check]\n")
		fmt.Fprintf(flag.CommandLine.Output(), "       agent -config <file> -accept-generation <control_generation>\n")
		fmt.Fprintf(flag.CommandLine.Output(), "       agent -version\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Printf("proxypanel-agent %s\n", config.AgentVersion)
		return nil
	}
	if *configPath == "" {
		flag.Usage()
		return fmt.Errorf("-config is required")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := cfg.CheckRuntime(); err != nil {
		return err
	}
	if *acceptGeneration != "" {
		// Local, deliberate reconcile action. The agent never adopts a new
		// control generation on its own: a restored backup could otherwise
		// re-authorize members that were revoked in the meantime.
		return acceptControlGeneration(cfg, *acceptGeneration)
	}
	if *checkOnly {
		fmt.Printf("configuration ok\n")
		fmt.Printf("  panel_url:  %s\n", cfg.PanelURL)
		fmt.Printf("  xray_path:  %s\n", cfg.XrayPath)
		fmt.Printf("  state_dir:  %s\n", cfg.StateDir)
		fmt.Printf("  intervals:  poll=%ds stats=%ds heartbeat=%ds\n",
			cfg.PollIntervalSeconds, cfg.StatsIntervalSeconds, cfg.HeartbeatIntervalSeconds)
		fmt.Printf("  registered: %t\n", cfg.Token != "" || cfg.RegistrationCode != "")
		return nil
	}

	logger := log.New(os.Stderr, "agent ", log.LstdFlags|log.LUTC)
	sup, err := supervisor.New(supervisor.Options{Config: cfg, Logger: logger})
	if err != nil {
		return err
	}
	defer sup.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return sup.Run(ctx)
}

// acceptControlGeneration records that an operator accepted the given control
// generation on this node. The marker is bound to the exact generation string and
// is consumed by the next successful apply; without it the agent keeps its last
// known authorization and reports coordination_required.
func acceptControlGeneration(cfg *config.Config, generation string) error {
	generation = strings.TrimSpace(generation)
	if generation == "" {
		return fmt.Errorf("-accept-generation needs the exact control generation string reported by the panel")
	}
	store, err := state.Open(cfg.StateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	lock, err := state.AcquireLock(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("stop the agent before accepting a control generation: %w", err)
	}
	defer lock.Close()

	ctx := context.Background()
	identity, err := store.Identity(ctx)
	if err != nil {
		return err
	}
	pending, err := store.PendingGeneration(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("applied control generation: %q (revision %d)\n", identity.ControlGeneration, identity.AppliedRevision)
	if pending != "" {
		fmt.Printf("generation refused by the agent: %q\n", pending)
	}
	if identity.ControlGeneration == generation {
		if err := store.ClearAcceptedGeneration(ctx); err != nil {
			return err
		}
		if err := store.SetPendingGeneration(ctx, ""); err != nil {
			return err
		}
		fmt.Printf("generation %q is already applied; nothing to do\n", generation)
		return nil
	}
	if pending != "" && pending != generation {
		fmt.Printf("warning: the panel last offered %q, but %q is accepted\n", pending, generation)
	}
	if err := store.SetAcceptedGeneration(ctx, generation); err != nil {
		return err
	}
	fmt.Printf("accepted control generation %q\n", generation)
	fmt.Println("Start the agent now: it will rebuild every instance from the next snapshot of that generation")
	fmt.Println("and only then advance the applied generation. Accepting means you verified the panel state.")
	return nil
}
