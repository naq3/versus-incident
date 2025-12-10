package scheduler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AlertmanagerClient handles communication with Alertmanager API
type AlertmanagerClient struct {
	baseURL    string
	httpClient *http.Client
	username   string
	password   string
}

// AlertmanagerAlert represents an alert from Alertmanager API
type AlertmanagerAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
	Status      AlertStatus       `json:"status"`
	Receivers   []Receiver        `json:"receivers"`
	Fingerprint string            `json:"fingerprint"`
	GeneratorURL string           `json:"generatorURL"`
}

// AlertStatus represents the status of an alert
type AlertStatus struct {
	State       string   `json:"state"`
	SilencedBy  []string `json:"silencedBy"`
	InhibitedBy []string `json:"inhibitedBy"`
}

// Receiver represents an alertmanager receiver
type Receiver struct {
	Name string `json:"name"`
}

// NewAlertmanagerClient creates a new Alertmanager client
func NewAlertmanagerClient(baseURL, username, password string) *AlertmanagerClient {
	return &AlertmanagerClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		username: username,
		password: password,
	}
}

// GetFiringAlerts fetches all currently firing alerts from Alertmanager
func (c *AlertmanagerClient) GetFiringAlerts() ([]AlertmanagerAlert, error) {
	url := fmt.Sprintf("%s/api/v2/alerts?active=true&silenced=false&inhibited=false", c.baseURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add basic auth if credentials provided
	if c.username != "" && c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch alerts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("alertmanager returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var alerts []AlertmanagerAlert
	if err := json.Unmarshal(body, &alerts); err != nil {
		return nil, fmt.Errorf("failed to parse alerts: %w", err)
	}

	// Filter only firing alerts
	var firingAlerts []AlertmanagerAlert
	for _, alert := range alerts {
		if alert.Status.State == "active" {
			firingAlerts = append(firingAlerts, alert)
		}
	}

	return firingAlerts, nil
}

// FilterAlertsByLabels filters alerts that match the given label matchers
// matchLabels: map of label key -> value that must match exactly
func FilterAlertsByLabels(alerts []AlertmanagerAlert, matchLabels map[string]string) []AlertmanagerAlert {
	if len(matchLabels) == 0 {
		return alerts
	}

	var filtered []AlertmanagerAlert
	for _, alert := range alerts {
		if matchesLabels(alert.Labels, matchLabels) {
			filtered = append(filtered, alert)
		}
	}
	return filtered
}

// matchesLabels checks if alert labels contain all required label matches
func matchesLabels(alertLabels, matchLabels map[string]string) bool {
	for key, value := range matchLabels {
		if alertValue, exists := alertLabels[key]; !exists || alertValue != value {
			return false
		}
	}
	return true
}

// ConvertToIncidentPayload converts Alertmanager alerts to Versus incident format
func ConvertToIncidentPayload(alerts []AlertmanagerAlert) map[string]interface{} {
	if len(alerts) == 0 {
		return nil
	}

	// Build alerts array in Alertmanager webhook format
	alertsPayload := make([]map[string]interface{}, 0, len(alerts))
	for _, alert := range alerts {
		alertPayload := map[string]interface{}{
			"status":       alert.Status.State,
			"labels":       alert.Labels,
			"annotations":  alert.Annotations,
			"startsAt":     alert.StartsAt.Format(time.RFC3339),
			"endsAt":       alert.EndsAt.Format(time.RFC3339),
			"fingerprint":  alert.Fingerprint,
			"generatorURL": alert.GeneratorURL,
		}
		alertsPayload = append(alertsPayload, alertPayload)
	}

	// Build common labels from first alert (they should be similar)
	firstAlert := alerts[0]

	payload := map[string]interface{}{
		"receiver":          "scheduled-alert",
		"status":            "firing",
		"alerts":            alertsPayload,
		"commonLabels":      firstAlert.Labels,
		"commonAnnotations": firstAlert.Annotations,
		"externalURL":       "",
		"groupKey":          fmt.Sprintf("scheduled-%d", time.Now().Unix()),
	}

	return payload
}
