package handlers

import (
	"log"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/lomokwa/mc-manager/services"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// TODO: restrict to allowed origins in production
		return true
	},
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

	// A Gorilla connection supports only one concurrent writer. Both this
	// goroutine (log streaming) and the command-reader goroutine below write
	// to conn, so all writes must be serialized through writeMu.
	var writeMu sync.Mutex

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
				writeMu.Lock()
				conn.WriteJSON(gin.H{"error": err.Error()})
				writeMu.Unlock()
			}
		}
	}()

	// Stream log lines to client
	for {
		select {
		case line, ok := <-logCh:
			if !ok {
				// Hub closed (server stopped) — notify client and exit
				writeMu.Lock()
				conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server stopped"))
				writeMu.Unlock()
				return
			}
			writeMu.Lock()
			err := conn.WriteMessage(websocket.TextMessage, []byte(line))
			writeMu.Unlock()
			if err != nil {
				log.Printf("websocket write error: %v", err)
				return
			}
		case <-done:
			// Client disconnected
			return
		}
	}
}
