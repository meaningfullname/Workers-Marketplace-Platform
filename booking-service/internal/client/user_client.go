package client

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	usermanagementpb "diploma/api/usermanagement-service-proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var ErrCustomerProfileNotFound = errors.New("customer profile not found")

type UserClient struct {
	address       string
	gatewaySecret string
	jwtSecret     []byte

	connOnce sync.Once
	conn     *grpc.ClientConn
	connErr  error
}

func NewUserClient(address string, gatewaySecret string, jwtSecret string) *UserClient {
	return &UserClient{
		address:       strings.TrimSpace(address),
		gatewaySecret: gatewaySecret,
		jwtSecret:     []byte(jwtSecret),
	}
}

func (c *UserClient) GetCustomerProfile(ctx context.Context, token string) (int, error) {
	userID, err := c.userIDFromToken(token)
	if err != nil {
		return 0, err
	}

	resp, err := c.call(ctx, func(ctx context.Context, client usermanagementpb.UserManagementServiceClient) (int64, error) {
		profile, err := client.GetCustomerProfile(ctx, &usermanagementpb.GetProfileRequest{UserId: int64(userID)})
		if err != nil {
			return 0, err
		}
		return profile.GetCustomerProfileId(), nil
	})
	if status.Code(err) == codes.NotFound {
		return 0, ErrCustomerProfileNotFound
	}
	if err != nil {
		return 0, err
	}
	return int(resp), nil
}

func (c *UserClient) GetWorkerProfile(ctx context.Context, token string) (int, error) {
	userID, err := c.userIDFromToken(token)
	if err != nil {
		return 0, err
	}

	resp, err := c.call(ctx, func(ctx context.Context, client usermanagementpb.UserManagementServiceClient) (int64, error) {
		profile, err := client.GetWorkerProfile(ctx, &usermanagementpb.GetProfileRequest{UserId: int64(userID)})
		if err != nil {
			return 0, err
		}
		return profile.GetWorkerProfileId(), nil
	})
	if err != nil {
		return 0, err
	}
	return int(resp), nil
}

func (c *UserClient) HasPaymentMethod(ctx context.Context, token string) (bool, error) {
	userID, err := c.userIDFromToken(token)
	if err != nil {
		return false, err
	}

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if c.gatewaySecret != "" {
		callCtx = metadata.AppendToOutgoingContext(callCtx, "x-gateway-secret", c.gatewaySecret)
	}

	grpcClient, err := c.grpcClient()
	if err != nil {
		return false, err
	}

	resp, err := grpcClient.HasPaymentMethod(
		callCtx,
		&usermanagementpb.GetProfileRequest{UserId: int64(userID)},
	)
	if err != nil {
		return false, err
	}
	return resp.GetHasPaymentMethod(), nil
}

func (c *UserClient) call(ctx context.Context, fn func(context.Context, usermanagementpb.UserManagementServiceClient) (int64, error)) (int64, error) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if c.gatewaySecret != "" {
		callCtx = metadata.AppendToOutgoingContext(callCtx, "x-gateway-secret", c.gatewaySecret)
	}

	grpcClient, err := c.grpcClient()
	if err != nil {
		return 0, err
	}

	return fn(callCtx, grpcClient)
}

// grpcClient создаёт соединение с usermanagement-service один раз и дальше переиспользует его.
// Раньше соединение открывалось и закрывалось на каждый запрос: DNS, TCP и HTTP/2 каждый раз
// заново, а при неудачной попытке gRPC ждёт перед повтором около секунды, отсюда хвост p95/p99.
// ClientConn безопасен для одновременного использования из многих горутин.
func (c *UserClient) grpcClient() (usermanagementpb.UserManagementServiceClient, error) {
	c.connOnce.Do(func() {
		c.conn, c.connErr = grpc.NewClient(c.address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	})
	if c.connErr != nil {
		return nil, c.connErr
	}
	return usermanagementpb.NewUserManagementServiceClient(c.conn), nil
}

func (c *UserClient) userIDFromToken(authHeader string) (int, error) {
	token := strings.TrimSpace(authHeader)
	token = strings.TrimPrefix(token, "Bearer ")
	if token == "" {
		return 0, errors.New("missing Authorization token")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, errors.New("invalid token format")
	}
	if len(c.jwtSecret) > 0 {
		mac := hmac.New(sha256.New, c.jwtSecret)
		mac.Write([]byte(parts[0] + "." + parts[1]))
		expected := mac.Sum(nil)
		actual, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			return 0, err
		}
		if !hmac.Equal(expected, actual) {
			return 0, errors.New("invalid token signature")
		}
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, err
	}

	var payload struct {
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return 0, err
	}
	if payload.Exp > 0 && time.Now().Unix() > payload.Exp {
		return 0, errors.New("token expired")
	}

	userID, err := strconv.Atoi(payload.Sub)
	if err != nil || userID <= 0 {
		return 0, errors.New("invalid token subject")
	}
	return userID, nil
}
