package config

import (
	"errors"
	"os"
	"path/filepath"

	homedir "github.com/mitchellh/go-homedir"
)

var ConfigDir string

// userConfigDir is a package-internal hook for testing fallback resolution.
var userConfigDir = os.UserConfigDir

// defaultConfigDir resolves the default TypX configuration directory in order:
// TYPX_CONFIG_HOME > os.UserConfigDir()/typx > ~/.config/typx.
func defaultConfigDir() (string, error) {
	if d := os.Getenv("TYPX_CONFIG_HOME"); d != "" {
		return d, nil
	}
	if d, err := userConfigDir(); err == nil && d != "" {
		return filepath.Join(d, "typx"), nil
	}
	home, err := homedir.Dir()
	if err != nil {
		return "", errors.New("Error finding your home directory\nCan't load config files: " + err.Error())
	}
	return filepath.Join(home, ".config", "typx"), nil
}

// InitConfigDir finds the configuration directory for TypX.
// Resolution order: -config-dir flag > TYPX_CONFIG_HOME > os.UserConfigDir()/typx > ~/.config/typx.
// If the directory does not exist, it creates one.
func InitConfigDir(flagConfigDir string) error {
	var e error

	typxHome, err := defaultConfigDir()
	if err != nil {
		return err
	}
	ConfigDir = typxHome

	if len(flagConfigDir) > 0 {
		if _, err := os.Stat(flagConfigDir); os.IsNotExist(err) {
			e = errors.New("Error: " + flagConfigDir + " does not exist. Defaulting to " + ConfigDir + ".")
		} else {
			ConfigDir = flagConfigDir
			return nil
		}
	}

	// Create TypX config home directory if it does not exist.
	// This creates parent directories and does nothing if it already exists.
	err = os.MkdirAll(ConfigDir, os.ModePerm)
	if err != nil {
		return errors.New("Error creating configuration directory: " + err.Error())
	}

	return e
}
