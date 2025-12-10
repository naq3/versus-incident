package scheduler

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/VersusControl/versus-incident/pkg/config"
	"github.com/VersusControl/versus-incident/pkg/services"
	"github.com/robfig/cron/v3"
)

// Scheduler manages scheduled alert jobs
type Scheduler struct {
	cron   *cron.Cron
	config *config.ScheduledAlertConfig
	jobs   map[string]cron.EntryID
	mu     sync.RWMutex
}

// NewScheduler creates a new scheduler instance
func NewScheduler(cfg *config.ScheduledAlertConfig) *Scheduler {
	// Create cron with seconds support and location
	location := time.Local
	if cfg.Timezone != "" {
		loc, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			log.Printf("Warning: Invalid timezone '%s', using local timezone: %v", cfg.Timezone, err)
		} else {
			location = loc
		}
	}

	c := cron.New(
		cron.WithLocation(location),
		cron.WithLogger(cron.VerbosePrintfLogger(log.Default())),
	)

	return &Scheduler{
		cron:   c,
		config: cfg,
		jobs:   make(map[string]cron.EntryID),
	}
}

// Start initializes and starts all scheduled jobs
func (s *Scheduler) Start() error {
	if !s.config.Enable {
		log.Println("Scheduled alerts are disabled")
		return nil
	}

	for _, job := range s.config.Jobs {
		if err := s.addJob(job); err != nil {
			return fmt.Errorf("failed to add job '%s': %w", job.Name, err)
		}
	}

	s.cron.Start()
	log.Printf("Scheduler started with %d jobs", len(s.config.Jobs))

	// Log next run times
	for name, entryID := range s.jobs {
		entry := s.cron.Entry(entryID)
		log.Printf("Job '%s' next run: %s", name, entry.Next.Format("2006-01-02 15:04:05"))
	}

	return nil
}

// Stop gracefully stops the scheduler
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	log.Println("Scheduler stopped")
}

// addJob adds a single scheduled job
func (s *Scheduler) addJob(job config.ScheduledJob) error {
	if !job.Enable {
		log.Printf("Job '%s' is disabled, skipping", job.Name)
		return nil
	}

	// Validate cron expression
	schedule := job.Schedule
	if schedule == "" {
		return fmt.Errorf("schedule is required for job '%s'", job.Name)
	}

	// Create the job function
	jobFunc := s.createJobFunc(job)

	// Add the job to cron
	entryID, err := s.cron.AddFunc(schedule, jobFunc)
	if err != nil {
		return fmt.Errorf("invalid cron expression '%s': %w", schedule, err)
	}

	s.mu.Lock()
	s.jobs[job.Name] = entryID
	s.mu.Unlock()

	log.Printf("Added scheduled job '%s' with schedule '%s'", job.Name, schedule)
	return nil
}

// createJobFunc creates the function that will be executed on schedule
func (s *Scheduler) createJobFunc(job config.ScheduledJob) func() {
	return func() {
		log.Printf("Running scheduled job: %s", job.Name)

		// Create Alertmanager client
		client := NewAlertmanagerClient(
			job.Alertmanager.URL,
			job.Alertmanager.Username,
			job.Alertmanager.Password,
		)

		// Fetch firing alerts
		alerts, err := client.GetFiringAlerts()
		if err != nil {
			log.Printf("Error fetching alerts for job '%s': %v", job.Name, err)
			return
		}

		log.Printf("Job '%s': Fetched %d firing alerts from Alertmanager", job.Name, len(alerts))

		// Filter alerts by labels
		matchedAlerts := FilterAlertsByLabels(alerts, job.MatchLabels)
		log.Printf("Job '%s': %d alerts matched label filters", job.Name, len(matchedAlerts))

		if len(matchedAlerts) == 0 {
			log.Printf("Job '%s': No alerts matched, skipping notification", job.Name)
			return
		}

		// Convert to incident payload
		payload := ConvertToIncidentPayload(matchedAlerts)
		if payload == nil {
			log.Printf("Job '%s': Failed to convert alerts to payload", job.Name)
			return
		}

		// Add scheduled metadata
		payload["scheduled_job"] = job.Name
		payload["scheduled_time"] = time.Now().Format(time.RFC3339)

		// Build params for channel override
		params := buildParamsFromJob(job)

		// Send to configured channels via incident service
		if err := services.CreateIncident("scheduled", &payload, &params); err != nil {
			log.Printf("Error sending scheduled alert for job '%s': %v", job.Name, err)
			return
		}

		log.Printf("Job '%s': Successfully sent %d alerts to notification channels", job.Name, len(matchedAlerts))
	}
}

// buildParamsFromJob builds query parameters from job config to override channels
func buildParamsFromJob(job config.ScheduledJob) map[string]string {
	params := make(map[string]string)

	// Disable oncall for scheduled alerts by default
	params["oncall_enable"] = "false"

	// Override channels if specified
	if job.Channels.SlackChannelID != "" {
		params["slack_channel_id"] = job.Channels.SlackChannelID
	}
	if job.Channels.TelegramChatID != "" {
		params["telegram_chat_id"] = job.Channels.TelegramChatID
	}
	if job.Channels.LarkWebhookKey != "" {
		params["lark_other_webhook_url"] = job.Channels.LarkWebhookKey
	}
	if job.Channels.MSTeamsPowerURLKey != "" {
		params["msteams_other_power_url"] = job.Channels.MSTeamsPowerURLKey
	}
	if job.Channels.EmailTo != "" {
		params["email_to"] = job.Channels.EmailTo
	}

	return params
}

// GetJobStatus returns the status of all scheduled jobs
func (s *Scheduler) GetJobStatus() []JobStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var statuses []JobStatus
	for name, entryID := range s.jobs {
		entry := s.cron.Entry(entryID)
		statuses = append(statuses, JobStatus{
			Name:     name,
			NextRun:  entry.Next,
			PrevRun:  entry.Prev,
			Running:  entry.Job != nil,
		})
	}
	return statuses
}

// JobStatus represents the status of a scheduled job
type JobStatus struct {
	Name    string    `json:"name"`
	NextRun time.Time `json:"next_run"`
	PrevRun time.Time `json:"prev_run"`
	Running bool      `json:"running"`
}

// ParseSimpleSchedule converts simple time format (e.g., "09:00") to cron expression
func ParseSimpleSchedule(simpleTime string) (string, error) {
	parts := strings.Split(simpleTime, ":")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid time format, expected HH:MM")
	}

	hour := strings.TrimLeft(parts[0], "0")
	if hour == "" {
		hour = "0"
	}
	minute := strings.TrimLeft(parts[1], "0")
	if minute == "" {
		minute = "0"
	}

	// Cron format: minute hour * * * (every day at specified time)
	return fmt.Sprintf("%s %s * * *", minute, hour), nil
}
