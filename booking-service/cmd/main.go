package main

import (
	"context"
	"log"
	"net"
	"net/http"

	bookingpb "diploma/api/booking-service-proto"
	"diploma/booking-service/internal/auth"
	"diploma/booking-service/internal/client"
	"diploma/booking-service/internal/config"
	"diploma/booking-service/internal/db"
	"diploma/booking-service/internal/grpcmiddleware"
	"diploma/booking-service/internal/grpcserver"
	"diploma/booking-service/internal/handler"
	"diploma/booking-service/internal/repository"
	"diploma/booking-service/internal/router"
	"diploma/booking-service/internal/service"

	"github.com/joho/godotenv"
	"google.golang.org/grpc"
)

func main() {
	ctx := context.Background()

	_ = godotenv.Load()
	cfg := config.Load()
	if cfg.JWT.Secret == "" {
		log.Fatal("JWT_SECRET is required")
	}

	pool, err := db.NewPool(ctx, db.Config{
		DSN:         cfg.DB.DSN,
		MaxConns:    cfg.DB.MaxConns,
		MinConns:    cfg.DB.MinConns,
		PingTimeout: cfg.DB.PingTimeout,
	})
	if err != nil {
		log.Fatal(err)
	}

	tokenManager := auth.NewTokenManager(cfg.JWT.Secret)

	requestRepo := repository.NewRequestRepository(pool)
	requestService := service.NewRequestService(requestRepo)
	bookingRepo := repository.NewBookingRepository(pool)
	if err := bookingRepo.EnsureWorkflowColumns(ctx); err != nil {
		log.Printf("Booking workflow bootstrap skipped: %v", err)
	}
	if err := bookingRepo.EnsurePerformanceIndexes(ctx); err != nil {
		log.Printf("Booking performance indexes skipped: %v", err)
	}
	bookingService := service.NewBookingService(bookingRepo)

	userClient := client.NewUserClient(
		cfg.User.GRPCAddress,
		cfg.Gateway.SharedSecret,
		cfg.JWT.Secret,
	)
	paymentClient := client.NewPaymentClient(
		cfg.Payment.GRPCAddress,
		cfg.Gateway.SharedSecret,
		cfg.Payment.Provider,
		cfg.Payment.Currency,
	)
	notificationClient := client.NewNotificationClient(
		cfg.Notification.URL,
		cfg.Gateway.SharedSecret,
	)

	h := handler.NewHandler(requestService, bookingService, userClient, paymentClient, notificationClient)
	r := router.SetupRouter(h, tokenManager, cfg.Gateway.SharedSecret)

	grpcListener, err := net.Listen("tcp", ":"+cfg.GRPC.Port)
	if err != nil {
		log.Fatalf("gRPC listen error: %v", err)
	}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(
		grpcmiddleware.Auth(cfg.Gateway.SharedSecret),
	))
	bookingpb.RegisterBookingServiceServer(
		grpcServer,
		grpcserver.New(requestService, bookingService),
	)

	go func() {
		log.Printf("Booking gRPC server listening on :%s", cfg.GRPC.Port)
		if err := grpcServer.Serve(grpcListener); err != nil {
			log.Fatalf("gRPC server error: %v", err)
		}
	}()

	log.Printf("Booking service running on :%s", cfg.HTTP.Port)
	if err := r.Run(":" + cfg.HTTP.Port); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
