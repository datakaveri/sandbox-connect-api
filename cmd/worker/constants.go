package main

import (
	"sandbox-backend-service/pkg/utils"
	"time"
)

const (
	PollInterval            = 1 * time.Second
	UploadPodWatcherTimeout = 4 * time.Minute
	DBTransactionTimeout    = 30 * time.Second
	DBReadTimeout           = 15 * time.Second
	DBWriteTimeout          = 20 * time.Second
	K8sCreationTimeout      = 30 * time.Second
	K8sPVCWatcherTimeout    = 2 * time.Minute
	K8sDeletionTimeout      = 30 * time.Second
	NotebookCreationTimeout = 1 * time.Minute
	NotebookDeletionTimeout = 1 * time.Minute
)

const (
	K8sInitialDelay = 500 * time.Millisecond
	K8sMaxDelay     = 30 * time.Second
	K8sMaxAttempts  = 5
	K8sFactor       = 2.0

	DBInitialDelay = 200 * time.Millisecond
	DBMaxDelay     = 30 * time.Second
	DBMaxAttempts  = 5
	DBFactor       = 2.0
)

var k8sRetryConfig = utils.BackoffConfig{
	InitialDelay: K8sInitialDelay,
	MaxDelay:     K8sMaxDelay,
	MaxAttempts:  K8sMaxAttempts,
	Factor:       K8sFactor,
}

var dbRetryConfig = utils.BackoffConfig{
	InitialDelay: DBInitialDelay,
	MaxDelay:     DBMaxDelay,
	MaxAttempts:  DBMaxAttempts,
	Factor:       DBFactor,
}
