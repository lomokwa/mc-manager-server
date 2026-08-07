package handlers

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/lomokwa/mc-manager/middleware"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// TODO: restrict to allowed origins in production
		return true
	},
}

const (
	consoleWriteTimeout = 10 * time.Second
	consolePingInterval = 30 * time.Second
	// consolePongTimeout must exceed consolePingInterval so a client gets at least one full ping cycle to
	// answer before being considered dead.
	consolePongTimeout = 60 * time.Second
)

// ConsoleHandler upgrades the connection to a WebSocket and streams
// Minecraft server logs to the client while accepting commands from it.
func ConsoleHandler(c *gin.Context) {
	if !services.IsServerRunning() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server is not running"})
		return
	}

	hub := services.GetLogHub()
	if hub == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server log stream not available"})
		return
	}

	// Read once, before the upgrade -- the route's RequirePermission(console.read)
	// already confirmed a JWT is present, so this is always populated here.
	userID, _ := middleware.UserIDFromContext(c)

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Subscribe to log stream
	logCh := hub.Subscribe()
	defer hub.Unsubscribe(logCh)

	// A client dropped without a clean FIN (a dead NAT/proxy route, a crashed browser tab) used to leave
	// ReadMessage() blocked forever with no deadline -- the goroutine below, its logCh subscription, and
	// this connection just leaked until the process restarted. The read deadline + pong handler close that
	// gap: no pong within consolePongTimeout means the next ReadMessage() call returns an error, which the
	// read goroutine already treats as "client gone" and exits on.
	conn.SetReadDeadline(time.Now().Add(consolePongTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(consolePongTimeout))
		return nil
	})

	// Carries a command-send failure from the read goroutine to the write loop below, which is the ONLY
	// goroutine allowed to call conn.WriteMessage/WriteJSON on this connection -- gorilla/websocket
	// explicitly forbids concurrent writers. The previous version wrote directly from the read goroutine
	// here, racing the log-streaming write loop below (could corrupt frames, or hit gorilla's internal
	// "concurrent write to websocket connection" panic if a log line and a command error landed at once).
	errCh := make(chan string, 1)

	// Read commands from client and send to server stdin
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					log.Printf("websocket read error: %v", err)
				}
				return
			}
			cmd := string(msg)
			if cmd == "" {
				continue
			}
			if !services.HasPermission(userID, classifyConsoleInput(cmd)) {
				select {
				case errCh <- "you don't have permission to send that":
				default:
				}
				continue
			}
			if err := services.SendCommand(cmd); err != nil {
				log.Printf("failed to send command %q: %v", cmd, err)
				select {
				case errCh <- err.Error():
				default:
					// a previous error is still queued; drop this one rather than block the read loop
				}
			}
		}
	}()

	pingTicker := time.NewTicker(consolePingInterval)
	defer pingTicker.Stop()

	// The log hub is now long-lived (it outlives any single server run, since
	// the minecraft container — and its log file — can outlive this API
	// process). It's never closed on stop, so a status check replaces the old
	// "hub closed" signal for knowing when the server has gone down.
	statusTicker := time.NewTicker(2 * time.Second)
	defer statusTicker.Stop()

	// Stream log lines to client
	for {
		select {
		case line, ok := <-logCh:
			if !ok {
				// Hub closed (server stopped) — notify client and exit
				conn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
				conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server stopped"))
				return
			}
			conn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				log.Printf("websocket write error: %v", err)
				return
			}

		case errMsg := <-errCh:
			conn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
			if err := conn.WriteJSON(gin.H{"error": errMsg}); err != nil {
				log.Printf("websocket write error: %v", err)
				return
			}

		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("websocket ping failed: %v", err)
				return
			}

		case <-statusTicker.C:
			if !services.IsServerRunning() {
				conn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
				conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server stopped"))
				return
			}

		case <-done:
			// Client disconnected
			return
		}
	}
}

// classifyConsoleInput decides which permission a raw console line needs.
// There is no separate chat channel -- "say <message>" IS how a broadcast is
// sent from the server console (the same convention vanilla's own console
// uses), so it's the one shape that counts as chat; everything else is a
// command. This is deliberately simple rather than a full command parser: it
// can't be fooled into treating a real command as chat (only a literal
// leading "say" ever classifies that way), which is the direction that
// matters for a permission check.
func classifyConsoleInput(cmd string) types.Permission {
	trimmed := strings.TrimSpace(cmd)
	if strings.EqualFold(trimmed, "say") || strings.HasPrefix(strings.ToLower(trimmed), "say ") {
		return types.PermConsoleChat
	}
	return types.PermConsoleCommands
}
