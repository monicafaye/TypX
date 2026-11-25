package buffer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/util"
)

const BackupMsg = `A backup was detected for:

%s

This likely means that micro crashed while editing this file,
or another instance of micro is currently editing this file,
or an error occurred while saving this file so it may be corrupted.

The backup was created on %s and its path is:

%s

* 'recover' will apply the backup as unsaved changes to the current buffer.
  When the buffer is closed, the backup will be removed.
* 'ignore' will ignore the backup, discarding its changes. The backup file
  will be removed.
* 'abort' will abort the open operation, and instead open an empty buffer.

Options: [r]ecover, [i]gnore, [a]bort: `

const backupSeconds = 8

type backupRequestType int

const (
	backupCreate = iota
	backupRemove
)

type backupEntry struct {
	backupDir string
	absPath   string
}

type backupRequest struct {
	buf       *SharedBuffer
	reqType   backupRequestType
	backupDir string
	absPath   string
	enabled   bool
	keep      bool
}

var requestedBackups map[*SharedBuffer]backupEntry

func init() {
	requestedBackups = make(map[*SharedBuffer]backupEntry)
}

func (b *SharedBuffer) RequestBackup() {
	enabled := b.Settings["backup"].(bool) && b.Path != "" && b.Type == BTDefault
	var backupDir, absPath string
	if enabled {
		backupDir = b.backupDir()
		absPath = b.AbsPath
	}
	backupRequestChan <- backupRequest{
		buf:       b,
		reqType:   backupCreate,
		backupDir: backupDir,
		absPath:   absPath,
		enabled:   enabled,
	}
}

func (b *SharedBuffer) CancelBackup() {
	keep := b.keepBackup() || b.Path == "" || b.Type != BTDefault
	var backupDir, absPath string
	if !keep {
		backupDir = b.backupDir()
		absPath = b.AbsPath
	}
	backupRequestChan <- backupRequest{
		buf:       b,
		reqType:   backupRemove,
		backupDir: backupDir,
		absPath:   absPath,
		keep:      keep,
	}
}

func handleBackupRequest(br backupRequest) {
	switch br.reqType {
	case backupCreate:
		// schedule periodic backup
		if br.enabled {
			requestedBackups[br.buf] = backupEntry{
				backupDir: br.backupDir,
				absPath:   br.absPath,
			}
		}
	case backupRemove:
		if !br.keep {
			br.buf.removeBackupInDir(br.backupDir, br.absPath)
		}
		delete(requestedBackups, br.buf)
	}
}

func periodicBackup() {
	for buf, entry := range requestedBackups {
		_, _, err := buf.writeBackupInDir(entry.backupDir, entry.absPath)
		if err == nil {
			delete(requestedBackups, buf)
		}
	}
}

func (b *SharedBuffer) backupDir() string {
	backupdir, err := util.ReplaceHome(b.Settings["backupdir"].(string))
	if backupdir == "" || err != nil {
		backupdir = filepath.Join(config.ConfigDir, "backups")
	}
	return backupdir
}

func (b *SharedBuffer) keepBackup() bool {
	return b.forceKeepBackup || b.Settings["permbackup"].(bool)
}

func (b *SharedBuffer) writeBackup(path string) (string, string, error) {
	return b.writeBackupInDir(b.backupDir(), path)
}

func (b *SharedBuffer) writeBackupInDir(backupdir, path string) (string, string, error) {
	if _, err := os.Stat(backupdir); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
		if err = os.Mkdir(backupdir, os.ModePerm); err != nil {
			return "", "", err
		}
	}

	name, resolveName := util.DetermineEscapePath(backupdir, path)
	tmp := name + util.BackupSuffix

	_, err := b.overwriteFile(tmp)
	if err != nil {
		os.Remove(tmp)
		return name, resolveName, err
	}
	err = os.Rename(tmp, name)
	if err != nil {
		os.Remove(tmp)
		return name, resolveName, err
	}

	if resolveName != "" {
		err = util.SafeWrite(resolveName, []byte(path), true)
		if err != nil {
			return name, resolveName, err
		}
	}

	return name, resolveName, nil
}

func (b *SharedBuffer) removeBackup(path string, resolveName string) {
	os.Remove(path)
	if resolveName != "" {
		os.Remove(resolveName)
	}
}

func (b *SharedBuffer) removeBackupInDir(backupdir, absPath string) {
	f, resolveName := util.DetermineEscapePath(backupdir, absPath)
	b.removeBackup(f, resolveName)
}

// Backup saves the buffer to the backups directory
func (b *SharedBuffer) Backup() error {
	if !b.Settings["backup"].(bool) || b.Path == "" || b.Type != BTDefault {
		return nil
	}

	_, _, err := b.writeBackup(b.AbsPath)
	return err
}

// RemoveBackup removes any backup file associated with this buffer
func (b *SharedBuffer) RemoveBackup() {
	if b.keepBackup() || b.Path == "" || b.Type != BTDefault {
		return
	}
	b.removeBackupInDir(b.backupDir(), b.AbsPath)
}

// ApplyBackup applies the corresponding backup file to this buffer (if one exists)
// Returns true if a backup was applied
func (b *SharedBuffer) ApplyBackup(fsize int64) (bool, bool) {
	if b.Settings["backup"].(bool) && !b.Settings["permbackup"].(bool) && len(b.Path) > 0 && b.Type == BTDefault {
		backupfile, resolveName := util.DetermineEscapePath(b.backupDir(), b.AbsPath)
		if info, err := os.Stat(backupfile); err == nil {
			backup, err := os.Open(backupfile)
			if err == nil {
				defer backup.Close()
				t := info.ModTime()
				msg := fmt.Sprintf(BackupMsg, b.Path, t.Format("Mon Jan _2 at 15:04, 2006"), backupfile)
				choice := screen.TermPrompt(msg, []string{"r", "i", "a", "recover", "ignore", "abort"}, true)

				if choice%3 == 0 {
					// recover
					b.LineArray = NewLineArray(uint64(fsize), FFAuto, backup)
					b.setModified()
					return true, true
				} else if choice%3 == 1 {
					// delete
					b.removeBackup(backupfile, resolveName)
				} else if choice%3 == 2 {
					return false, false
				}
			}
		}
	}

	return false, true
}
