package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creack/pty"
	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
	"github.com/rs/cors"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func main() {
	mux := http.NewServeMux()

	// Setup CORS
	c := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
	})
	server := &http.Server{
		Addr:    ":9000",
		Handler: c.Handler(mux),
	}

	// Watcher setup
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}
	defer watcher.Close()

	userDir := "./user"
	err = watcher.Add(userDir)
	if err != nil {
		panic(err)
	}

	// Terminal setup
	cmd := exec.Command("bash")
	ptyProc, err := pty.Start(cmd)
	if err != nil {
		panic(err)
	}
	defer ptyProc.Close()

	// WebSocket communication
	var clients = make(map[*websocket.Conn]bool)

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			fmt.Println("WebSocket upgrade error:", err)
			return
		}
		defer conn.Close()
		clients[conn] = true

		// Handle incoming messages
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				delete(clients, conn)
				break
			}

			// Handle terminal write messages
			if strings.HasPrefix(string(msg), "terminal:write:") {
				input := strings.TrimPrefix(string(msg), "terminal:write:")
				_, err = ptyProc.Write([]byte(input))
				if err != nil {
					fmt.Println("PTY write error:", err)
				}
			}
		}
	})

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := ptyProc.Read(buf)
			if err != nil {
				break
			}
			for client := range clients {
				client.WriteMessage(websocket.TextMessage, buf[:n])
			}
		}
	}()

	// File change notifications
	go func() {
		for {
			event, ok := <-watcher.Events
			if !ok {
				break
			}
			for client := range clients {
				client.WriteMessage(websocket.TextMessage, []byte("file:refresh:"+event.Name))
			}
		}
	}()

	mux.HandleFunc("/files", func(w http.ResponseWriter, r *http.Request) {
		tree, err := generateFileTree(userDir)
		if err != nil {
			http.Error(w, "Error generating file tree", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"tree": tree})
	})

	mux.HandleFunc("/files/content", func(w http.ResponseWriter, r *http.Request) {
		filePath := r.URL.Query().Get("path")
		if filePath == "" {
			http.Error(w, "Path query parameter missing", http.StatusBadRequest)
			return
		}
		content, err := ioutil.ReadFile(filepath.Join(userDir, filePath))
		if err != nil {
			http.Error(w, "Error reading file", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"content": string(content)})
	})

	fmt.Println("🐳 Docker server running on port 9000")
	if err := server.ListenAndServe(); err != nil {
		panic(err)
	}
}

func generateFileTree(directory string) (map[string]interface{}, error) {
	tree := make(map[string]interface{})

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		parts := strings.Split(relPath, string(os.PathSeparator))
		subTree := tree
		for i, part := range parts {
			if i == len(parts)-1 {
				if info.IsDir() {
					subTree[part] = make(map[string]interface{})
				} else {
					subTree[part] = nil
				}
			} else {
				if _, exists := subTree[part]; !exists {
					subTree[part] = make(map[string]interface{})
				}
				subTree = subTree[part].(map[string]interface{})
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return tree, nil
}
