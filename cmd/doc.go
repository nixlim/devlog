// Package cmd contains the implementations of devlog subcommands.
//
// Each subcommand lives in its own file (e.g. init.go, capture.go) and
// exports a single entry function with the signature
// func(args []string) int. main.go dispatches to these based on os.Args[1].
package cmd
