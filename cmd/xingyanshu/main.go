package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"xingyanshu-backend/internal/app"
	"xingyanshu-backend/internal/config"
	"xingyanshu-backend/internal/platform/logx"
)

func main() {
	command, configPath := parseArgs()
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	cleanupLogs, err := logx.Init(logx.Settings{
		Directory: cfg.Logging.Directory, RetentionDays: cfg.Logging.RetentionDays,
		AlsoStdout: cfg.Logging.AlsoStdout, Component: command,
	})
	if err != nil {
		log.Fatalf("initialize logs: %v", err)
	}
	defer cleanupLogs()

	switch command {
	case "server":
		err = app.RunServer(cfg, configPath)
	case "worker":
		err = app.RunWorker(cfg)
	case "migrate":
		err = app.RunMigrate(cfg)
	case "bootstrap":
		err = app.RunBootstrap(cfg)
	default:
		log.Fatalf("unsupported command: %s", command)
	}
	if err != nil {
		logx.Errorf("command=%q stage=command.run error_type=%T error=%q", command, err, logx.Redact(err.Error()))
		log.Fatalf("%s failed: %v", command, err)
	}
}

func parseArgs() (string, string) {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: xingyanshu <server|worker|migrate|bootstrap> [-config config/test.yaml]")
		os.Exit(2)
	}

	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	configPath := flags.String("config", "config/test.yaml", "configuration file path")
	if err := flags.Parse(os.Args[2:]); err != nil {
		log.Fatal(err)
	}
	return os.Args[1], *configPath
}
