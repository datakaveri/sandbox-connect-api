package main

import (
	"context"
	"fmt"
	"net/http"
	"sandbox-backend-service/pkg/utils"
)

func (app *application) createNotebook(w http.ResponseWriter, r *http.Request) {
	logger := getLogger(r)
	notebookReq, err := utils.DecodeAndValidate[NotebookRequest](r.Body, logger)
	if err != nil {
		logger.Error("invalid body", "error", err)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid Body")
		return
	}

	if notebookReq.GPU.Limit > 0 && !IsValidGPUResource(notebookReq.GPU.Type) {
		logger.Error("invalid gpu type", "gpu_type", notebookReq.GPU.Type)
		sendResponse(w, r, logger, http.StatusUnprocessableEntity, "Invalid GPU Type")
		return
	}
	if notebookReq.TemplateName != "ai" && notebookReq.TemplateName != "ml" {
		logger.Error("invalid template name", "template_name", notebookReq.TemplateName)
		jsonResponse(w, http.StatusUnprocessableEntity, map[string]string{"error": "Invalid Template Name"})
		return
	}

	memoryRequest := fmt.Sprintf("%dGi", int(notebookReq.MemoryInGi.Request))
	memoryLimit := fmt.Sprintf("%dGi", int(notebookReq.MemoryInGi.Limit))
	storageSize := fmt.Sprintf("%.2fGi", notebookReq.StorageSizeInGi)

	userID := "00000000-0000-0000-0000-000000000000"
	baseArgs := []any{
		userID,
		notebookReq.Name,
		notebookReq.Namespace,
		storageSize,
		notebookReq.PVCName,
		notebookReq.CPU.Request,
		notebookReq.CPU.Limit,
		memoryRequest,
		memoryLimit,
	}
	var query string
	var args []any

	ctx := context.Background()

	var notebookId int64
	if notebookReq.GPU.Limit > 0 {
		query = `
		INSERT INTO notebooks (
			user_id, name, namespace, storage_size, pvc_name, 
			cpu_request, cpu_limit, memory_request, memory_limit, 
			gpu_type, gpu_count, template_name
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
		) RETURNING id`

		args = append(baseArgs, notebookReq.GPU.Type, notebookReq.GPU.Limit, notebookReq.TemplateName)
	} else {
		query = `
		INSERT INTO notebooks (
			user_id, name, namespace, storage_size, pvc_name, 
			cpu_request, cpu_limit, memory_request, memory_limit, 
			template_name
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		) RETURNING id`

		args = append(baseArgs, notebookReq.TemplateName)
	}

	err = app.pgPool.Pool.QueryRow(ctx, query, args...).Scan(&notebookId)
	if err != nil {
		logger.Error("failed to create notebook", "error", err)
		sendResponse(w, r, logger, http.StatusInternalServerError, "Failed to create notebook")
		return
	}
	sendResponse(w, r, logger, http.StatusCreated, "Notebook created successfully")
}
