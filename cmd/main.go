package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aika/internal/auth"
	"aika/internal/calls"
	"aika/internal/config"
	"aika/internal/database"
	"aika/internal/httpapi"
	"aika/internal/profilephoto"
	"aika/internal/telegram"
	"aika/internal/users"
	"aika/traits/logger"

	"go.uber.org/zap"
)

func main() {
	log, err := logger.NewLogger()
	if err != nil {
		panic(err)
	}
	defer func() { _ = log.Sync() }()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal("invalid configuration", zap.Error(err))
	}

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := database.Open(rootContext, cfg.DatabasePath)
	if err != nil {
		log.Fatal("database startup failed", zap.Error(err))
	}
	defer store.Close()
	photos, err := profilephoto.New(cfg.ProfilePhotoDir)
	if err != nil {
		log.Fatal("profile photo storage startup failed", zap.Error(err))
	}

	telegramClient := telegram.NewClient(cfg.BotToken, cfg.MiniAppURL, cfg.BotUsername, store, log)
	validator := auth.NewValidator(cfg)
	userService := users.NewService(store)
	// Video call signalling is in-memory state, not stored data: a call is a few seconds of
	// coordination between two browsers that then exchange media directly.
	callRegistry := calls.NewRegistry(calls.Settings{
		InviteTimeout:   cfg.Calls.InviteTimeout,
		SetupTimeout:    cfg.Calls.SetupTimeout,
		PresenceTimeout: cfg.Calls.PresenceTimeout,
	})
	api := httpapi.NewServer(cfg, store, userService, validator, telegramClient, photos, callRegistry, log)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	go telegramClient.Run(rootContext)
	if cfg.Calls.Enabled {
		go callRegistry.Run(rootContext)
	}
	serverErrors := make(chan error, 1)
	go func() {
		log.Info("AikaBot HTTP server started", zap.String("port", cfg.Port))
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-rootContext.Done():
		log.Info("shutdown requested")
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server stopped unexpectedly", zap.Error(err))
		}
		stop()
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		log.Error("graceful shutdown failed", zap.Error(err))
	}
	log.Info("AikaBot stopped")
}
