// Command claude stands in for a Claude Code process in the site's
// recordings (director.py): it waits until it is killed, while its tty
// echoes whatever Smif types.
package main

import "time"

func main() {
	for {
		time.Sleep(time.Hour)
	}
}
