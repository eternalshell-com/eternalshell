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
	"eternalshell/internal/report"
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
  report [--send] [--config path]
            print the status report (what needs attention, disk use, what
            changed since the last report); --send mails it now and saves the
            state for the next comparison. The CPU and alert parts only exist
            in the reports of the running watcher. The state is kept in
            $STATE_DIRECTORY, else ~/.local/state/eternalshell; to count as the
            watcher's report, run --send as its user with
            STATE_DIRECTORY=/var/lib/eternalshell
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
	case os.Args[1] == "report":
		err = runReport(os.Args[2:])
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
	return mailConfig(*configPath)
}

// mailConfig finds, loads and checks the config for a command that sends mail
// and reads the SMTP password from the environment.
func mailConfig(explicit string) (*config.Config, string, error) {
	path, err := config.Find(explicit)
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

// optionalConfig loads the config for a command that can run without one: an
// explicit path must exist, otherwise the usual places are tried and the
// defaults are used if there is no file.
func optionalConfig(explicit string) (*config.Config, error) {
	path, err := config.Find(explicit)
	if err != nil {
		if explicit != "" {
			return nil, err
		}
		def := config.Default()
		fmt.Fprintln(os.Stderr, "No config file found, using the defaults.")
		return &def, nil
	}
	fmt.Fprintln(os.Stderr, "Config:", path)
	return config.Load(path)
}

// runReport prints the status report, or mails it with --send.
func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to the config file")
	sendMail := fs.Bool("send", false, "mail the report now and save the state")
	// Hidden (not in the usage text): print the HTML part instead of the text, to
	// open it in a browser. It sends nothing and saves nothing.
	asHTML := fs.Bool("html", false, "print the HTML version of the report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *asHTML && *sendMail {
		return errors.New("--html only prints the HTML; it cannot be combined with --send")
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	var cfg *config.Config
	var password string
	var err error
	if *sendMail {
		cfg, password, err = mailConfig(*configPath)
	} else {
		cfg, err = optionalConfig(*configPath)
	}
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	col := collector.New()
	defer col.Close()
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	snap, err := report.CollectSnapshot(waitCtx, col, os.Stderr)
	cancel()
	if err != nil {
		return err
	}

	prev, err := report.LoadState()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	fmt.Fprintln(os.Stderr, "Checking the domains...")
	d := report.Build(ctx, report.Input{Snap: snap, Config: cfg, Prev: prev, Now: time.Now()})
	subject, body := report.Render(d)

	if *asHTML {
		html := report.RenderHTML(d)
		if html == "" {
			return errors.New("the HTML version could not be drawn")
		}
		fmt.Print(html)
		return nil
	}
	if !*sendMail {
		fmt.Println("Subject:", subject)
		fmt.Println()
		fmt.Print(body)
		return nil
	}

	s := cfg.SMTP
	fmt.Printf("Sending the report to %s via %s:%d (%s)...\n",
		strings.Join(s.To, ", "), s.Host, s.Port, mail.TransportFor(s.Port))
	if err := mail.Send(ctx, s, password, mail.Message{Subject: subject, Body: body, HTML: report.RenderHTML(d)}); err != nil {
		return errors.New(mail.Explain(err, s))
	}
	fmt.Println("OK: the mail server accepted the report:", subject)

	st := report.SnapshotState(d)
	if err := st.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: the state could not be saved:", err)
		return nil
	}
	fmt.Println("State saved to", report.StatePath())
	if os.Getenv("STATE_DIRECTORY") == "" {
		fmt.Println("Note: the watcher keeps its own state (STATE_DIRECTORY, normally /var/lib/eternalshell); " +
			"this report does not count as its last one.")
	}
	return nil
}
