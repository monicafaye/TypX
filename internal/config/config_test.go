package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitConfigDirPrecedence(t *testing.T) {
	origConfigDir := ConfigDir
	origUserConfigDir := userConfigDir
	t.Cleanup(func() {
		ConfigDir = origConfigDir
		userConfigDir = origUserConfigDir
	})

	t.Run("flag overrides TYPX_CONFIG_HOME", func(t *testing.T) {
		flagDir := t.TempDir()
		envDir := filepath.Join(t.TempDir(), "env-typx")
		t.Setenv("TYPX_CONFIG_HOME", envDir)

		err := InitConfigDir(flagDir)
		require.NoError(t, err)
		assert.Equal(t, flagDir, ConfigDir)
	})

	t.Run("TYPX_CONFIG_HOME overrides UserConfigDir", func(t *testing.T) {
		envDir := filepath.Join(t.TempDir(), "custom-typx")
		t.Setenv("TYPX_CONFIG_HOME", envDir)
		userConfigDir = func() (string, error) {
			return filepath.Join(t.TempDir(), "ignored"), nil
		}

		err := InitConfigDir("")
		require.NoError(t, err)
		assert.Equal(t, envDir, ConfigDir)
		info, statErr := os.Stat(envDir)
		require.NoError(t, statErr)
		assert.True(t, info.IsDir())
	})

	t.Run("UserConfigDir/typx when TYPX_CONFIG_HOME unset", func(t *testing.T) {
		t.Setenv("TYPX_CONFIG_HOME", "")
		base := t.TempDir()
		userConfigDir = func() (string, error) {
			return base, nil
		}

		err := InitConfigDir("")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(base, "typx"), ConfigDir)
	})

	t.Run("fallback to ~/.config/typx when UserConfigDir fails", func(t *testing.T) {
		t.Setenv("TYPX_CONFIG_HOME", "")
		userConfigDir = func() (string, error) {
			return "", errors.New("no user config dir")
		}

		dir, err := defaultConfigDir()
		require.NoError(t, err)
		home, herr := homedir.Dir()
		require.NoError(t, herr)
		assert.Equal(t, filepath.Join(home, ".config", "typx"), dir)
	})

	t.Run("nonexistent flag falls back to defaultConfigDir with error", func(t *testing.T) {
		envDir := filepath.Join(t.TempDir(), "fallback-typx")
		t.Setenv("TYPX_CONFIG_HOME", envDir)
		missing := filepath.Join(t.TempDir(), "does-not-exist")

		err := InitConfigDir(missing)
		assert.Error(t, err)
		assert.Equal(t, envDir, ConfigDir)
	})
}
