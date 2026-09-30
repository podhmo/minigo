package main

import "example.com/enums/models"

type Task struct {
	CurrentStatus models.Status
	TaskPriority  models.Priority
}
