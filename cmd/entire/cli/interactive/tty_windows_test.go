//go:build windows

package interactive

import (
	"testing"
	"time"
)

// Closing the prompt terminal must not wait for a keypress. A console read
// issued by a reader loop (Bubble Tea's, after the answer) is still pending
// when the caller closes the handle, and os.File.Close waits for it.
func TestPromptTTYClose_WindowsReleasesPendingRead(t *testing.T) {
	t.Parallel()
	tty, err := OpenPromptTTY()
	if err != nil {
		t.Skipf("no console: %v", err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, readErr := tty.Input().Read(make([]byte, 16))
		readDone <- readErr
	}()
	time.Sleep(200 * time.Millisecond) // let the read block in the console

	closeDone := make(chan error, 1)
	go func() { closeDone <- tty.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited for a keypress")
	}
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("the pending read was not released")
	}
}
