package main

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/contiamo/fettle/internal/project"
	"github.com/contiamo/fettle/internal/ui/server"
	"github.com/pkg/browser"
	"github.com/spf13/cobra"
)

var uiFlags struct {
	addr string
	open bool
}

var uiCmd = &cobra.Command{
	Use:     "ui",
	Short:   "Serve a local web UI for browsing runs",
	GroupID: groupProject,
	Long: `ui starts a local HTTP server that lets you browse the runs in
the current fettle project. The default landing page is a run picker;
clicking a run opens its findings, groups, reviews, and outcomes
(per-run views are still in development).

The server binds to localhost only and writes to disk on the same
path the CLI does — there is no auth, and runs added or completed
while the server is up appear on the next page load.`,
	RunE: runUI,
}

func init() {
	uiCmd.Flags().StringVar(&uiFlags.addr, "addr", "127.0.0.1:7878", "listen address")
	uiCmd.Flags().BoolVar(&uiFlags.open, "open", true, "open the browser on start")
	rootCmd.AddCommand(uiCmd)
}

func runUI(cmd *cobra.Command, args []string) error {
	dir, err := projectDir()
	if err != nil {
		return err
	}
	cfg, err := project.Load(dir)
	if err != nil {
		return fmt.Errorf("not a fettle project at %s: %w", dir, err)
	}

	h := server.New(dir, cfg)

	// Bind first so the URL we print and the URL the browser opens
	// are guaranteed live. http.Serve below accepts on this socket;
	// no race between OpenURL and ListenAndServe.
	ln, err := net.Listen("tcp", uiFlags.addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", uiFlags.addr, err)
	}
	url := "http://" + ln.Addr().String()
	fmt.Fprintf(os.Stderr, "fettle ui: %s  (project: %s)\n", url, dir)

	if uiFlags.open {
		if err := browser.OpenURL(url); err != nil {
			fmt.Fprintf(os.Stderr, "fettle ui: open browser: %v\n", err)
		}
	}
	return http.Serve(ln, h)
}
