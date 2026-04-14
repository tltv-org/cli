//go:build windows

package main

import "os"

func sighupNotify(ch chan<- os.Signal) {}
