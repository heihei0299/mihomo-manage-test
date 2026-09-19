package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/heihei0299/mihomo-manage/internal/cli"
	"github.com/heihei0299/mihomo-manage/internal/manager"
)

var (
	version   = "dev"
	quietMode bool
)

func main() {
	oss := &manager.OSSystem{}
	svcMgr := manager.NewOSServiceManager(oss, oss)
	sched := manager.NewNativeScheduleManager(oss, oss)
	lifecycle := manager.NewLifecycleManager(oss, oss, oss, svcMgr, sched)
	cfg := manager.NewConfigManager(oss, oss, manager.NewConfigValidator(oss), func(ctx context.Context) error {
		return svcMgr.Reload(ctx, manager.ServiceName)
	}, manager.WithConfigUpdateLock(manager.NewFileConfigUpdateLock()))
	ctrl := manager.NewServiceControl(oss, oss, svcMgr, cfg.ValidateConfig)

	var args []string
	showHelp := false
	showVersion := false

	raw := os.Args[1:]
	for i := 0; i < len(raw); i++ {
		a := raw[i]
		switch a {
		case "--quiet", "-q":
			quietMode = true
		case "--help", "-h":
			showHelp = true
		case "--version":
			showVersion = true
		case "-v":
			showVersion = true
		case "-s":
			if i+1 >= len(raw) {
				fmt.Fprintln(os.Stderr, "usage: mihomo-manager -s <url>")
				os.Exit(1)
			}
			i++
			args = append(args, "subscription", "set", raw[i])
		case "-u":
			args = append(args, "subscription", "update")
		case "-i":
			args = append(args, "install")
		case "-c":
			args = append(args, "config", "preview")
		case "-t":
			if i+1 < len(raw) && raw[i+1] == "--off" {
				i++
				args = append(args, "subscription", "schedule", "--off")
			} else if i+2 < len(raw) && raw[i+1] == "--interval" {
				i += 2
				args = append(args, "subscription", "schedule", "--interval", raw[i])
			} else {
				args = append(args, "subscription", "schedule")
			}
		default:
			args = append(args, a)
		}
	}

	if showVersion {
		fmt.Printf("mihomo-manager %s\n", version)
		return
	}
	if showHelp {
		printUsage()
		return
	}

	stdout := io.Writer(os.Stdout)
	if quietMode {
		stdout = io.Discard
	}
	h := cli.New(ctrl, lifecycle, cfg, sched, stdout, os.Stderr)

	if len(args) == 0 {
		if tryElevate(nil) {
			return
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := startTUI(ctx, ctrl, lifecycle, cfg, sched); err != nil {
			log.Fatal(err)
		}
		return
	}

	switch args[0] {
	case "i":
		args[0] = "install"
	case "ui":
		args[0] = "uninstall"
	case "ug":
		args[0] = "upgrade"
	case "v":
		args[0] = "versions"
	}

	if tryElevate(args) {
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var exitCode int

	switch args[0] {
	case "status":
		exitCode = h.Status(ctx)
	case "install":
		ver := "latest"
		autoStart := true
		fromPath := ""
		for i := 0; i < len(args[1:]); i++ {
			a := args[1:][i]
			if a == "--no-autostart" {
				autoStart = false
			} else if a == "--from" {
				if i+1 >= len(args[1:]) {
					fmt.Fprintln(os.Stderr, "usage: mihomo-manager install --from <path>")
					os.Exit(1)
				}
				i++
				fromPath = args[1:][i]
			} else if !strings.HasPrefix(a, "-") {
				ver = a
			}
		}
		if fromPath != "" {
			exitCode = h.InstallFromLocal(ctx, fromPath, autoStart)
		} else {
			exitCode = h.Install(ctx, ver, autoStart)
		}
	case "autostart":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: mihomo-manager autostart on|off")
			os.Exit(1)
		}
		switch args[1] {
		case "on":
			exitCode = h.AutoStart(ctx, true)
		case "off":
			exitCode = h.AutoStart(ctx, false)
		default:
			fmt.Fprintf(os.Stderr, "usage: mihomo-manager autostart on|off\n")
			exitCode = 1
		}
	case "config":
		exitCode = handleConfigCommand(h, cfg, ctx, args[1:])
	case "subscription":
		exitCode = handleSubscription(h, ctx, args[1:])
	case "start":
		exitCode = h.Start(ctx)
	case "stop":
		exitCode = h.Stop(ctx)
	case "restart":
		exitCode = h.Restart(ctx)
	case "reload":
		exitCode = h.Reload(ctx)
	case "uninstall":
		keepBackup := false
		for _, a := range args[1:] {
			if a == "--keep-backup" {
				keepBackup = true
			}
		}
		exitCode = h.Uninstall(ctx, keepBackup)
	case "upgrade":
		ver := "latest"
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			ver = args[1]
		}
		exitCode = h.Upgrade(ctx, ver)
	case "logs":
		cliLogs(args[1:])
		return
	case "versions":
		exitCode = h.Versions(ctx)
	case "template":
		exitCode = handleLegacyTemplate()
	case "rules":
		fmt.Fprintln(os.Stderr, "error: 'rules' is now a config subcommand. Use 'mihomo-manager config rules edit' instead.")
		exitCode = 1
	default:
		exitCode = 1
		printUsage()
	}

	stop()
	os.Exit(exitCode)
}

func deprecatedError(msg string) int {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	return 1
}

