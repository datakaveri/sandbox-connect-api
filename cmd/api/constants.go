package main

import (
	"sandbox-backend-service/pkg/utils"
	"time"
)

const (
	K8sInitialDelay = 500 * time.Millisecond
	K8sMaxDelay     = 5 * time.Second
	K8sMaxAttempts  = 4
	K8sIncrement    = 500 * time.Millisecond
)

var k8sRetryConfig = utils.LinearRetryConfig{
	InitialDelay: K8sInitialDelay,
	MaxDelay:     K8sMaxDelay,
	MaxAttempts:  K8sMaxAttempts,
	Increment:    K8sIncrement,
}
