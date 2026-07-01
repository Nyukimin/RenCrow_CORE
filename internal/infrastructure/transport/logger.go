package transport

import (
	"context"
	"log"
	"time"

	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

// LoggingTransport はTransportのDecoratorパターンによるログ出力ラッパー
type LoggingTransport struct {
	inner     domaintransport.Transport
	agentName string
}

// NewLoggingTransport は新しいLoggingTransportを作成
func NewLoggingTransport(inner domaintransport.Transport, agentName string) *LoggingTransport {
	return &LoggingTransport{
		inner:     inner,
		agentName: agentName,
	}
}

// Send はメッセージを送信し、ログを出力
func (t *LoggingTransport) Send(ctx context.Context, msg domaintransport.Message) error {
	start := time.Now()
	err := t.inner.Send(ctx, msg)
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[Transport:%s] SEND FAILED %s→%s type=%s elapsed=%v err=%v",
			t.agentName, msg.From, msg.To, msg.Type, elapsed, err)
	} else {
		log.Printf("[Transport:%s] SEND %s→%s type=%s job=%s elapsed=%v",
			t.agentName, msg.From, msg.To, msg.Type, msg.JobID, elapsed)
	}

	return err
}

// Receive はメッセージを受信し、ログを出力
func (t *LoggingTransport) Receive(ctx context.Context) (domaintransport.Message, error) {
	start := time.Now()
	msg, err := t.inner.Receive(ctx)
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[Transport:%s] RECV FAILED elapsed=%v err=%v",
			t.agentName, elapsed, err)
	} else {
		log.Printf("[Transport:%s] RECV %s→%s type=%s job=%s elapsed=%v",
			t.agentName, msg.From, msg.To, msg.Type, msg.JobID, elapsed)
	}

	return msg, err
}

// Close はTransportを閉じる
func (t *LoggingTransport) Close() error {
	log.Printf("[Transport:%s] Closing", t.agentName)
	err := t.inner.Close()
	if err != nil {
		log.Printf("[Transport:%s] Close error: %v", t.agentName, err)
	}
	return err
}

// IsHealthy はTransportの健全性を返す
func (t *LoggingTransport) IsHealthy() bool {
	return t.inner.IsHealthy()
}