func handleConfigCommand(h *cli.Handler, cfg manager.ConfigManager, ctx context.Context, args []string) int {
	if len(args) == 0 {
		printUsage()
		return 1
	}
	switch args[0] {
	case "preview":
		return h.PreviewConfig(ctx)
	case "adopt":
		force := false
		for _, a := range args[1:] {
			if a == "--force" {
				force = true
			}
		}
		return h.AdoptConfig(ctx, force)
	case "override":
		return cliEditFile(cfg, manager.OverrideFilePath, args[1:])
	case "template":
		return deprecatedError("'config template' is deprecated. Use 'mihomo-manager config override edit' instead.")
	case "rules":
		fmt.Fprintln(os.Stderr, "warning: 'config rules' is deprecated. Add routing rules to the 'rules:' field in override.yaml instead.")
		return cliEditFile(cfg, manager.RoutingRulesPath, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown config subcommand: %s\n", args[0])
		return 1
	}
}

func handleLegacyTemplate() int {
	return deprecatedError("'template' is deprecated. Use 'mihomo-manager config override edit' instead.")
}

func handleSubscription(h *cli.Handler, ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mihomo-manager subscription <set|update|schedule>")
		return 1
	}

	switch args[0] {
	case "set":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: mihomo-manager subscription set <url-or-data>")
			return 1
		}
		return h.SetSubscription(ctx, strings.Join(args[1:], " "))
	case "update":
		return h.UpdateConfig(ctx)
	case "schedule":
		return handleSchedule(h, ctx, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subscription subcommand: %s\n", args[0])
		return 1
	}
}

func handleSchedule(h *cli.Handler, ctx context.Context, args []string) int {
	if len(args) == 0 {
		return h.ScheduleStatus(ctx)
	}
	switch args[0] {
	case "--interval":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: mihomo-manager subscription schedule --interval <duration>")
			return 1
		}
		d, err := time.ParseDuration(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid duration: %v\n", err)
			return 1
		}
		return h.SetSchedule(ctx, d)
	case "--off":
		return h.StopSchedule(ctx)
	default:
		fmt.Fprintf(os.Stderr, "unknown schedule option: %s\n", args[0])
		return 1
	}
}

func cliLogs(args []string) {
	follow := false
	tail := 50
	for _, a := range args {
		if a == "--follow" || a == "-f" {
			follow = true
		}
		if strings.HasPrefix(a, "--tail=") {
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--tail="))
			if err == nil {
				tail = n
			}
		}
	}
	journalArgs := []string{"-u", "mihomo", "-n", fmt.Sprintf("%d", tail)}
	if follow {
		journalArgs = append(journalArgs, "--follow")
	}
	cmd := exec.Command("journalctl", journalArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "logs failed: %v\n", err)
		os.Exit(1)
	}
}

func cliEditFile(cfg manager.ConfigManager, path string, args []string) int {
	if len(args) != 1 || args[0] != "edit" {
		fmt.Fprintf(os.Stderr, "usage: mihomo-manager %s edit\n", path)
		return 1
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "editor failed: %v\n", err)
		return 1
	}
	if err := cfg.UpdateConfig(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "config update failed: %v\n", err)
		return 1
	}
	if !quietMode {
		fmt.Println("config updated")
	}
	return 0
}

func printUsage() {
	fmt.Println(usageText())
}

func usageText() string {
	return `Usage: mihomo-manager [command]

Flags:
  -c              Preview generated config
  -h, --help      Show this help
  -i              Install mihomo (alias: install)
  -q, --quiet     Suppress non-error output
  -s <url>        Set subscription source
  -t [opt]        View/configure auto-refresh (--interval|--off)
  -u              Refresh and apply subscription
  -v, --version   Show version

Basic operations:
  status                  Show mihomo status
  start                   Start mihomo
  stop                    Stop mihomo
  restart                 Restart mihomo
  reload                  Reload config
  logs [--tail=N] [--follow]  View mihomo logs

Subscription:
  subscription set <s>        Set subscription source
  subscription update         Refresh and apply subscription
  subscription schedule [opt] View/configure auto-refresh (--interval|--off)

Config:
  config preview              Preview generated config
  config adopt [--force]      Adopt manual config.yaml changes into override
  config override edit        Edit override file ($EDITOR)

Environments:
  MIHOMO_DOWNLOAD_PROXY=<url>   Proxy for GitHub core download (e.g. http://127.0.0.1:10809)
  MIHOMO_RELEASE_URL=<tmpl>     Download URL template with {os} {arch} {version} placeholders
  MIHOMO_RELEASE_CHECKSUM_URL=<tmpl>  Checksum URL template with {version} {asset} placeholders

Lifecycle:
  install/i [ver] [--no-autostart] [--from <path>]   Install mihomo (default: latest; --from for local .gz/binary)
  upgrade/ug [ver]                    Upgrade mihomo (default: latest)
  uninstall/ui [--keep-backup]        Remove mihomo
  autostart on|off                    Toggle auto-start on boot
  versions/v                          List available versions

Run without arguments to start the TUI.`
}

func needsElevation(args []string) bool {
	if len(args) == 0 {
		return true // TUI
	}
	switch args[0] {
	case "status", "versions", "v", "logs":
		return false
	case "config":
		if len(args) > 1 && args[1] == "preview" {
			return false
		}
		return true
	default:
		return true
	}
}

func tryElevate(args []string) bool {
	if os.Geteuid() == 0 {
		return false
	}
	if !needsElevation(args) {
		return false
	}
	sudoPath, err := exec.LookPath("sudo")
	if err != nil {
		return false
	}
	cmd := exec.Command(sudoPath, os.Args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
	return true
}
