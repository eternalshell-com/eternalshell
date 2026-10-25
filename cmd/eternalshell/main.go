// Command eternalshell shows the state of the server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"eternalshell/internal/collector"
	"eternalshell/internal/config"
	"eternalshell/internal/format"
	"eternalshell/internal/mail"
	"eternalshell/internal/tui"
	"eternalshell/internal/watch"
)

const usage = `Usage: eternalshell [command]

Without a command, eternalshell opens the live terminal UI.

Commands:
  status    print CPU, load, memory, disks and the top 5 processes
  watch [--config path]
            run in the foreground and mail CPU alerts (one process or the whole
            server above the thresholds in alerts.cpu); sampled every 10 s
  test-mail [--config path]
            send one test mail with the smtp settings from the config file
            (default: /etc/eternalshell/config.yaml, then ./config.yaml)
`

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = runTUI()
	case len(os.Args) == 2 && os.Args[1] == "status":
		err = runStatus()
	case os.Args[1] == "test-mail":
		err = runTestMail(os.Args[2:])
	case os.Args[1] == "watch":
		err = runWatch(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runTUI opens the full-screen UI and blocks until the user quits.
func runTUI() error {
	c := collector.New()
	defer c.Close()
	c.EnableDiskUsage() // fills the DISK column in the background
	_, err := tea.NewProgram(tui.New(c), tea.WithAltScreen()).Run()
	return err
}

func runStatus() error {
	c := collector.New()
	defer c.Close()
	// CPU values are changes between two samples, so take one, wait, take another.
	if _, err := c.Collect(); err != nil {
		return err
	}
	time.Sleep(time.Second)
	snap, err := c.Collect()
	if err != nil {
		return err
	}

	fmt.Printf("CPU     %.1f %% total\n", snap.CPUPercent)
	fmt.Printf("LOAD    %.2f %.2f %.2f\n", snap.Load1, snap.Load5, snap.Load15)
	fmt.Printf("MEM     %.0f %% (%s of %s)\n", snap.MemPercent, format.Bytes(snap.MemUsed), format.Bytes(snap.MemTotal))
	for _, m := range snap.Mounts {
		fmt.Printf("DISK    %s %.0f %% (%s of %s)\n", m.Path, m.UsedPercent, format.Bytes(m.Used), format.Bytes(m.Total))
	}

	fmt.Println("\nPROCESSES (top 5 by CPU, 100 % = one core)")
	fmt.Printf("%-8s %-20s %7s %8s  %s\n", "PID", "NAME", "CPU", "MEM", "ORIGIN")
	for _, p := range collector.Top(collector.UserProcesses(snap.Processes), 5) {
		fmt.Printf("%-8d %-20.20s %7s %8s  %s\n", p.PID, p.Name, format.Percent(p.CPU), format.Bytes(p.RSS), p.Origin)
	}
	return nil
}

// loadMailConfig parses "--config path" for a command that sends mail, then
// finds, loads and checks the config and reads the SMTP password from the
// environment. It prints which config file is used and warns about its mode.
func loadMailConfig(command string, args []string) (*config.Config, string, error) {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := fs.String("config", "", "path to the config file")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	if fs.NArg() > 0 {
		return nil, "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	path, err := config.Find(*configPath)
	if err != nil {
		return nil, "", err
	}
	fmt.Println("Config:", path)
	if w := config.PermissionWarning(path); w != "" {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}

	cfg, err := config.Load(path)
	if err != nil {
		return nil, "", err
	}
	if err := cfg.ValidateSMTP(); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	password, err := cfg.SMTP.Password()
	if err != nil {
		return nil, "", err
	}
	return cfg, password, nil
}

// runTestMail sends one mail to check the smtp section of the config.
func runTestMail(args []string) error {
	cfg, password, err := loadMailConfig("test-mail", args)
	if err != nil {
		return err
	}

	s := cfg.SMTP
	fmt.Printf("Sending test mail to %s via %s:%d (%s)...\n",
		strings.Join(s.To, ", "), s.Host, s.Port, mail.TransportFor(s.Port))

	// Ctrl+C cancels the connection attempt.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := mail.Send(ctx, s, password, mail.TestMessage(cfg.Server.Name, time.Now())); err != nil {
		return errors.New(mail.Explain(err, s))
	}
	fmt.Println("OK: the mail server accepted the message. Check the inbox (and the spam folder).")
	return nil
}

// runWatch runs the watcher in the foreground until Ctrl+C or SIGTERM.
// It refuses to start without working mail settings: a watcher that cannot
// mail would only look like it protects the server.
func runWatch(args []string) error {
	cfg, password, err := loadMailConfig("watch", args)
	if err != nil {
		return err
	}

	c := collector.New()
	defer c.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return watch.Run(ctx, watch.Options{Config: cfg, Password: password, Collector: c, Out: os.Stdout})
}
