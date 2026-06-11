package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/influxdata/telegraf/plugins/common/shim"

	// Register the axis_aoa input plugin.
	_ "github.com/Mo3he/telegraf-execd-axis-aoa/plugins/inputs/axis_aoa"
)

var (
	pollInterval         = flag.Duration("poll_interval", 1*time.Minute, "how often to send metrics")
	pollIntervalDisabled = flag.Bool("poll_interval_disabled", false, "disable polling and let Telegraf drive collection via STDIN")
	configFile           = flag.String("config", "", "path to the plugin config file")
)

func main() {
	flag.Parse()

	if *pollIntervalDisabled {
		*pollInterval = shim.PollIntervalDisabled
	}

	s := shim.New()

	if err := s.LoadConfig(configFile); err != nil {
		fmt.Fprintf(os.Stderr, "loading config failed: %s\n", err)
		os.Exit(1)
	}

	if err := s.Run(*pollInterval); err != nil {
		fmt.Fprintf(os.Stderr, "running plugin failed: %s\n", err)
		os.Exit(1)
	}
}
