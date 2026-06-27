package services

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

var (
	// mu guards the server process state below. Hold it (read or write) when
	// touching serverCmd, serverStdin, logHub or serverDone.
	mu          sync.RWMutex
	serverCmd   *exec.Cmd
	serverStdin io.WriteCloser
	logHub      *types.LogHub
	serverDone  chan struct{} // closed once the process has exited and state is cleared

	// stdinMu serializes concurrent writes to the server's stdin.
	stdinMu sync.Mutex
)

func StartServerProcess() (string, error) {
	mu.Lock()
	if serverCmd != nil {
		mu.Unlock()
		return "", fmt.Errorf("server already running")
	}

	log.Printf("Starting Server...")
	cmd := exec.Command("java", "-Xms1G", "-Xmx2G", "-jar", "server.jar", "nogui")
	cmd.Dir = "./minecraft-server"

	stdin, err := cmd.StdinPipe()
	if err != nil {
		mu.Unlock()
		return "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		mu.Unlock()
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		mu.Unlock()
		log.Printf("start server command failed: %v", err)
		return "", fmt.Errorf("failed to start server: %w", err)
	}

	hub := types.NewLogHub()
	done := make(chan struct{})
	serverCmd = cmd
	serverStdin = stdin
	logHub = hub
	serverDone = done
	mu.Unlock()

	// Track player join/leave from the console so the API can report
	// current-session time. Exits when the hub closes (server stops).
	go trackSessions(hub)

	// Pump stdout into the log hub and detect readiness.
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			log.Println(line)
			hub.Broadcast(line)
			if strings.Contains(line, "Done") {
				// Non-blocking: never stall the log pump waiting on a reader.
				select {
				case ready <- line:
				default:
				}
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("error reading server output: %v", err)
		}
		select {
		case ready <- "":
		default:
		}
	}()

	// Reap the process and clear shared state exactly once. This is the only
	// place that calls cmd.Wait(), so callers observe the exit via serverDone.
	go func() {
		waitErr := cmd.Wait()
		log.Printf("server process exited: %v", waitErr)
		mu.Lock()
		hub.Close()
		serverCmd = nil
		serverStdin = nil
		logHub = nil
		serverDone = nil
		mu.Unlock()
		close(done)
	}()

	select {
	case line := <-ready:
		if line == "" {
			return "", fmt.Errorf("server process exited before becoming ready")
		}
		return line, nil
	case <-time.After(120 * time.Second):
		// Kill and let the reaper goroutine clean up shared state.
		cmd.Process.Kill()
		return "", fmt.Errorf("server failed to start within 120 seconds")
	}
}

func StopServerProcess() (string, error) {
	log.Printf("executing stop server command")

	mu.RLock()
	cmd := serverCmd
	done := serverDone
	mu.RUnlock()
	if cmd == nil || done == nil {
		return "", fmt.Errorf("server is not running")
	}

	if err := SendCommand("stop"); err != nil {
		log.Printf("failed to send stop command: %v", err)
		return "", fmt.Errorf("failed to send stop command: %w", err)
	}

	select {
	case <-done:
		log.Printf("server stopped gracefully")
		return "server stopped", nil

	case <-time.After(30 * time.Second):
		log.Printf("server did not stop in time, force killing")
		cmd.Process.Kill()
		<-done // wait for the reaper to finish clearing state
		return "server force-killed after timeout", nil
	}
}

func IsServerRunning() bool {
	mu.RLock()
	defer mu.RUnlock()
	return serverCmd != nil
}

func SendCommand(cmd string) error {
	mu.RLock()
	w := serverStdin
	mu.RUnlock()
	if w == nil {
		return fmt.Errorf("server is not running")
	}

	stdinMu.Lock()
	defer stdinMu.Unlock()
	_, err := w.Write([]byte(cmd + "\n"))
	return err
}

func GetLogHub() *types.LogHub {
	mu.RLock()
	defer mu.RUnlock()
	return logHub
}
