package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"lan-chat/server/internal/filetransfer"
	"lan-chat/server/internal/forum"
	"lan-chat/server/internal/protocol"
	"lan-chat/server/internal/relay"
	"lan-chat/server/internal/room"
)

func intEnv(name string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func main() {
	origins := make(map[string]struct{})
	for _, origin := range strings.Split(env("ALLOWED_ORIGINS", "https://chat.local,https://localhost"), ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			origins[origin] = struct{}{}
		}
	}
	rooms := room.NewManager(intEnv("MAX_ROOMS", 1000), intEnv("MAX_MEMBERS_PER_ROOM", 50))
	// Stored files announce their own expiry to the room; the broadcast runs
	// on the store's timer goroutine with no store or room lock held.
	files := filetransfer.NewStore(filetransfer.Config{
		OnExpired: func(roomID, fileID string) {
			rooms.Broadcast(roomID, "", protocol.Marshal(protocol.FileExpired{V: protocol.Version, Type: "file_expired", FileID: fileID}))
		},
	})
	handler := relay.NewHandler(rooms, relay.Config{AllowedOrigins: origins, MaxConnections: intEnv("MAX_CONNECTIONS", 2000), HandshakeTimeout: 15 * time.Second, ApprovalTimeout: 60 * time.Second, PingInterval: 30 * time.Second, Files: files})
	forumStore, err := forum.NewStore(env("FORUM_DATA_PATH", "/data/forum.json"), intEnv("MAX_FORUM_POSTS", forum.DefaultMaxPosts))
	if err != nil {
		log.Print("fatal:forum_store")
		os.Exit(1)
	}
	forumHandler := forum.NewHandler(forumStore, forum.Config{AllowedOrigins: origins, MaxBodyBytes: forum.MaxRequestBytes})
	filesHandler := filetransfer.NewHandler(files, filetransfer.HandlerConfig{AllowedOrigins: origins})
	mux := http.NewServeMux()
	mux.Handle("/ws", handler)
	mux.Handle("/api/board/posts", forumHandler)
	mux.Handle("/api/board/posts/", forumHandler)
	mux.Handle("/api/file/upload", filesHandler)
	mux.Handle("/api/file/", filesHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: securityHeaders(mux), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		log.Print("server_started")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Print("fatal:listen_failed")
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	handler.CloseAll()
	// Zero every stored file before the HTTP server drains: in-flight
	// downloads observe the wipe via their cancelled contexts.
	files.WipeAll()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	log.Print("server_stopped")
}

func env(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
