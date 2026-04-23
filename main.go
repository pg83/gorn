package main

import (
	"fmt"
	"os"
)

func main() {
	exc := Try(func() {
		if len(os.Args) < 2 {
			ThrowFmt("usage: gorn {serve|control|web|wrap|wrap_lower|ignite|exec|prom} [args...]")
		}

		sub := os.Args[1]
		args := os.Args[2:]

		switch sub {
		case "serve":
			serveMain(args)
		case "control":
			controlMain(args)
		case "web":
			webMain(args)
		case "wrap":
			wrapMain(args)
		case "wrap_lower":
			wrapLowerMain(args)
		case "ignite":
			igniteMain(args)
		case "exec":
			execMain(args)
		case "prom":
			promMain(args)
		default:
			ThrowFmt("unknown subcommand: %q", sub)
		}
	})

	exc.Catch(func(e *Exception) {
		fmt.Fprintln(os.Stderr, "gorn:", e.Error())
		os.Exit(1)
	})
}
