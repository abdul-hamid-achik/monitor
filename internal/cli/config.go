package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/config"
)

var configKeys = []string{
	"update_interval",
	"temperature_unit",
	"show_system_processes",
	"max_processes",
	"mouse_enabled",
	"cpu_alert_threshold",
	"memory_alert_threshold",
}

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config <subcommand>",
		Aliases: []string{"settings"},
		Short:   "Inspect and update Monitor settings",
		Long: `Inspect and update the settings shared by the CLI and Studio.

Changes are validated and written atomically to ~/.config/monitor/config.json.`,
	}
	cmd.AddCommand(
		newConfigShowCmd(),
		newConfigGetCmd(),
		newConfigSetCmd(),
		newConfigPathCmd(),
		newConfigResetCmd(),
	)
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective settings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			settings, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if JSONOutput(cmd) {
				return WriteJSON(settings)
			}
			return printSettings(cmd, *settings)
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newConfigGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Print one effective setting",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			settings, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			key := normalizeConfigKey(args[0])
			value, err := configValue(*settings, key)
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				return WriteJSON(map[string]any{"key": key, "value": value})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), value)
			return err
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newConfigSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Validate and persist one setting",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			settings, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			key := normalizeConfigKey(args[0])
			if err := setConfigValue(settings, key, args[1]); err != nil {
				return err
			}
			if err := settings.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			value, _ := configValue(*settings, key)
			path, _ := config.Path()
			if JSONOutput(cmd) {
				return WriteJSON(map[string]any{
					"updated":  true,
					"key":      key,
					"value":    value,
					"path":     path,
					"settings": settings,
				})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s = %v\n", key, value)
			return err
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newConfigPathCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Print the settings file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return fmt.Errorf("config path: %w", err)
			}
			if JSONOutput(cmd) {
				return WriteJSON(map[string]string{"path": path})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newConfigResetCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Restore every setting to its default",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				if JSONOutput(cmd) {
					return WriteJSON(map[string]any{
						"reset":   false,
						"refused": true,
						"reason":  "pass --yes to confirm resetting all settings",
					})
				}
				return fmt.Errorf("refusing to reset all settings without --yes")
			}
			settings := config.Default()
			if err := settings.Save(); err != nil {
				return fmt.Errorf("reset config: %w", err)
			}
			path, _ := config.Path()
			if JSONOutput(cmd) {
				return WriteJSON(map[string]any{
					"reset":    true,
					"path":     path,
					"settings": settings,
				})
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Reset settings to defaults in %s\n", path)
			return err
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm resetting all settings")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func printSettings(cmd *cobra.Command, settings config.Settings) error {
	text := fmt.Sprintf(`update_interval       %s
temperature_unit      %s
show_system_processes %t
max_processes         %d
mouse_enabled         %t
cpu_alert_threshold   %g
memory_alert_threshold %g
`, settings.UpdateInterval, settings.TemperatureUnit, settings.ShowSystemProcesses,
		settings.MaxProcesses, settings.MouseEnabled, settings.CPUAlertThreshold,
		settings.MemoryAlertThreshold)
	_, err := io.WriteString(cmd.OutOrStdout(), text)
	return err
}

func normalizeConfigKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
}

func configValue(settings config.Settings, key string) (any, error) {
	switch key {
	case "update_interval":
		return settings.UpdateInterval.String(), nil
	case "temperature_unit":
		return settings.TemperatureUnit, nil
	case "show_system_processes":
		return settings.ShowSystemProcesses, nil
	case "max_processes":
		return settings.MaxProcesses, nil
	case "mouse_enabled":
		return settings.MouseEnabled, nil
	case "cpu_alert_threshold":
		return settings.CPUAlertThreshold, nil
	case "memory_alert_threshold":
		return settings.MemoryAlertThreshold, nil
	default:
		return nil, fmt.Errorf("unknown config key %q (valid: %s)", key, strings.Join(configKeys, ", "))
	}
}

func setConfigValue(settings *config.Settings, key, raw string) error {
	if settings == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	var err error
	switch key {
	case "update_interval":
		settings.UpdateInterval, err = time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid update_interval %q: use a duration such as 500ms or 2s", raw)
		}
	case "temperature_unit":
		settings.TemperatureUnit = strings.ToUpper(strings.TrimSpace(raw))
	case "show_system_processes":
		settings.ShowSystemProcesses, err = strconv.ParseBool(raw)
	case "max_processes":
		settings.MaxProcesses, err = strconv.Atoi(raw)
	case "mouse_enabled":
		settings.MouseEnabled, err = strconv.ParseBool(raw)
	case "cpu_alert_threshold":
		settings.CPUAlertThreshold, err = strconv.ParseFloat(raw, 64)
	case "memory_alert_threshold":
		settings.MemoryAlertThreshold, err = strconv.ParseFloat(raw, 64)
	default:
		_, err = configValue(*settings, key)
		return err
	}
	if err != nil {
		return fmt.Errorf("invalid value %q for %s: %w", raw, key, err)
	}
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("invalid value %q for %s: %w", raw, key, err)
	}
	return nil
}
