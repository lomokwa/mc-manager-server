package handlers

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/lomokwa/mc-manager/services"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		// Non-browser clients send no Origin and are allowed (auth is still
		// required). Browsers always send Origin, so a cross-site page is
		// rejected unless its origin is allow-listed.
		return origin == "" || originAllowed(origin)
	},
}

// originAllowed reports whether a browser Origin may open the console
// WebSocket, using the same CORS_ALLOWED_ORIGINS list as the REST API
// (defaulting to the local dev origins when it is unset).
func originAllowed(origin string) bool {
	raw := os.Getenv("CORS_ALLOWED_ORIGINS")
	if strings.TrimSpace(raw) == "" {
		return origin == "http://localhost:5173" || origin == "http://localhost:8080"
	}
	for _, p := range strings.Split(raw, ",") {
		if strings.TrimSpace(p) == origin {
			return true
		}
	}
	return false
}

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

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Subscribe to log stream
	logCh := hub.Subscribe()
	defer hub.Unsubscribe(logCh)

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
			if err := services.SendCommand(cmd); err != nil {
				log.Printf("failed to send command %q: %v", cmd, err)
				conn.WriteJSON(gin.H{"error": err.Error()})
			}
		}
	}()

	// Stream log lines to client
	for {
		select {
		case line, ok := <-logCh:
			if !ok {
				// Hub closed (server stopped) — notify client and exit
				conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server stopped"))
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				log.Printf("websocket write error: %v", err)
				return
			}
		case <-done:
			// Client disconnected
			return
		}
	}
}
