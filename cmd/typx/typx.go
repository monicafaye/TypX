package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"syscall"
	"time"

	"github.com/go-errors/errors"
	isatty "github.com/mattn/go-isatty"
	"github.com/micro-editor/tcell/v2"
	lua "github.com/yuin/gopher-lua"
	"github.com/monicafaye/TypX/internal/action"
	"github.com/monicafaye/TypX/internal/buffer"
	"github.com/monicafaye/TypX/internal/clipboard"
	"github.com/monicafaye/TypX/internal/config"
	"github.com/monicafaye/TypX/internal/lsp"
	"github.com/monicafaye/TypX/internal/screen"
	"github.com/monicafaye/TypX/internal/shell"
	"github.com/monicafaye/TypX/internal/typst/features"
	"github.com/monicafaye/TypX/internal/typst/preview"
	"github.com/monicafaye/TypX/internal/util"
)

var (
	// Command line flags
	flagVersion   = flag.Bool("version", false, "Show the version number and information")
	flagConfigDir = flag.String("config-dir", "", "Specify a custom location for the configuration directory")
	flagOptions   = flag.Bool("options", false, "Show all option help")
	flagDebug     = flag.Bool("debug", false, "Enable debug mode (prints debug info to ./log.txt)")
	flagProfile   = flag.Bool("profile", false, "Enable CPU profiling (writes profile info to ./typx.prof)")
	flagPlugin    = flag.String("plugin", "", "Plugin command")
	flagClean     = flag.Bool("clean", false, "Clean configuration directory")
	flagCheck     = flag.Bool("check", false, "Verify TypX environment and exit")
	flagNoLSP     = flag.Bool("no-lsp", false, "Launch without starting tinymist LSP")
	flagPreview   = flag.Bool("preview", false, "Start companion live preview on the opened .typ file")
	flagNew       = flag.String("new", "", "Create a new .typ document or project from a template")
	optionFlags   map[string]*string

	sighup chan os.Signal
)

func InitFlags() {
	flag.Usage = func() {
		fmt.Println("Usage: typx [OPTION]... [FILE]... [+LINE[:COL]] [+/REGEX]")
		fmt.Println("       typx [OPTION]... [FILE[:LINE[:COL]]]...  (only if the `parsecursor` option is enabled)")
		fmt.Println("-clean")
		fmt.Println("    \tClean the configuration directory and exit")
		fmt.Println("-config-dir dir")
		fmt.Println("    \tSpecify a custom location for the configuration directory")
		fmt.Println("FILE:LINE[:COL] (only if the `parsecursor` option is enabled)")
		fmt.Println("FILE +LINE[:COL]")
		fmt.Println("    \tSpecify a line and column to start the cursor at when opening a buffer")
		fmt.Println("+/REGEX")
		fmt.Println("    \tSpecify a regex to search for when opening a buffer")
		fmt.Println("-options")
		fmt.Println("    \tShow all options help and exit")
		fmt.Println("-debug")
		fmt.Println("    \tEnable debug mode (enables logging to ./log.txt)")
		fmt.Println("-profile")
		fmt.Println("    \tEnable CPU profiling (writes profile info to ./typx.prof")
		fmt.Println("    \tso it can be analyzed later with \"go tool pprof typx.prof\")")
		fmt.Println("-version")
		fmt.Println("    \tShow the version number and information and exit")
		fmt.Println("-check")
		fmt.Println("    \tVerify TypX environment (config dir, syntax, Unicode, tinymist) and exit")
		fmt.Println("-no-lsp")
		fmt.Println("    \tLaunch without starting the tinymist LSP server")
		fmt.Println("-preview")
		fmt.Println("    \tImmediately start live preview (inline Sixel or browser) on the opened .typ file")
		fmt.Println("-new template")
		fmt.Println("    \tCreate a new .typ file or project from a template (use `-new list` to list)")

		fmt.Print("\nTypX's plugins can be managed at the command line with the following commands.\n")
		fmt.Println("-plugin install [PLUGIN]...")
		fmt.Println("    \tInstall plugin(s)")
		fmt.Println("-plugin remove [PLUGIN]...")
		fmt.Println("    \tRemove plugin(s)")
		fmt.Println("-plugin update [PLUGIN]...")
		fmt.Println("    \tUpdate plugin(s) (if no argument is given, updates all plugins)")
		fmt.Println("-plugin search [PLUGIN]...")
		fmt.Println("    \tSearch for a plugin")
		fmt.Println("-plugin list")
		fmt.Println("    \tList installed plugins")
		fmt.Println("-plugin available")
		fmt.Println("    \tList available plugins")

		fmt.Print("\nTypX's options can also be set via command line arguments for quick\nadjustments. For real configuration, please use the settings.json\nfile (see 'help options').\n\n")
		fmt.Println("-<option> value")
		fmt.Println("    \tSet `option` to `value` for this session")
		fmt.Println("    \tFor example: `typx -syntax off paper.typ`")
		fmt.Println("\nUse `typx -options` to see the full list of configuration options")
	}

	optionFlags = make(map[string]*string)

	for k, v := range config.DefaultAllSettings() {
		optionFlags[k] = flag.String(k, "", fmt.Sprintf("The %s option. Default value: '%v'.", k, v))
	}

	flag.Parse()

	if *flagVersion {
		// If -version was passed
		fmt.Printf("TypX %s (based on micro %s)\n", util.TypXVersion, util.MicroBaseVersion)
		fmt.Println("Commit hash:", util.CommitHash)
		fmt.Println("Compiled on", util.CompileDate)
		exit(0)
	}

	if *flagOptions {
		// If -options was passed
		var keys []string
		m := config.DefaultAllSettings()
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := m[k]
			fmt.Printf("-%s value\n", k)
			fmt.Printf("    \tDefault value: '%v'\n", v)
		}
		exit(0)
	}

	if util.Debug == "OFF" && *flagDebug {
		util.Debug = "ON"
	}
}

