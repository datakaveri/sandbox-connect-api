package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitMQService manages the AMQP connection and publishes audit messages.
// It is fault-tolerant: all errors are caught and logged, never crashing the app.
type RabbitMQService struct {
	config      RabbitMQConfig
	conn        *amqp.Connection
	channel     *amqp.Channel
	mu          sync.Mutex
	isConnected bool
}

// NewRabbitMQService creates a new RabbitMQ service and attempts an initial connection.
// Returns an error only if the configuration is invalid (missing required fields).
// A failed initial connection is logged as a warning — it will be retried on first publish.
func NewRabbitMQService(config RabbitMQConfig) (*RabbitMQService, error) {
	svc := &RabbitMQService{config: config}

	if !svc.validateConfig() {
		return nil, fmt.Errorf("incomplete RabbitMQ configuration: one or more required fields are empty")
	}

	// Attempt initial connection (non-fatal if it fails)
	if err := svc.connect(); err != nil {
		slog.Warn("RabbitMQ initial connection failed, will retry on first publish",
			"error", err,
			"host", config.Host,
			"port", config.Port,
		)
	}

	return svc, nil
}

// validateConfig checks that all required RabbitMQ environment variables are present.
func (s *RabbitMQService) validateConfig() bool {
	missing := []string{}
	if s.config.Host == "" {
		missing = append(missing, "RABBITMQ_HOST")
	}
	if s.config.Port == "" {
		missing = append(missing, "RABBITMQ_PORT")
	}
	if s.config.Username == "" {
		missing = append(missing, "RABBITMQ_USERNAME")
	}
	if s.config.Password == "" {
		missing = append(missing, "RABBITMQ_PASSWORD")
	}
	if s.config.Vhost == "" {
		missing = append(missing, "RABBITMQ_VHOST")
	}
	if s.config.Exchange == "" {
		missing = append(missing, "RABBITMQ_EXCHANGE")
	}
	if s.config.RoutingKey == "" {
		missing = append(missing, "RABBITMQ_ROUTING_KEY")
	}

	if len(missing) > 0 {
		slog.Warn("Missing required RabbitMQ environment variables", "missing", missing)
		return false
	}
	return true
}

// connect establishes a connection to RabbitMQ and opens a channel.
// Uses amqps:// (SSL) for hosts containing "iudx.io", amqp:// otherwise.
// URL-encodes username, password, and vhost to handle special characters.
func (s *RabbitMQService) connect() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isConnected && s.channel != nil {
		return nil
	}

	// URL-encode credentials and vhost to handle special characters like %, #, ^
	encodedUsername := url.QueryEscape(s.config.Username)
	encodedPassword := url.QueryEscape(s.config.Password)
	encodedVhost := url.QueryEscape(s.config.Vhost)

	// Choose protocol based on host (heuristic: iudx.io hosts use SSL)
	protocol := "amqp"
	if strings.Contains(s.config.Host, "iudx.io") {
		protocol = "amqps"
	}

	connURL := fmt.Sprintf("%s://%s:%s@%s:%s/%s",
		protocol, encodedUsername, encodedPassword,
		s.config.Host, s.config.Port, encodedVhost,
	)

	slog.Debug("Attempting RabbitMQ connection",
		"host", s.config.Host,
		"port", s.config.Port,
		"vhost", s.config.Vhost,
		"protocol", protocol,
	)

	conn, err := amqp.DialConfig(connURL, amqp.Config{
		Heartbeat: 60 * time.Second,
		Dial:      amqp.DefaultDial(10 * time.Second),
	})
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to open RabbitMQ channel: %w", err)
	}

	s.conn = conn
	s.channel = ch
	s.isConnected = true

	// Monitor connection closure for auto-reconnect on next publish
	go func() {
		closeErr := <-conn.NotifyClose(make(chan *amqp.Error, 1))
		if closeErr != nil {
			slog.Warn("RabbitMQ connection closed unexpectedly", "error", closeErr)
		}
		s.mu.Lock()
		s.isConnected = false
		s.mu.Unlock()
	}()

	slog.Info("Connected to RabbitMQ successfully",
		"host", s.config.Host,
		"port", s.config.Port,
	)
	return nil
}

// PublishAuditMessage serializes the message as JSON and publishes it to the
// configured exchange with persistent delivery mode.
// Auto-reconnects if the connection was lost.
func (s *RabbitMQService) PublishAuditMessage(message any) error {
	// Ensure connection
	s.mu.Lock()
	needsReconnect := !s.isConnected || s.channel == nil
	s.mu.Unlock()

	if needsReconnect {
		if err := s.connect(); err != nil {
			return fmt.Errorf("failed to reconnect to RabbitMQ: %w", err)
		}
	}

	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal audit message: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.mu.Lock()
	ch := s.channel
	s.mu.Unlock()

	if ch == nil {
		return fmt.Errorf("RabbitMQ channel not available")
	}

	err = ch.PublishWithContext(ctx,
		s.config.Exchange,
		s.config.RoutingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish audit message: %w", err)
	}

	slog.Debug("Audit message published to RabbitMQ",
		"exchange", s.config.Exchange,
		"routingKey", s.config.RoutingKey,
	)
	return nil
}

// Close gracefully shuts down the channel and connection.
func (s *RabbitMQService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	if s.channel != nil {
		if err := s.channel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.channel = nil
	}
	if s.conn != nil {
		if err := s.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.conn = nil
	}
	s.isConnected = false

	if firstErr != nil {
		slog.Warn("Error closing RabbitMQ connection", "error", firstErr)
		return firstErr
	}
	slog.Info("RabbitMQ connection closed gracefully")
	return nil
}
