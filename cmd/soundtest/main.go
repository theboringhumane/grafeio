// soundtest — verification binary for the Grafeio sound layer.
//
//	grafeio-soundtest                   play every sound serially (350ms gaps)
//	grafeio-soundtest --only queued,done
//	                                    play just these sounds
//	grafeio-soundtest --bell-mode       bell mode: print \a per sound, no files
//	grafeio-soundtest --list            list name/wav size only, no playback
//
// Exit code is 0 even when no player exists (prints
// "player: none — file check only"); this is a verification harness, not a
// gate on the host hardware.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/theboringhumane/grafeio/internal/sound"
)

func main() {
	var (
		only     = flag.String("only", "", "comma-separated sound names to play")
		bellMode = flag.Bool("bell-mode", false, "bell mode: print \\a per sound, no wav playback")
		list     = flag.Bool("list", false, "list wav files only, no playback")
	)
	flag.Parse()

	home := os.Getenv("GRAFEIO_HOME")
	if home == "" {
		home = os.Getenv("HOME")
	}
	mode := "on"
	if *bellMode {
		mode = "bell"
	}
	bus := sound.NewBus(mode, home)

	names := sound.Names()
	if *only != "" {
		wanted := strings.Split(*only, ",")
		var filtered []string
		for _, w := range wanted {
			w = strings.TrimSpace(w)
			if !sound.IsValid(w) {
				fmt.Fprintf(os.Stderr, "skipping unknown sound %q\n", w)
				continue
			}
			filtered = append(filtered, w)
		}
		names = filtered
	}

	fmt.Printf("mode: %s | dir: %s\n", bus.Mode(), bus.Dir())
	if bus.PlayerPath() == "" {
		fmt.Println("player: none — file check only")
	} else {
		fmt.Printf("player: %s\n", bus.PlayerPath())
	}

	exit := 0
	for i, name := range names {
		if *bellMode {
			fmt.Printf("[%s] bell-mode: \\a\n", name)
			if err := bus.Play(name); err != nil {
				fmt.Fprintf(os.Stderr, "[%s] bell error: %v\n", name, err)
				exit = 1
			}
			pause(i, len(names))
			continue
		}

		path, err := sound.EnsureWav(bus.Dir(), name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] synth error: %v\n", name, err)
			exit = 1
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] stat error: %v\n", name, err)
			exit = 1
			continue
		}
		fmt.Printf("[%s] file: %s (%d bytes)\n", name, path, st.Size())

		if *list {
			continue
		}
		if bus.PlayerPath() == "" {
			continue
		}

		start := time.Now()
		cmd := exec.Command(bus.PlayerPath(), path)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		runErr := cmd.Run()
		code := 0
		if runErr != nil {
			code = 1
			if ee, ok := runErr.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
			exit = 1
		}
		fmt.Printf("[%s] command: %s %s | duration: %s | exit: %d\n",
			name, bus.PlayerPath(), path, time.Since(start).Round(time.Millisecond), code)
		pause(i, len(names))
	}
	os.Exit(exit)
}

// pause sleeps 350ms between plays so chimes don't overlap.
func pause(i, n int) {
	if i < n-1 {
		time.Sleep(350 * time.Millisecond)
	}
}