// DoPluginFlags parses and executes any flags that require LoadAllPlugins (-plugin and -clean)
func DoPluginFlags() {
	if *flagClean || *flagPlugin != "" {
		config.LoadAllPlugins()

		if *flagPlugin != "" {
			args := flag.Args()

			config.PluginCommand(os.Stdout, *flagPlugin, args)
		} else if *flagClean {
			CleanConfig()
		}

		exit(0)
	}
}

// LoadInput determines which files should be loaded into buffers
// based on the input stored in flag.Args()
func LoadInput(args []string) []*buffer.Buffer {
	// There are a number of ways micro should start given its input

	// 1. If it is given a files in flag.Args(), it should open those

	// 2. If there is no input file and the input is not a terminal, that means
	// something is being piped in and the stdin should be opened in an
	// empty buffer

	// 3. If there is no input file and the input is a terminal, an empty buffer
	// should be opened

	buffers := make([]*buffer.Buffer, 0, len(args))

	files := make([]string, 0, len(args))

	flagStartPos := buffer.Loc{-1, -1}
	posFlagr := regexp.MustCompile(`^\+(\d+)(?::(\d+))?$`)
	posIndex := -1

	searchText := ""
	searchFlagr := regexp.MustCompile(`^\+\/(.+)$`)
	searchIndex := -1

	for i, a := range args {
		posMatch := posFlagr.FindStringSubmatch(a)
		if len(posMatch) == 3 && posMatch[2] != "" {
			line, err := strconv.Atoi(posMatch[1])
			if err != nil {
				screen.TermMessage(err)
				continue
			}
			col, err := strconv.Atoi(posMatch[2])
			if err != nil {
				screen.TermMessage(err)
				continue
			}
			flagStartPos = buffer.Loc{col - 1, line - 1}
			posIndex = i
		} else if len(posMatch) == 3 && posMatch[2] == "" {
			line, err := strconv.Atoi(posMatch[1])
			if err != nil {
				screen.TermMessage(err)
				continue
			}
			flagStartPos = buffer.Loc{0, line - 1}
			posIndex = i
		} else {
			searchMatch := searchFlagr.FindStringSubmatch(a)
			if len(searchMatch) == 2 {
				searchText = searchMatch[1]
				searchIndex = i
			} else {
				files = append(files, a)
			}
		}
	}

	command := buffer.Command{
		StartCursor:      flagStartPos,
		SearchRegex:      searchText,
		SearchAfterStart: searchIndex > posIndex,
	}

	if len(files) > 0 {
		// Option 1
		// We go through each file and load it
		for i := 0; i < len(files); i++ {
			buf, err := buffer.NewBufferFromFileWithCommand(files[i], buffer.BTDefault, command)
			if err != nil {
				screen.TermMessage(err)
				continue
			}
			// If the file didn't exist, input will be empty, and we'll open an empty buffer
			buffers = append(buffers, buf)
		}
	} else {
		btype := buffer.BTDefault
		if !isatty.IsTerminal(os.Stdout.Fd()) {
			btype = buffer.BTStdout
		}

		if !isatty.IsTerminal(os.Stdin.Fd()) {
			// Option 2
			// The input is not a terminal, so something is being piped in
			// and we should read from stdin
			input, err := io.ReadAll(os.Stdin)
			if err != nil {
				screen.TermMessage("Error reading from stdin: ", err)
				input = []byte{}
			}
			buffers = append(buffers, buffer.NewBufferFromStringWithCommand(string(input), "", btype, command))
		} else {
			// Option 3, just open an empty buffer
			buffers = append(buffers, buffer.NewBufferFromStringWithCommand("", "", btype, command))
		}
	}

	return buffers
}

