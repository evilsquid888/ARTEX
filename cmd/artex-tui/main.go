package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/Autumn-27/artex/internal/tui"
	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "artex-tui:", err)
		os.Exit(1)
	}
}
func run() error {
	server := flag.String("server", "http://127.0.0.1:8787", "ARTEX server URL")
	tokenFile := flag.String("token-file", "", "file containing an ARTEX session token (otherwise ARTEX_TOKEN)")
	login := flag.Bool("login", false, "prompt for the ARTEX password instead of using a token")
	flag.Parse()
	token := strings.TrimSpace(os.Getenv("ARTEX_TOKEN"))
	if *tokenFile != "" {
		raw, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(raw))
	}
	c, err := tui.NewClient(*server, token)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *login || token == "" {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("interactive terminal required for login; use --token-file or ARTEX_TOKEN")
		}
		fmt.Fprint(os.Stderr, "ARTEX password: ")
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		if err := c.Login(ctx, string(pw)); err != nil {
			return err
		}
	}
	_, err = tea.NewProgram(tui.NewModel(ctx, c), tea.WithContext(ctx)).Run()
	return err
}
