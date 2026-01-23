package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"time"

	"argon-watch-go/internal/config"
)

type Notifier struct {
	config config.NotificationsConfig
}

func NewNotifier(cfg config.NotificationsConfig) *Notifier {
	return &Notifier{config: cfg}
}

func (n *Notifier) Notify(alert AlertHistory, rule config.AlertRule) {
	for _, channel := range rule.Notifications {
		switch channel {
		case "email":
			if n.config.Email.Enabled {
				n.sendEmail(alert, rule)
			}
		case "discord":
			if n.config.Discord.Enabled {
				n.sendDiscord(alert, rule)
			}
		case "slack":
			if n.config.Slack.Enabled {
				n.sendSlack(alert, rule)
			}
		case "desktop":
			n.sendDesktop(alert, rule)
		}
	}
}

func (n *Notifier) sendEmail(alert AlertHistory, rule config.AlertRule) {
	// Simple SMTP implementation
	cfg := n.config.Email
	auth := smtp.PlainAuth("", cfg.SMTP.Auth.User, cfg.SMTP.Auth.Pass, cfg.SMTP.Host)

	to := cfg.To
	msg := []byte(fmt.Sprintf("To: %v\r\n"+
		"Subject: [%s] %s\r\n"+
		"\r\n"+
		"Alert: %s\r\n"+
		"Status: %s\r\n"+
		"Metric: %s\r\n"+
		"Value: %v\r\n"+
		"Threshold: %v\r\n"+
		"Severity: %s\r\n"+
		"Time: %s\r\n",
		to, alert.Severity, rule.Name, rule.Name, alert.Status, rule.Metric, alert.Value, alert.Threshold, alert.Severity, alert.Timestamp))

	addr := fmt.Sprintf("%s:%d", cfg.SMTP.Host, cfg.SMTP.Port)
	err := smtp.SendMail(addr, auth, cfg.From, to, msg)
	if err != nil {
		log.Printf("Email send failed: %v", err)
	} else {
		log.Printf("📧 Email sent: %s", rule.Name)
	}
}

func (n *Notifier) sendDiscord(alert AlertHistory, rule config.AlertRule) {
	color := 0x3B82F6 // Blue
	if alert.Status == "triggered" {
		if alert.Severity == "critical" {
			color = 0xFF0000 // Red
		} else {
			color = 0xFFAA00 // Orange
		}
	} else {
		color = 0x10B981 // Green
	}

	description := "Alert Triggered"
	if alert.Status == "resolved" {
		description = "Alert Resolved"
	}

	body := map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":       fmt.Sprintf("%s %s", getEmoji(alert.Status), rule.Name),
				"description": description,
				"color":       color,
				"fields": []map[string]interface{}{
					{"name": "Metric", "value": rule.Metric, "inline": true},
					{"name": "Value", "value": fmt.Sprintf("%v", alert.Value), "inline": true},
					{"name": "Threshold", "value": fmt.Sprintf("%v", rule.Threshold), "inline": true},
					{"name": "Severity", "value": alert.Severity, "inline": true},
				},
				"timestamp": alert.Timestamp.Format(time.RFC3339),
			},
		},
	}

	sendWebhook(n.config.Discord.WebhookURL, body)
}

func (n *Notifier) sendSlack(alert AlertHistory, rule config.AlertRule) {
	color := "good"
	if alert.Status == "triggered" {
		color = "danger"
	}

	body := map[string]interface{}{
		"text": fmt.Sprintf("%s *%s*", getEmoji(alert.Status), rule.Name),
		"attachments": []map[string]interface{}{
			{
				"color": color,
				"fields": []map[string]interface{}{
					{"title": "Metric", "value": rule.Metric, "short": true},
					{"title": "Value", "value": fmt.Sprintf("%v", alert.Value), "short": true},
					{"title": "Threshold", "value": fmt.Sprintf("%v", rule.Threshold), "short": true},
					{"title": "Severity", "value": alert.Severity, "short": true},
				},
				"footer": "ArgonWatchGo",
				"ts":     alert.Timestamp.Unix(),
			},
		},
	}

	sendWebhook(n.config.Slack.WebhookURL, body)
}

func (n *Notifier) sendDesktop(alert AlertHistory, rule config.AlertRule) {
	// In a headless server env, desktop notifications might not make sense or require specific OS libs.
	// For now we log it, similar to JS implementation fallback.
	log.Printf("🖥️  Desktop notification: %s - %s", rule.Name, alert.Status)
}

func sendWebhook(url string, body interface{}) {
	jsonBody, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Printf("Webhook send failed: %v", err)
		return
	}
	defer resp.Body.Close()
}

func getEmoji(status string) string {
	if status == "triggered" {
		return "🚨"
	}
	return "✅"
}