func checkBackup(name string) error {
	target := filepath.Join(config.ConfigDir, name)
	backup := target + util.BackupSuffix
	if info, err := os.Stat(backup); err == nil {
		input, err := os.ReadFile(backup)
		if err == nil {
			t := info.ModTime()
			msg := fmt.Sprintf(buffer.BackupMsg, target, t.Format("Mon Jan _2 at 15:04, 2006"), backup)
			choice := screen.TermPrompt(msg, []string{"r", "i", "a", "recover", "ignore", "abort"}, true)

			if choice%3 == 0 {
				// recover
				err := os.WriteFile(target, input, util.FileMode)
				if err != nil {
					return err
				}
				return os.Remove(backup)
			} else if choice%3 == 1 {
				// delete
				return os.Remove(backup)
			} else if choice%3 == 2 {
				// abort
				return errors.New("Aborted")
			}
		}
	}
	return nil
}

func exit(rc int) {
	preview.Shutdown()
	lsp.Shutdown()

	for _, b := range buffer.OpenBuffers {
		if !b.Modified() {
			b.Fini()
		}
	}

	if screen.Screen != nil {
		screen.Screen.Fini()
	}

	os.Exit(rc)
}

func main() {
	defer func() {
		if util.Stdout.Len() > 0 {
			fmt.Fprint(os.Stdout, util.Stdout.String())
		}
		exit(0)
	}()

	var err error

	InitFlags()

	if *flagProfile {
		f, err := os.Create("typx.prof")
		if err != nil {
			log.Fatal("error creating CPU profile: ", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("error starting CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}

	InitLog()

	configDirErr := config.InitConfigDir(*flagConfigDir)
	if configDirErr != nil && !*flagCheck {
		screen.TermMessage(configDirErr)
	}

	config.InitRuntimeFiles(true)
	config.InitPlugins()

	if !*flagCheck {
		err = checkBackup("settings.json")
		if err != nil {
			screen.TermMessage(err)
			exit(1)
		}
	}

	settingsErr := config.ReadSettings()
	if settingsErr != nil && !*flagCheck {
		screen.TermMessage(settingsErr)
	}
	err = config.InitGlobalSettings()
	if err != nil && !*flagCheck {
		screen.TermMessage(err)
	}

	// flag options
	for k, v := range optionFlags {
		if *v != "" {
			nativeValue, err := config.GetNativeValue(k, *v)
			if err != nil {
				screen.TermMessage(err)
				continue
			}
			if err = config.OptionIsValid(k, nativeValue); err != nil {
				screen.TermMessage(err)
				continue
			}
			config.GlobalSettings[k] = nativeValue
			config.VolatileSettings[k] = true
		}
	}

	if *flagNoLSP {
		config.GlobalSettings["typst.lspEnabled"] = false
		config.VolatileSettings["typst.lspEnabled"] = true
	}

	if *flagCheck {
		exit(runCheck(os.Stdout, configDirErr, settingsErr))
	}

	DoPluginFlags()

	openArgs := flag.Args()
	if *flagNew != "" {
		var exitNow bool
		var newErr error
		openArgs, exitNow, newErr = handleNewFlag(*flagNew, openArgs, os.Stdout, nil)
		if newErr != nil {
			fmt.Fprintln(os.Stderr, "Error:", newErr)
			exit(1)
		}
		if exitNow {
			exit(0)
		}
	}

	// Probe terminal inline graphics (Sixel) and cell pixel dimensions before
	// tcell takes over stdin/stdout. Safe no-op on non-TTY streams.
	preview.ProbeTerminal()

	err = screen.Init()
	if err != nil {
		fmt.Println(err)
		fmt.Println("Fatal: TypX could not initialize a Screen.")
		exit(1)
	}
	m := clipboard.SetMethod(config.GetGlobalOption("clipboard").(string))
	clipErr := clipboard.Initialize(m)

	defer func() {
		if err := recover(); err != nil {
			if screen.Screen != nil {
				screen.Screen.Fini()
			}
			if e, ok := err.(*lua.ApiError); ok {
				fmt.Println("Lua API error:", e)
			} else {
				fmt.Println("TypX encountered an error:", errors.Wrap(err, 2).ErrorStack())
			}
			// immediately backup all buffers with unsaved changes
			for _, b := range buffer.OpenBuffers {
				if b.Modified() {
					b.Backup()
				}
			}
			exit(1)
		}
	}()

	err = config.LoadAllPlugins()
	if err != nil {
		screen.TermMessage(err)
	}

	err = checkBackup("bindings.json")
	if err != nil {
		screen.TermMessage(err)
		exit(1)
	}

	features.Init()
	startBrowserPreview := *flagPreview && !lsp.ShouldUseInlinePreview()
	lsp.Init(lsp.Options{StartPreview: startBrowserPreview, InitialFiles: openArgs})

	action.InitBindings()
	action.InitCommands()

	err = config.RunPluginFn("preinit")
	if err != nil {
		screen.TermMessage(err)
	}

	action.InitGlobals()
	buffer.SetMessager(action.InfoBar)
	b := LoadInput(openArgs)

	if len(b) == 0 {
		// No buffers to open
		screen.Screen.Fini()
		runtime.Goexit()
	}

	action.InitTabs(b)
	if *flagPreview && lsp.ShouldUseInlinePreview() {
		if cp := action.MainTab().CurPane(); cp != nil && cp.Buf != nil && cp.Buf.IsTypst() {
			preview.Open(cp, false)
		}
	}

	err = config.RunPluginFn("init")
	if err != nil {
		screen.TermMessage(err)
	}

	err = config.RunPluginFn("postinit")
	if err != nil {
		screen.TermMessage(err)
	}

	err = config.InitColorscheme()
	if err != nil {
		screen.TermMessage(err)
	}

	if clipErr != nil {
		log.Println(clipErr, " or change 'clipboard' option")
	}

	config.StartAutoSave()
	if a := config.GetGlobalOption("autosave").(float64); a > 0 {
		config.SetAutoTime(a)
	}

	screen.Events = make(chan tcell.Event)

	util.Sigterm = make(chan os.Signal, 1)
	sighup = make(chan os.Signal, 1)
	signal.Notify(util.Sigterm, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGABRT)
	signal.Notify(sighup, syscall.SIGHUP)

	// Here is the event loop which runs in a separate thread
	go func() {
		for {
			screen.Lock()
			e := screen.Screen.PollEvent()
			screen.Unlock()
			if e != nil {
				screen.Events <- e
			}
		}
	}()

	// clear the drawchan so we don't redraw excessively
	// if someone requested a redraw before we started displaying
	for len(screen.DrawChan()) > 0 {
		<-screen.DrawChan()
	}

	// wait for initial resize event
	select {
	case event := <-screen.Events:
		action.Tabs.HandleEvent(event)
	case <-time.After(10 * time.Millisecond):
		// time out after 10ms
	}

	for {
		DoEvent()
	}
}

// DoEvent runs the main action loop of the editor
func DoEvent() {
	var event tcell.Event

	// Deliver coalesced change / cursor notifications before drawing so observer
	// state (conceal cache, outline highlight, LSP status) is rendered this frame.
	buffer.FlushObservers()

	// Display everything
	screen.Screen.Fill(' ', config.DefStyle)
	screen.Screen.HideCursor()
	action.Tabs.Display()
	for _, ep := range action.MainTab().Panes {
		ep.Display()
	}
	action.MainTab().Display()
	action.InfoBar.Display()
	screen.Screen.Show()
	screen.RunPostShowHooks()

	// Check for new events
	select {
	case f := <-shell.Jobs:
		// If a new job has finished while running in the background we should execute the callback
		f.Function(f.Output, f.Args)
	case <-config.Autosave:
		for _, b := range buffer.OpenBuffers {
			b.AutoSave()
		}
	case <-shell.CloseTerms:
		action.Tabs.CloseTerms()
	case event = <-screen.Events:
	case <-screen.DrawChan():
		for len(screen.DrawChan()) > 0 {
			<-screen.DrawChan()
		}
	case <-action.UIQueueReady():
		action.DrainUIQueue()
	case <-sighup:
		exit(0)
	case <-util.Sigterm:
		exit(0)
	}

	if e, ok := event.(*tcell.EventError); ok {
		log.Println("tcell event error: ", e.Error())

		if e.Err() == io.EOF {
			// shutdown due to terminal closing/becoming inaccessible
			exit(0)
		}
		return
	}

	if event != nil {
		_, resize := event.(*tcell.EventResize)
		if resize {
			action.InfoBar.HandleEvent(event)
			action.Tabs.HandleEvent(event)
		} else if action.InfoBar.HasPrompt {
			action.InfoBar.HandleEvent(event)
		} else if action.HandleBottomBarEvent(event) {
			// Consumed by the bottom bar or expanded key menu.
		} else {
			action.Tabs.HandleEvent(event)
		}
	}

	err := config.RunPluginFn("onAnyEvent")
	if err != nil {
		screen.TermMessage(err)
	}
}
