package controllers

import (
	"github.com/VersusControl/versus-incident/pkg/scheduler"
	"github.com/gofiber/fiber/v2"
)

var alertScheduler *scheduler.Scheduler

// SetScheduler sets the scheduler instance for the controller
func SetScheduler(s *scheduler.Scheduler) {
	alertScheduler = s
}

// GetSchedulerStatus returns the status of all scheduled jobs
func GetSchedulerStatus(c *fiber.Ctx) error {
	if alertScheduler == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"status":  "disabled",
			"message": "Scheduled alerts are not enabled",
		})
	}

	statuses := alertScheduler.GetJobStatus()
	return c.JSON(fiber.Map{
		"status": "enabled",
		"jobs":   statuses,
	})
}
